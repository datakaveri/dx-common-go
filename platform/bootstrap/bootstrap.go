// Package bootstrap is the platform's composition root.
//
// It owns the boot sequence, dependency resolution, worker supervision and
// shutdown ordering, so a service's main() is a declaration rather than a
// procedure. The block it replaces is 62-of-70 lines identical across 8–16
// services, and it carried two defects that no amount of care at the call site
// could fix:
//
//   - Three services built their logger with zap.NewProduction() BEFORE loading
//     config, so log_level was silently ignored. Here the logger cannot precede
//     config, because bootstrap owns the order of both.
//   - httpserver.Start() installed its own signal handler and blocked, while
//     background workers were cancelled by the SAME signal — so the server and
//     the workers shut down concurrently and raced. Here the server drains
//     first, then workers, then infrastructure.
//
// Layer: L3 (root). Nothing imports it, which is what makes it safe for it to
// import everything else.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/datakaveri/dx-common-go/platform/config"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

// Spec declares what a service needs.
//
// Everything except Name and Wire is optional; a zero Spec boots an HTTP server
// with health and metrics and nothing else.
type Spec[C config.Configurer] struct {
	Name    string
	Version string
	Config  config.Options

	// Deps is evaluated once, after config load, so dependency selection can
	// branch on configuration — events only when cfg.RabbitMQ.Enabled, say.
	Deps func(*C) Deps

	// Wire builds the service's own object graph from ready infrastructure and
	// returns the root handler. Called exactly once, after every dependency has
	// been resolved or degraded. This is the ONLY service-specific code in main.
	Wire func(context.Context, *App[C]) (http.Handler, error)
}

// Deps is the dependency set. A nil field means the service does not use it;
// there is no auto-detection from config shape, because a dependency a service
// did not declare is one nobody decided to depend on.
type Deps struct {
	// Migrations run BEFORE the pool opens, on a throwaway connection closed
	// immediately: DDL takes locks, and no repository may be handed a pool whose
	// schema is not yet current. Always required — a failed migration is a
	// broken deploy, never a degraded one.
	Migrations *MigrationSpec

	Postgres *Dep[dxsql.Config]
}

// Dep is a dependency plus its failure policy.
type Dep[T any] struct {
	Config   T
	Optional bool
}

// Required fails the boot: log, exit 1, let the orchestrator restart. Use it
// whenever the service cannot serve one correct response without the dependency.
func Required[T any](c T) *Dep[T] { return &Dep[T]{Config: c} }

// Degrade logs a warning, leaves the handle NIL, and boots anyway.
//
// Use it only where a real degraded mode exists — a broker outage where writes
// still land in the transactional outbox and drain on a later restart. The nil
// handle is a programming contract: Wire must branch on it. A dependency that
// has no degraded mode should be Required, so the failure is loud.
func Degrade[T any](c T) *Dep[T] { return &Dep[T]{Config: c, Optional: true} }

// MigrationSpec locates a service's embedded migrations.
type MigrationSpec struct {
	FS    fs.FS
	Dir   string
	Table string
}

// Migrations builds a MigrationSpec. Table is the per-service history table,
// e.g. "schema_migrations_acl" — per-service so two services sharing a database
// cannot fight over one history.
func Migrations(fsys fs.FS, dir, table string) *MigrationSpec {
	return &MigrationSpec{FS: fsys, Dir: dir, Table: table}
}

// App is ready infrastructure plus registration surfaces.
//
// Every handle is an interface. No *pgxpool.Pool, *redis.Client or
// *amqp.Connection is reachable from here.
type App[C config.Configurer] struct {
	Cfg     *C
	Log     *zap.Logger
	Name    string
	Version string

	// DB and Tx are nil unless Deps.Postgres was declared.
	DB dxsql.DB
	Tx dxsql.Manager

	Health *health.Registry

	group    *errgroup.Group
	groupCtx context.Context
	closers  []closer
	workers  []worker
}

type closer struct {
	name string
	fn   func(context.Context) error
}

type worker struct {
	name       string
	fn         func(context.Context) error
	supervised bool // restart on failure rather than bringing the app down
}

// Go supervises fn. A non-nil error triggers coordinated shutdown — for a
// worker whose failure means the service is no longer doing its job.
func (a *App[C]) Go(name string, fn func(context.Context) error) {
	a.workers = append(a.workers, worker{name: name, fn: fn})
}

// Background supervises fn but does NOT let its failure stop the app: the error
// is logged and the worker restarted with capped backoff.
//
// Use it for broker consumers, which die on ordinary connection churn. Treating
// that as fatal turns a RabbitMQ restart into a fleet-wide outage.
func (a *App[C]) Background(name string, fn func(context.Context) error) {
	a.workers = append(a.workers, worker{name: name, fn: fn, supervised: true})
}

// Closer registers a shutdown hook. Hooks run LIFO, after the HTTP server has
// drained and after every worker has stopped.
func (a *App[C]) Closer(name string, fn func(context.Context) error) {
	a.closers = append(a.closers, closer{name: name, fn: fn})
}

// Probe adds a readiness check. Every declared dependency is registered
// automatically; call this only for a service-specific check.
func (a *App[C]) Probe(name string, c health.Checker) { a.Health.Add(name, c) }

// Run boots, serves and shuts down. It does not return: exit 0 on a clean
// shutdown, exit 1 on any Required failure.
func Run[C config.Configurer](spec Spec[C]) {
	if err := run(spec); err != nil {
		// The logger may not exist yet — a config failure happens before it.
		fmt.Fprintf(os.Stderr, "%s: %v\n", spec.Name, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// run is Run without the exit, so it is testable.
func run[C config.Configurer](spec Spec[C]) error {
	// 1. Signals first, so ^C during migration is clean rather than a half-
	//    applied schema.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Config. No logger exists yet, so a failure here goes to stderr.
	cfg, err := config.Load[C](spec.Config)
	if err != nil {
		return err
	}
	base := (*cfg).PlatformConfig()

	// 3. Logger. THE FIX: it cannot precede config, because bootstrap owns both.
	log, err := newLogger(base.LogLevel, spec.Name, spec.Version)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck // best-effort flush; stderr may be closed

	app := &App[C]{
		Cfg: cfg, Log: log, Name: spec.Name, Version: spec.Version,
		Health: health.New(),
	}

	var deps Deps
	if spec.Deps != nil {
		deps = spec.Deps(cfg)
	}

	// 4. Migrations, before the pool. DDL takes locks, and no repository may be
	//    handed a pool whose schema is not yet current.
	if deps.Migrations != nil {
		if err := runMigrations(ctx, deps, base, log); err != nil {
			return err
		}
	}

	// 5. Stores.
	if deps.Postgres != nil {
		db, err := dxsql.Open(ctx, deps.Postgres.Config, dxsql.WithLogger(log))
		switch {
		case err != nil && deps.Postgres.Optional:
			log.Warn("postgres unavailable — continuing degraded", zap.Error(err))
		case err != nil:
			return fmt.Errorf("connect postgres: %w", err)
		default:
			app.DB = db
			app.Tx = dxsql.NewManager(db)
			app.Health.Add("postgres", db)
			app.Closer("postgres", func(context.Context) error { db.Close(); return nil })
		}
	}

	// 6. The only service-specific step.
	handler, err := spec.Wire(ctx, app)
	if err != nil {
		return fmt.Errorf("wire: %w", err)
	}

	return app.serve(ctx, handler, base)
}

// serve runs the HTTP server and every worker, then shuts down in order.
func (a *App[C]) serve(ctx context.Context, handler http.Handler, base config.Base) error {
	g, gctx := errgroup.WithContext(ctx)
	a.group, a.groupCtx = g, gctx

	// Workers get a context cancelled independently of the server's, so the
	// server can drain BEFORE they stop pulling new work. WithoutCancel keeps
	// any trace or request values on ctx while dropping its cancellation.
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWorkers()

	for _, w := range a.workers {
		w := w
		g.Go(func() error { return a.runWorker(workerCtx, w) })
	}

	srv := &http.Server{
		Addr:              addr(base.Server.Port),
		Handler:           handler,
		ReadTimeout:       orDuration(base.Server.ReadTimeout, 15*time.Second),
		WriteTimeout:      orDuration(base.Server.WriteTimeout, 30*time.Second),
		IdleTimeout:       orDuration(base.Server.IdleTimeout, 120*time.Second),
		ReadHeaderTimeout: 10 * time.Second,
	}

	g.Go(func() error {
		a.Log.Info("listening",
			zap.String("addr", srv.Addr),
			zap.String("version", a.Version))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	// Wait for a signal or the first worker failure.
	<-gctx.Done()
	a.Log.Info("shutting down")

	timeout := orDuration(base.Server.ShutdownTimeout, 20*time.Second)

	// (a) Drain the server FIRST. The load balancer sees connections close and
	//     stops sending work, and in-flight requests finish — including any
	//     that are mid-write.
	// WithoutCancel, not Background: ctx is already done by now, so deriving
	// from it directly would give an expired context and drain nothing — but
	// its values (trace id, deployment metadata) are still worth carrying into
	// the shutdown spans.
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		a.Log.Warn("server did not drain cleanly", zap.Error(err))
	}

	// (b) Only now stop the workers, so a consumer is not killed while the
	//     server is still accepting requests that depend on it.
	stopWorkers()

	// (c) Wait for in-flight work to finish.
	if err := a.group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		a.Log.Error("worker stopped with an error", zap.Error(err))
	}

	// (d) Close infrastructure, LIFO — the reverse of construction, so nothing
	//     is closed while something that depends on it is still open.
	closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancelClose()
	for i := len(a.closers) - 1; i >= 0; i-- {
		c := a.closers[i]
		if err := c.fn(closeCtx); err != nil {
			a.Log.Warn("closer failed", zap.String("name", c.name), zap.Error(err))
		}
	}

	a.Log.Info("stopped")
	return nil
}

const (
	workerRetryBase = 500 * time.Millisecond
	workerRetryMax  = 30 * time.Second
)

// runWorker runs a worker, restarting a supervised one with capped backoff.
func (a *App[C]) runWorker(ctx context.Context, w worker) error {
	if !w.supervised {
		if err := w.fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("worker %s: %w", w.name, err)
		}
		return nil
	}

	delay := workerRetryBase
	for {
		err := w.fn(ctx)
		if ctx.Err() != nil {
			return nil // shutting down
		}
		if err == nil {
			// A supervised worker that returns nil without cancellation has
			// finished its job; restarting it would be a busy loop.
			return nil
		}
		a.Log.Error("background worker failed — restarting",
			zap.String("name", w.name), zap.Duration("in", delay), zap.Error(err))

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		if delay *= 2; delay > workerRetryMax {
			delay = workerRetryMax
		}
	}
}

func addr(port int) string {
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf(":%d", port)
}

func orDuration(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}
