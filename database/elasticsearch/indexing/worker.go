package indexing

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"go.uber.org/zap"
)

// Worker periodically runs a job function until its context is cancelled — the
// standing-service form of a Syncer, for keeping an index eventually consistent
// with a source of truth by re-running a Sync (or any indexing task) on an
// interval. It owns panic recovery and jittered scheduling so a service can
// launch it under an errgroup and forget about it.
//
// Scope note: this is a deliberately small, dependency-free loop. For
// sub-minute cadence, cross-replica singleton coordination, or Prometheus
// per-run metrics, register Job as a dx-common-go/scheduler Job instead — the
// scheduler owns those concerns and this Worker stays focused on the simple
// "every N, do the indexing task" case.
type Worker struct {
	// Name labels the worker in logs. Required for useful diagnostics.
	Name string
	// Interval is the delay between run starts. Required (> 0).
	Interval time.Duration
	// Jitter randomizes each delay by up to ±Jitter to avoid thundering herds
	// across replicas. Optional.
	Jitter time.Duration
	// RunOnStart runs Job once immediately before the first interval wait.
	RunOnStart bool
	// Job is the indexing task to run each tick. Required. A returned error is
	// logged and the loop continues — a transient sync failure must not kill
	// the worker.
	Job func(context.Context) error
	// Logger, if set, records run start/finish/failure. Optional.
	Logger *zap.Logger
}

// Start runs the worker until ctx is cancelled, then returns ctx.Err(). It
// blocks, so launch it in its own goroutine or errgroup.
func (w *Worker) Start(ctx context.Context) error {
	if w.Interval <= 0 {
		return fmt.Errorf("elastic indexing.Worker %q: Interval must be > 0", w.Name)
	}
	if w.Job == nil {
		return fmt.Errorf("elastic indexing.Worker %q: Job is required", w.Name)
	}

	if w.RunOnStart {
		w.runOnce(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.nextDelay()):
			w.runOnce(ctx)
		}
	}
}

// nextDelay is Interval adjusted by a symmetric random jitter in [-Jitter, +Jitter].
func (w *Worker) nextDelay() time.Duration {
	if w.Jitter <= 0 {
		return w.Interval
	}
	delta := time.Duration(rand.Int63n(int64(2*w.Jitter+1))) - w.Jitter //nolint:gosec // G404: non-crypto jitter for retry spacing; math/rand is correct
	d := w.Interval + delta
	if d < 0 {
		d = 0
	}
	return d
}

// runOnce executes Job with panic recovery and optional logging.
func (w *Worker) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil && w.Logger != nil {
			w.Logger.Error("elastic indexing worker panicked",
				zap.String("worker", w.Name), zap.Any("panic", r))
		}
	}()
	if w.Logger != nil {
		w.Logger.Debug("elastic indexing worker run", zap.String("worker", w.Name))
	}
	start := time.Now()
	if err := w.Job(ctx); err != nil {
		if w.Logger != nil {
			w.Logger.Warn("elastic indexing worker job failed",
				zap.String("worker", w.Name),
				zap.Duration("duration", time.Since(start)),
				zap.Error(err))
		}
		return
	}
	if w.Logger != nil {
		w.Logger.Debug("elastic indexing worker done",
			zap.String("worker", w.Name), zap.Duration("duration", time.Since(start)))
	}
}
