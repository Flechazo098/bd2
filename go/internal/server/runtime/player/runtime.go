// Package player owns the execution order and lifecycle of one account.
package player

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"bd2server/internal/server/domain/command"
)

var (
	ErrMailboxFull       = errors.New("player mailbox capacity exhausted")
	ErrClosed            = errors.New("player runtime is closing")
	ErrUnavailable       = errors.New("player runtime requires recovery")
	ErrIdentityConflict  = errors.New("request identity has a different digest")
	ErrWrongAccount      = errors.New("command belongs to another account")
	ErrGenerationExpired = errors.New("accepted player command belongs to a recovered session generation")
)

var runtimeGeneration atomic.Uint64

type Request struct {
	Path string
	Body []byte
}

type Command struct {
	Identity           command.Identity
	Digest             [32]byte
	Requests           []Request
	LoginSessionKey    []byte
	ExpectedGeneration uint64
	// Cancellation records the accepting caller's cancellation signal; Execute's
	// separate context remains live until the accepted transaction is settled.
	Cancellation context.Context
}

type Response struct {
	PacketCode   int
	Body         []byte
	Notification []byte
}

type Timing struct {
	Queue, Execute, Observer, Begin, Commit, Rollback time.Duration
}

type Reply struct {
	Responses  []Response
	Timing     Timing
	Generation uint64
}

// Execute must atomically persist assets and the identity/digest receipt before
// returning a successful reply. Its receipt survives runtime unload and restart.
type Executor interface {
	Execute(context.Context, Command) (Reply, error)
}

type RecoveryRequiredError interface {
	error
	RequiresRecovery() bool
}

type Failure struct {
	Cause            error
	RecoveryRequired bool
	AlreadyRecovered bool
}

func (f Failure) Error() string {
	if f.Cause == nil {
		return ErrUnavailable.Error()
	}
	return f.Cause.Error()
}
func (f Failure) Unwrap() error          { return f.Cause }
func (f Failure) RequiresRecovery() bool { return f.RecoveryRequired }
func (f Failure) RecoveryComplete() bool { return f.AlreadyRecovered }

type Recoverer interface {
	Recover(context.Context) error
}

type Closer interface {
	Close() error
}

type Limits struct {
	MaxCommands       int
	MaxBytes          int64
	CompletedReceipts int
}

type Future struct {
	done  chan struct{}
	reply Reply
	err   error
}

// Wait cancels only this caller's wait. Accepted commands retain their outcome.
func (f *Future) Wait(ctx context.Context) (Reply, error) {
	select {
	case <-f.done:
		return cloneReply(f.reply), f.err
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
}

type receipt struct {
	digest [32]byte
	future *Future
	bytes  int64
}

type work struct {
	command    Command
	ctx        context.Context
	future     *Future
	bytes      int64
	recovery   bool
	acceptedAt time.Time
	generation uint64
}

type Runtime struct {
	accountID      string
	executor       Executor
	limits         Limits
	mu             sync.Mutex
	queue          chan work
	receipts       map[command.Identity]*receipt
	completed      []command.Identity
	completedBytes int64
	pending        int
	bytes          int64
	closing        bool
	failed         error
	done           chan struct{}
	closeErr       error
	generation     atomic.Uint64
}

func New(accountID string, executor Executor, limits Limits) (*Runtime, error) {
	if accountID == "" || executor == nil {
		return nil, errors.New("player runtime requires account identity and executor")
	}
	if limits.MaxCommands == 0 {
		limits.MaxCommands = 128
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = 8 << 20
	}
	if limits.CompletedReceipts == 0 {
		limits.CompletedReceipts = 256
	}
	if limits.MaxCommands < 1 || limits.MaxBytes < 1 || limits.CompletedReceipts < 1 {
		return nil, errors.New("player runtime limits must be positive")
	}
	r := &Runtime{accountID: accountID, executor: executor, limits: limits,
		queue: make(chan work, limits.MaxCommands), receipts: make(map[command.Identity]*receipt), done: make(chan struct{})}
	r.generation.Store(runtimeGeneration.Add(1))
	go r.run()
	return r, nil
}

func (r *Runtime) Generation() uint64 { return r.generation.Load() }

// Submit's successful return is the acceptance boundary, including for batches.
// Executor receives owned copies and a context detached from network cancellation.
func (r *Runtime) Submit(ctx context.Context, c Command) (*Future, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Identity.AccountID != r.accountID {
		return nil, ErrWrongAccount
	}
	if c.Identity.SessionID == "" || c.Identity.RequestID == "" || len(c.Requests) == 0 {
		return nil, errors.New("command requires session, request identity and requests")
	}
	size := commandBytes(c)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.closing {
		return nil, ErrClosed
	}
	if c.ExpectedGeneration != 0 && c.ExpectedGeneration != r.generation.Load() {
		return nil, ErrGenerationExpired
	}
	if previous, exists := r.receipts[c.Identity]; exists {
		if previous.digest != c.Digest {
			return nil, ErrIdentityConflict
		}
		return previous.future, nil
	}
	if r.failed != nil {
		return nil, errors.Join(ErrUnavailable, r.failed)
	}
	if r.pending >= r.limits.MaxCommands || size > r.limits.MaxBytes-r.bytes {
		return nil, ErrMailboxFull
	}
	f := &Future{done: make(chan struct{})}
	c.Cancellation = ctx
	r.receipts[c.Identity] = &receipt{digest: c.Digest, future: f}
	r.pending++
	r.bytes += size
	r.queue <- work{command: cloneCommand(c), ctx: context.WithoutCancel(ctx), future: f, bytes: size, acceptedAt: time.Now(), generation: r.generation.Load()}
	return f, nil
}

// Recover runs on the owner goroutine, after any previously accepted commands.
func (r *Runtime) Recover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	if r.closing {
		r.mu.Unlock()
		return ErrClosed
	}
	const recoveryBytes = 160
	if r.pending >= r.limits.MaxCommands || recoveryBytes > r.limits.MaxBytes-r.bytes {
		r.mu.Unlock()
		return ErrMailboxFull
	}
	f := &Future{done: make(chan struct{})}
	r.pending++
	r.bytes += recoveryBytes
	r.queue <- work{ctx: context.WithoutCancel(ctx), future: f, recovery: true, bytes: recoveryBytes}
	r.mu.Unlock()
	_, err := f.Wait(ctx)
	return err
}

// Close rejects new commands and drains every accepted command. A cancelled
// caller can stop waiting; draining and executor disposal continue in the owner.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.queue)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return r.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) run() {
	defer close(r.done)
	for w := range r.queue {
		var reply Reply
		var err error
		queued := time.Since(w.acceptedAt)
		if w.recovery {
			err = r.recover(w.ctx)
		} else {
			r.mu.Lock()
			failure := r.failed
			r.mu.Unlock()
			if w.generation != r.generation.Load() {
				err = ErrGenerationExpired
			} else if failure != nil {
				err = errors.Join(ErrUnavailable, failure)
			} else {
				started := time.Now()
				reply, err = safeExecute(r.executor, w.ctx, w.command)
				if reply.Timing.Execute == 0 {
					reply.Timing.Execute = time.Since(started)
				}
				if requiresRecovery(err) {
					if !needsRecovery(err) {
						r.generation.Store(runtimeGeneration.Add(1))
					} else {
						r.mu.Lock()
						r.failed = err
						r.mu.Unlock()
						if _, ok := r.executor.(Recoverer); ok {
							if recoveryErr := r.recover(w.ctx); recoveryErr != nil {
								err = errors.Join(err, recoveryErr)
							}
						}
					}
				}
			}
		}
		if !w.recovery {
			reply.Timing.Queue = queued
		}
		reply.Generation = r.generation.Load()
		w.future.reply, w.future.err = cloneReply(reply), err
		r.mu.Lock()
		r.pending--
		r.bytes -= w.bytes
		if !w.recovery {
			if err == nil {
				entry := r.receipts[w.command.Identity]
				entry.bytes = w.bytes + replyBytes(reply)
				r.completedBytes += entry.bytes
				r.completed = append(r.completed, w.command.Identity)
				for len(r.completed) > r.limits.CompletedReceipts || r.completedBytes > r.limits.MaxBytes {
					r.completedBytes -= r.receipts[r.completed[0]].bytes
					delete(r.receipts, r.completed[0])
					r.completed = slices.Delete(r.completed, 0, 1)
				}
			} else {
				delete(r.receipts, w.command.Identity)
			}
		}
		close(w.future.done)
		r.mu.Unlock()
	}
	if closer, ok := r.executor.(Closer); ok {
		r.closeErr = safeCall(closer.Close)
	}
}

func (r *Runtime) recover(ctx context.Context) error {
	recoverer, ok := r.executor.(Recoverer)
	if !ok {
		return ErrUnavailable
	}
	err := safeCall(func() error { return recoverer.Recover(ctx) })
	r.mu.Lock()
	r.failed = err
	if err == nil {
		r.generation.Store(runtimeGeneration.Add(1))
	}
	r.mu.Unlock()
	return err
}

func safeExecute(executor Executor, ctx context.Context, c Command) (reply Reply, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = Failure{Cause: fmt.Errorf("player executor panic: %v", value), RecoveryRequired: true}
		}
	}()
	return executor.Execute(ctx, c)
}

func safeCall(fn func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("player lifecycle panic: %v", value)
		}
	}()
	return fn()
}

func requiresRecovery(err error) bool {
	return recoveryErrorMatches(err, func(failure RecoveryRequiredError) bool { return failure.RequiresRecovery() })
}

func needsRecovery(err error) bool {
	return recoveryErrorMatches(err, func(failure RecoveryRequiredError) bool {
		if !failure.RequiresRecovery() {
			return false
		}
		completed, ok := failure.(interface{ RecoveryComplete() bool })
		return !ok || !completed.RecoveryComplete()
	})
}

func recoveryErrorMatches(err error, matches func(RecoveryRequiredError) bool) bool {
	if err == nil {
		return false
	}
	if failure, ok := err.(RecoveryRequiredError); ok && matches(failure) {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(wrapped.Unwrap(), func(cause error) bool { return recoveryErrorMatches(cause, matches) })
	case interface{ Unwrap() error }:
		return recoveryErrorMatches(wrapped.Unwrap(), matches)
	}
	return false
}

func commandBytes(c Command) int64 {
	bytes := int64(len(c.Identity.AccountID)) + int64(len(c.Identity.SessionID)) + int64(len(c.Identity.RequestID)) + int64(len(c.LoginSessionKey)) + 160
	for _, request := range c.Requests {
		bytes += int64(len(request.Path)) + int64(len(request.Body)) + 48
	}
	return bytes
}

func cloneCommand(c Command) Command {
	c.Requests = slices.Clone(c.Requests)
	for i := range c.Requests {
		c.Requests[i].Body = slices.Clone(c.Requests[i].Body)
	}
	c.LoginSessionKey = slices.Clone(c.LoginSessionKey)
	return c
}

func cloneReply(reply Reply) Reply {
	reply.Responses = slices.Clone(reply.Responses)
	for i := range reply.Responses {
		reply.Responses[i].Body = slices.Clone(reply.Responses[i].Body)
		reply.Responses[i].Notification = slices.Clone(reply.Responses[i].Notification)
	}
	return reply
}

func replyBytes(reply Reply) int64 {
	bytes := int64(80)
	for _, response := range reply.Responses {
		bytes += int64(len(response.Body)) + int64(len(response.Notification)) + 64
	}
	return bytes
}
