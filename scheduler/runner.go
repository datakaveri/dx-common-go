package scheduler

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Option configures a Runner at construction time. None are defined yet;
// the parameter exists so New's signature doesn't need to change when one is.
type Option func(*Runner)

// JobOption configures one Job's registration.
type JobOption func(*registeredJob)

// WithSingleton guards j with a non-blocking PostgreSQL advisory lock keyed
// by Job.Name: when several replicas share pool and register the same job,
// only the replica that wins the try-lock on a given tick actually runs —
// the rest skip that tick (counted in scheduler_skipped_singleton_total)
// rather than piling up behind a blocking lock.
func WithSingleton(pool *pgxpool.Pool) JobOption {
	return func(rj *registeredJob) { rj.singletonPool = pool }
}

type registeredJob struct {
	job           Job
	singletonPool *pgxpool.Pool
	kick          chan struct{}
}

// Runner runs registered Jobs on their own interval until Start's context is
// cancelled. Register every job before calling Start; Register after Start
// has begun is not supported.
type Runner struct {
	logger *zap.Logger
	jobs   []*registeredJob
}

// New constructs a Runner. logger must not be nil.
func New(logger *zap.Logger, opts ...Option) *Runner {
	r := &Runner{logger: logger}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds j to the set of jobs Start will run. Panics on a
// non-positive Every or a duplicate Name — both are programmer errors,
// caught at startup rather than surfacing as a silently-broken scheduler.
func (r *Runner) Register(j Job, opts ...JobOption) {
	if j.Every <= 0 {
		panic("scheduler: Job.Every must be positive: " + j.Name)
	}
	for _, existing := range r.jobs {
		if existing.job.Name == j.Name {
			panic("scheduler: duplicate job name: " + j.Name)
		}
	}
	rj := &registeredJob{job: j, kick: make(chan struct{}, 1)}
	for _, opt := range opts {
		opt(rj)
	}
	r.jobs = append(r.jobs, rj)
}

// Kick requests an immediate out-of-cycle run of the named job (coalesces
// with any already-pending kick for that job). No-op if name isn't
// registered.
func (r *Runner) Kick(name string) {
	for _, rj := range r.jobs {
		if rj.job.Name == name {
			select {
			case rj.kick <- struct{}{}:
			default:
			}
			return
		}
	}
}

// Start runs every registered job on its own goroutine until ctx is
// cancelled, then waits for all in-flight runs to finish before returning.
func (r *Runner) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, rj := range r.jobs {
		wg.Add(1)
		go func(rj *registeredJob) {
			defer wg.Done()
			r.runLoop(ctx, rj)
		}(rj)
	}
	wg.Wait()
	return nil
}

func (r *Runner) runLoop(ctx context.Context, rj *registeredJob) {
	if rj.job.Jitter > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(rand.Int63n(int64(rj.job.Jitter)))):
		}
	}

	ticker := time.NewTicker(rj.job.Every)
	defer ticker.Stop()

	for {
		r.runOnce(ctx, rj)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-rj.kick:
		}
	}
}

func (r *Runner) runOnce(ctx context.Context, rj *registeredJob) {
	runCtx := ctx
	if rj.job.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, rj.job.Timeout)
		defer cancel()
	}

	if rj.singletonPool != nil {
		unlock, ok, err := tryAdvisoryLock(runCtx, rj.singletonPool, lockKey(rj.job.Name))
		if err != nil {
			r.logger.Error("scheduler: advisory lock check failed", zap.String("job", rj.job.Name), zap.Error(err))
			return
		}
		if !ok {
			skippedSingletonTotal.WithLabelValues(rj.job.Name).Inc()
			return
		}
		defer unlock()
	}

	// One span per run (new root — a tick has no inbound trace), so a job's work
	// and any dependency spans it creates are a navigable trace, and a failure
	// is a failed trace. Started AFTER the singleton lock so a skipped tick
	// produces no span.
	spanCtx, span := startJobSpan(runCtx, rj.job.Name)
	start := time.Now()
	err := runProtected(spanCtx, rj.job)
	duration := time.Since(start)
	endJobSpan(span, err)

	runsTotal.WithLabelValues(rj.job.Name).Inc()
	durationSeconds.WithLabelValues(rj.job.Name).Observe(duration.Seconds())
	if err != nil {
		failuresTotal.WithLabelValues(rj.job.Name).Inc()
		r.logger.Error("scheduler: job failed",
			zap.String("job", rj.job.Name), zap.Duration("duration", duration), zap.Error(err))
	}
}

// runProtected calls job.Run, converting a panic into an error so one
// failing job can never take down the process or another job's loop.
func runProtected(ctx context.Context, job Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("scheduler: job %q panicked: %v", job.Name, p)
		}
	}()
	return job.Run(ctx)
}
