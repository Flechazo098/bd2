package delivery

import (
	"bd2server/internal/common/util"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
)

type Router interface {
	Acquire(context.Context, string) (*player.Runtime, func(), error)
}

type Consumer interface {
	Consume(context.Context, outbox.Pending) error
}

type Discovery interface {
	Accounts(context.Context, string, int) ([]string, error)
	HasPending(context.Context, string) (bool, error)
}

type Dispatcher struct {
	router    Router
	consumer  Consumer
	discovery Discovery
	queue     chan string
	mu        sync.Mutex
	scheduled map[string]bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	session   string
	next      atomic.Uint64
}

func New(router Router, consumer Consumer, discovery Discovery) (*Dispatcher, error) {
	if router == nil || consumer == nil || discovery == nil {
		return nil, errors.New("delivery requires player routing, consumer and discovery")
	}
	nonce, err := util.RandomHex(16)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{router: router, consumer: consumer, discovery: discovery, queue: make(chan string, 128), scheduled: map[string]bool{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), session: "delivery:" + nonce}
	var workers sync.WaitGroup
	workers.Go(d.scan)
	for range 4 {
		workers.Go(d.run)
	}
	go func() { workers.Wait(); close(d.done) }()
	return d, nil
}

func (d *Dispatcher) Wake(account string) {
	if account == "" || d.ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.scheduled[account]; exists {
		d.scheduled[account] = true
		return
	}
	select {
	case d.queue <- account:
		d.scheduled[account] = false
	default:
	}
}

func (d *Dispatcher) Close(ctx context.Context) error {
	d.cancel()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) run() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case account := <-d.queue:
			ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
			err := d.deliver(ctx, account)
			cancel()
			d.mu.Lock()
			again := d.scheduled[account]
			delete(d.scheduled, account)
			d.mu.Unlock()
			if err == nil && again {
				d.Wake(account)
			}
			if err != nil && d.ctx.Err() == nil {
				slog.Warn("shared event delivery deferred", "account_id", account, "error", err)
			}
		}
	}
}

func (d *Dispatcher) scan() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := d.discover(); err != nil && d.ctx.Err() == nil {
			slog.Warn("outbox discovery deferred", "error", err)
		}
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) discover() error {
	for cursor := ""; ; {
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
		accounts, err := d.discovery.Accounts(ctx, cursor, 128)
		cancel()
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return nil
		}
		for _, account := range accounts {
			if account <= cursor {
				return errors.New("outbox discovery returned nonadvancing account cursor")
			}
			cursor = account
			ctx, cancel := context.WithTimeout(d.ctx, 2*time.Second)
			pending, err := d.discovery.HasPending(ctx, account)
			cancel()
			if err != nil {
				return err
			}
			if pending {
				d.Wake(account)
			}
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, account string) error {
	owner, release, err := d.router.Acquire(ctx, account)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("outbox player lease is missing")
	}
	defer release()
	for range (outbox.MaxPending / 32) + 1 {
		body, err := d.execute(ctx, owner, account, player.ReadOutbox, nil)
		if err != nil {
			return err
		}
		var events []outbox.Pending
		if err := json.Unmarshal(body, &events); err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		if len(events) > 32 {
			return errors.New("outbox page exceeds delivery bound")
		}
		for _, event := range events {
			if event.Envelope.Source != account {
				return errors.New("outbox source does not match owning account")
			}
			if err := d.consumer.Consume(ctx, event); err != nil {
				return err
			}
			ack, err := json.Marshal(struct {
				Key    string `json:"key"`
				Digest string `json:"digest"`
			}{event.Key, event.Digest})
			if err != nil {
				return err
			}
			if _, err := d.execute(ctx, owner, account, player.AcknowledgeOutbox, ack); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Dispatcher) execute(ctx context.Context, owner *player.Runtime, account string, kind player.SystemKind, body []byte) ([]byte, error) {
	id := fmt.Sprintf("%d:%d", kind, d.next.Add(1))
	digest := sha256.Sum256(append([]byte{byte(kind)}, body...))
	future, err := owner.Submit(ctx, player.Command{Identity: command.Identity{AccountID: account, SessionID: d.session, RequestID: id}, Digest: digest, System: &player.SystemRequest{Kind: kind, Body: body}})
	if err != nil {
		return nil, err
	}
	reply, err := future.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if len(reply.Responses) != 1 {
		return nil, errors.New("outbox command returned an invalid response")
	}
	return reply.Responses[0].Body, nil
}
