// Package executor owns goroutines that are spawned per request, so shutdown
// can drain them.
//
// # The gap it fills (ROADMAP P0-12 / P0-7)
//
// platform/bootstrap owns two kinds of goroutine: the HTTP server, and workers
// registered up front with Background. Neither covers work started DURING a
// request and outliving it — an agent plan loop, a long export, anything that
// answers 202 and keeps going.
//
// dx-agent-runtime-go started those with a bare `go func()`. Nothing owned
// them, so at shutdown the process drained the HTTP server, ran its closers and
// exited while plan loops were still calling tools. The work was not cancelled
// and not waited for; it was abandoned mid-flight, which for an agent means a
// transcript that stops in the middle of a turn with no record of why.
//
// # What ownership means here
//
//   - every task runs on a context derived from the executor's, so Shutdown
//     cancels all of them at once;
//   - Shutdown then WAITS, bounded by its own context, so a caller chooses
//     between draining and a deadline rather than getting whichever the
//     implementation preferred;
//   - once Shutdown begins, Go refuses new work. Without that, a request that
//     slips in during the drain starts a task nobody will wait for — which is
//     the original defect, occurring in the narrow window the fix created;
//   - a panicking task is contained and logged rather than taking the process
//     down, matching the worker edge in bootstrap.
//
// # What it is not
//
// Not a worker pool and not a queue: there is no bound on concurrency and no
// backpressure. Adding either would change when callers block, which is a
// scheduling decision each service should make deliberately. This owns
// lifetimes, nothing else.
package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// ErrShuttingDown is returned by Go once Shutdown has begun.
//
// Callers should treat it as "refuse this request" (503), not as a failure to
// log and continue: the work genuinely will not run.
var ErrShuttingDown = errors.New("executor: shutting down")

// Executor owns per-request goroutines.
type Executor struct {
	log *zap.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	wg       sync.WaitGroup
	draining bool
	inFlight int
}

// New builds an Executor. log may be nil.
func New(log *zap.Logger) *Executor {
	if log == nil {
		log = zap.NewNop()
	}
	// context.Background, deliberately: these tasks outlive the request that
	// started them, so deriving from a request context would cancel them the
	// moment the response is written — which is the bug that makes people reach
	// for `go func()` with a detached context in the first place.
	ctx, cancel := context.WithCancel(context.Background())
	return &Executor{log: log, ctx: ctx, cancel: cancel}
}

// Go runs fn on a goroutine this Executor owns.
//
// fn receives a context cancelled at Shutdown. It must return when that context
// is done — a task that ignores cancellation can only be waited for, and will
// hold Shutdown until its deadline.
//
// name appears in logs and is the only thing identifying a task that panics or
// outstays the drain, so make it say which work it is.
func (e *Executor) Go(name string, fn func(context.Context)) error {
	e.mu.Lock()
	if e.draining {
		e.mu.Unlock()
		return ErrShuttingDown
	}
	// Add to the WaitGroup under the same lock that guards `draining`. Adding
	// after the unlock would race Shutdown's Wait: the task would be
	// unregistered at the moment Wait ran, and Shutdown would report a clean
	// drain while the goroutine was still starting.
	e.wg.Add(1)
	e.inFlight++
	e.mu.Unlock()

	go func() {
		defer func() {
			e.mu.Lock()
			e.inFlight--
			e.mu.Unlock()
			e.wg.Done()
			// Recover INSIDE this goroutine. A panic here would otherwise take
			// the whole process down, turning one malformed tool response into
			// an outage for every session on the replica.
			if r := recover(); r != nil {
				e.log.Error("panic in background task",
					zap.String("task", name), zap.Any("panic", r))
			}
		}()
		fn(e.ctx)
	}()
	return nil
}

// InFlight reports how many tasks are running. For probes and tests.
func (e *Executor) InFlight() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inFlight
}

// Shutdown cancels every running task and waits for them, bounded by ctx.
//
// Register it with bootstrap's Closer, which runs after the HTTP server has
// drained: new requests have stopped by then, so what remains is genuinely
// in-flight work rather than a moving target.
//
// It returns ctx.Err() if the deadline passes with tasks still running, and
// names how many — an operator seeing a shutdown timeout needs to know whether
// one task hung or a hundred were mid-flight.
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if e.draining {
		e.mu.Unlock()
		return nil // idempotent: a closer may run twice on a racing signal
	}
	e.draining = true
	e.mu.Unlock()

	e.cancel()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("executor: %d task(s) still running at shutdown deadline: %w",
			e.InFlight(), ctx.Err())
	}
}
