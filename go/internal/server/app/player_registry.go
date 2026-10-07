package app

import (
	"bd2server/internal/server/runtime/player"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	playerLoadWorkers  = 4
	playerLoadQueue    = 128
	maxResidentPlayers = 1024
)

var errPlayerLoadCapacity = errors.New("player load capacity exhausted")

type playerSlot struct {
	ready      chan struct{}
	closed     chan struct{}
	runtime    *player.Runtime
	err        error
	leases     int
	lastUsed   time.Time
	retiring   bool
	retiredErr error
	retireOnce sync.Once
}

type playerLoad struct {
	accountID string
	slot      *playerSlot
}

type playerRegistry struct {
	factory   *PlayerFactory
	mu        sync.Mutex
	players   map[string]*playerSlot
	closing   bool
	stop      chan struct{}
	done      chan struct{}
	loadDone  chan struct{}
	closeDone chan struct{}
	loads     chan playerLoad
	closeErr  error
	idle      time.Duration
}

func newPlayerRegistry(factory *PlayerFactory, idle time.Duration) *playerRegistry {
	r := &playerRegistry{factory: factory, players: make(map[string]*playerSlot), stop: make(chan struct{}), done: make(chan struct{}), loadDone: make(chan struct{}), closeDone: make(chan struct{}), loads: make(chan playerLoad, playerLoadQueue), idle: idle}
	var workers sync.WaitGroup
	for range playerLoadWorkers {
		workers.Go(func() {
			for load := range r.loads {
				r.load(load.accountID, load.slot)
			}
		})
	}
	go func() { workers.Wait(); close(r.loadDone) }()
	go r.sweep()
	return r
}

func accountDirectoryName(accountID string) string {
	digest := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(digest[:])
}

func (r *playerRegistry) Acquire(ctx context.Context, accountID string) (*player.Runtime, func(), error) {
	if accountID == "" {
		return nil, nil, errors.New("player account identity is empty")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r.mu.Lock()
		if r.closing {
			r.mu.Unlock()
			return nil, nil, player.ErrClosed
		}
		slot := r.players[accountID]
		if slot != nil && slot.retiring {
			if slot.retiredErr != nil {
				err := slot.retiredErr
				r.mu.Unlock()
				return nil, nil, errors.Join(player.ErrUnavailable, err)
			}
			r.mu.Unlock()
			select {
			case <-slot.closed:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		if slot == nil {
			if len(r.players) >= maxResidentPlayers {
				r.mu.Unlock()
				return nil, nil, errors.Join(player.ErrMailboxFull, errPlayerLoadCapacity)
			}
			slot = &playerSlot{ready: make(chan struct{}), closed: make(chan struct{}), lastUsed: time.Now()}
			select {
			case r.loads <- playerLoad{accountID: accountID, slot: slot}:
				r.players[accountID] = slot
			default:
				r.mu.Unlock()
				return nil, nil, errors.Join(player.ErrMailboxFull, errPlayerLoadCapacity)
			}
		}
		slot.leases++
		r.mu.Unlock()
		var once sync.Once
		release := func() { once.Do(func() { r.mu.Lock(); slot.leases--; slot.lastUsed = time.Now(); r.mu.Unlock() }) }
		select {
		case <-slot.ready:
			r.mu.Lock()
			err := slot.err
			if ctx.Err() != nil {
				err = errors.Join(err, ctx.Err())
			}
			if r.closing {
				err = errors.Join(player.ErrClosed, err)
			}
			runtime := slot.runtime
			r.mu.Unlock()
			if err != nil {
				release()
				return nil, nil, err
			}
			return runtime, release, nil
		case <-ctx.Done():
			release()
			return nil, nil, ctx.Err()
		}
	}
}

func (r *playerRegistry) load(accountID string, slot *playerSlot) {
	r.mu.Lock()
	if r.closing {
		slot.err = player.ErrClosed
		close(slot.ready)
		delete(r.players, accountID)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	runtime, err := r.open(accountID)
	r.mu.Lock()
	slot.runtime, slot.err = runtime, err
	close(slot.ready)
	if err != nil && r.players[accountID] == slot {
		delete(r.players, accountID)
	}
	r.mu.Unlock()
	if err != nil {
		slog.Error("player load failed", "account_id", accountID, "error", err)
	}
}

func (r *playerRegistry) open(accountID string) (runtime *player.Runtime, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("load player panic: %v", value)
		}
	}()
	owner, err := r.factory.open(accountID)
	if err != nil {
		return nil, err
	}
	runtime, err = player.New(accountID, owner, player.Limits{})
	if err != nil {
		err = errors.Join(err, owner.Close())
	}
	return runtime, err
}

func (r *playerRegistry) sweep() {
	defer close(r.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			r.expire(now)
		case <-r.stop:
			return
		}
	}
}

func (r *playerRegistry) expire(now time.Time) {
	r.mu.Lock()
	var expired []playerLoad
	if !r.closing {
		for accountID, slot := range r.players {
			select {
			case <-slot.ready:
				if !slot.retiring && slot.leases == 0 && now.Sub(slot.lastUsed) >= r.idle && slot.runtime != nil {
					slot.retiring = true
					expired = append(expired, playerLoad{accountID: accountID, slot: slot})
				}
			default:
			}
		}
	}
	r.mu.Unlock()
	for _, load := range expired {
		r.retire(load.accountID, load.slot)
	}
}

func (r *playerRegistry) retire(accountID string, slot *playerSlot) {
	slot.retireOnce.Do(func() {
		err := slot.runtime.Close(context.Background())
		r.mu.Lock()
		slot.retiredErr = err
		if err == nil && r.players[accountID] == slot {
			delete(r.players, accountID)
		}
		r.closeErr = errors.Join(r.closeErr, err)
		close(slot.closed)
		r.mu.Unlock()
		if err != nil {
			slog.Error("player close failed", "account_id", accountID, "error", err)
		}
	})
}

func (r *playerRegistry) shutdown() {
	defer close(r.closeDone)
	<-r.loadDone
	<-r.done
	r.mu.Lock()
	var loaded []playerLoad
	for accountID, slot := range r.players {
		if slot.runtime != nil {
			slot.retiring = true
			loaded = append(loaded, playerLoad{accountID: accountID, slot: slot})
		}
	}
	r.mu.Unlock()
	for _, load := range loaded {
		r.retire(load.accountID, load.slot)
	}
}

func (r *playerRegistry) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.stop)
		close(r.loads)
		go r.shutdown()
	}
	r.mu.Unlock()
	select {
	case <-r.closeDone:
		return r.closeErr
	case <-ctx.Done():
		return fmt.Errorf("player registry shutdown: %w", ctx.Err())
	}
}
