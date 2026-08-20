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

	"github.com/datakaveri/dx-common-go/observability"
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

	// Load overrides how the configuration is read. Nil means config.Load,
	// which is what a service with nothing special to do wants.
	//
	// It exists because five services wrote their own `config.Load` wrapper —
	// promoting legacy environment names onto canonical ones, splitting a
	// comma-separated list, defaulting one secret from another — and NONE of it
	// ran, because bootstrap called config.Load directly and only ever took the
	// service's Options. The wrappers were dead code at boot while every test
	// exercised them, which is the worst possible arrangement: the behaviour
	// was covered, just not in the binary. `dx-files-connect-api-go` could not
	// start in ANY environment as a result (ROADMAP P0-3).
	//
	// The signature is deliberately identical to config.Load's instantiation,
	// so adopting it is `Load: config.Load` and nothing else.
	Load func(config.Options) (*C, error)

	// Deps is evaluated once, after config load, so dependency selection can
	// branch on configuration — events only when cfg.RabbitMQ.Enabled, say.
	Deps func(*C) Deps

	// Wire builds the service's own object graph from ready infrastructure and
	// returns the root handler. Called exactly once, after every dependency has
	// been resolved or degraded. This is the ONLY service-specific code in main.
	Wire func(context.Context, *App[C]) (http.Handler, error)

	// GRPC returns the service's internal gRPC surface, already constructed.
	// Nil means the service serves no gRPC and no socket is opened.
	//
	// It returns an INTERFACE, and the service builds the concrete server with
	// platform/grpc/server.New. That indirection is not ceremony: bootstrap is
	// imported by every service's main, so importing the gRPC server here links
	// gRPC into all 24 modules — which broke dx-notification-go's build
	// outright, exactly as platform/http/middleware → workload → resilience →
	// gRPC did during P0-2 and forced the issuer package to be split out.
	// Inverting it means only a service that actually serves gRPC pays for it.
	//
	// Called after Wire, so the implementation can close over the object graph
	// Wire built, and before serve, so a misconfigured surface fails the boot
	// rather than leaving the service half-listening.
	GRPC func(*App[C]) (Servable, error)
}

// Servable is a long-running listener bootstrap supervises. It exists so the
// gRPC server can join the shutdown sequence without bootstrap knowing what
// gRPC is.
type Servable interface {
	// Serve blocks until ctx is cancelled, then stops gracefully.
	Serve(ctx context.Context) error
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

	grpc     Servable
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

// bootModeEnv selects what the process does with the image it was started from.
//
// One enum rather than a boolean per mode: these are mutually exclusive roles
// for the same binary, and independent booleans leave "both set" undefined the
// moment a third mode appears.
//
// DX_-prefixed on purpose. Every configuration key in this platform is
// unprefixed (AD-010), so the prefix marks this as an operational role rather
// than a setting, and makes it obvious in a manifest that it is not one.
const bootModeEnv = "DX_BOOT_MODE"

// Boot modes. Unset means modeServe, so the normal path needs no variable.
const (
	// modeServe loads config, resolves dependencies and serves. The default.
	modeServe = "serve"

	// modeConfigCheck loads and validates configuration, then exits 0 without
	// dialling a dependency or binding a port.
	//
	// It exists so a RENDERED environment can be tested: helm template, take
	// the resulting environment, run the real image, get a verdict without a
	// database, a broker or an object store in reach. That is the only way to
	// tell "this deployment's config is wrong" apart from "this deployment
	// cannot reach its dependencies", and until ROADMAP P0-3 those were
	// indistinguishable — both showed up as a pod that would not start.
	modeConfigCheck = "config-check"

	// modeMigrateOnly applies migrations and exits 0 without serving.
	//
	// This is the ArgoCD PreSync Job's role (ROADMAP P0-4). The Job runs the
	// SAME image as the service — migrations are embedded in the binary, so a
	// different image would apply a different schema than the code expects —
	// and without this mode it migrates and then starts serving, so the hook
	// never completes and burns its activeDeadlineSeconds instead.
	//
	// A mode rather than a per-service `migrate` subcommand: ten repositories
	// would each need one, and the eleventh service to declare migrations is
	// the one that would forget.
	modeMigrateOnly = "migrate-only"
)

// bootMode reads the mode, defaulting to serve.
//
// An unrecognised value is an ERROR, not a fallback to serving. A typo in an
// operational role must stop the process: a PreSync Job that silently served
// instead of migrating would hang the sync, and a pod that silently migrated
// instead of serving is the defect this item exists to remove.
func bootMode() (string, error) {
	switch m := os.Getenv(bootModeEnv); m {
	case "", modeServe:
		return modeServe, nil
	case modeConfigCheck, modeMigrateOnly:
		return m, nil
	default:
		return "", fmt.Errorf("%s=%q is not a boot mode (want %q, %q or %q)",
			bootModeEnv, m, modeServe, modeConfigCheck, modeMigrateOnly)
	}
}

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
	// 0. What role is this process playing? Read before anything else, so a
	//    typo fails immediately rather than after a config load that may itself
	//    fail for an unrelated reason and mask it.
	mode, err := bootMode()
	if err != nil {
		return err
	}

	// 1. Signals first, so ^C during migration is clean rather than a half-
	//    applied schema.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Config. No logger exists yet, so a failure here goes to stderr.
	load := spec.Load
	if load == nil {
		load = config.Load[C]
	}
	cfg, err := load(spec.Config)
	if err != nil {
		return err
	}
	base := (*cfg).PlatformConfig()

	// 2a. config-check stops here: the configuration decoded and passed
	//     Validate(), which is the whole verdict this mode exists to give.
	//
	//     Deliberately an environment variable and not a flag: the thing under
	//     test is an image plus an environment, and adding a flag would mean
	//     overriding the container's command, which changes what is being
	//     tested. Deliberately not a config key either — it must be readable
	//     before any config exists.
	if mode == modeConfigCheck {
		fmt.Fprintf(os.Stderr, "%s: config ok (%s=%s — not starting)\n", spec.Name, bootModeEnv, mode)
		return nil
	}

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

	// 3a. Observability. After the logger so a failure is logged, and before
	//     migrations/Wire so every driver seam Wire builds reads a live
	//     TracerProvider. Telemetry is availability-safe (OBSERVABILITY.md
	//     principle 3): an init failure is a WARNING, never a boot failure — a
	//     service must serve even when its collector is misconfigured. The
	//     empty-endpoint case builds no SDK at all.
	//
	//     Registered as the FIRST closer so it runs LAST (LIFO) and can flush
	//     spans emitted while the other closers ran. It shares the shutdown
	//     budget with them; a reserved telemetry flush budget is tracked
	//     separately (OBSERVABILITY_PLAN review P1-11).
	otelShutdown, oerr := observability.Init(ctx, observability.Config{
		ServiceName: spec.Name,
		Version:     spec.Version,
		Environment: base.Observability.Environment,
		Endpoint:    base.Observability.OTLPEndpoint,
		SampleRatio: base.Observability.SampleRatio,
		Secure:      base.Observability.Secure,
	})
	if oerr != nil {
		log.Warn("observability init failed; continuing without tracing", zap.Error(oerr))
	}
	app.Closer("observability", otelShutdown)

	var deps Deps
	if spec.Deps != nil {
		deps = spec.Deps(cfg)
	}

	// 4. Migrations, before the pool. DDL takes locks, and no repository may be
	//    handed a pool whose schema is not yet current.
	if deps.Migrations != nil {
		if err := runMigrations(ctx, deps, base, log, mode); err != nil {
			return err
		}
	}

	// 4a. migrate-only stops here — the PreSync Job's whole job is done.
	//
	//     A service with no migrations declared that is started in this mode is
	//     an ERROR, not a no-op success: a Job enabled against a service that
	//     applies no DDL would report a migration that never happened, and the
	//     rollout would proceed believing the schema is current.
	if mode == modeMigrateOnly {
		if deps.Migrations == nil {
			return fmt.Errorf("%s=%s but this service declares no migrations — "+
				"nothing would be applied and the deploy would proceed as if it had",
				bootModeEnv, modeMigrateOnly)
		}
		log.Info("migrations applied; exiting", zap.String("mode", mode))
		return nil
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

	// 6a. The internal gRPC surface, if the service declares one. After Wire so
	//     the registrars can close over the object graph it built, and BEFORE
	//     serve so a misconfigured gRPC surface fails the boot rather than
	//     leaving the service half-listening.
	//
	//     It is handed the SAME workload verifier and the SAME internal-auth
	//     secret the HTTP side uses. Two identity configurations for one process
	//     would mean the P0-2 rollout could land on one transport and not the
	//     other, which is indistinguishable from it having landed.
	if spec.GRPC != nil {
		gsrv, gerr := spec.GRPC(app)
		if gerr != nil {
			return fmt.Errorf("grpc server: %w", gerr)
		}
		app.grpc = gsrv
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
		Addr:        addr(base.Server.Port),
		Handler:     handler,
		ReadTimeout: orDuration(base.Server.ReadTimeout, 15*time.Second),
		// Write and idle accept a NEGATIVE value meaning "no limit". A service
		// that streams needs exactly that, and Go spells no-limit as zero —
		// which is also what an unset config key produces, so without the
		// distinction there is no way to ask for it. An SSE stream would then
		// die at 30s in the HTTP server however the router is configured.
		WriteTimeout:      serverTimeout(base.Server.WriteTimeout, 30*time.Second),
		IdleTimeout:       serverTimeout(base.Server.IdleTimeout, 120*time.Second),
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

	// The gRPC listener shares the errgroup and the same cancellation, so it
	// drains in the SAME ordered shutdown as HTTP rather than racing it — the
	// defect this bootstrap exists to prevent, arriving on a second socket.
	if a.grpc != nil {
		g.Go(func() error { return a.grpc.Serve(gctx) })
	}

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

	// (c) Wait for in-flight work to finish, BOUNDED.
	//
	// Two defects lived in these three lines (ROADMAP P0-7 / review H-05):
	//
	//   - The error was logged and then discarded, and serve() ended in
	//     `return nil`. So a worker whose failure means the service is no
	//     longer doing its job — and a failure to BIND THE PORT, which arrives
	//     through this same errgroup — exited 0. Kubernetes reads that as
	//     Completed, not CrashLoopBackOff: a service that never started looks
	//     exactly like one that shut down cleanly.
	//   - It was the only phase outside the timeout budget. Steps (a) and (d)
	//     both bound their work; a worker that never returns hung shutdown here
	//     until the orchestrator's grace period expired and SIGKILL landed.
	//
	// The error is captured, not returned yet: the closers below must still run
	// so infrastructure is released either way.
	workerErr := a.waitForWorkers(timeout)

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

	if workerErr != nil {
		// Non-nil here means the process must exit non-zero. Run() prints it
		// and calls os.Exit(1).
		return workerErr
	}
	a.Log.Info("stopped")
	return nil
}

// waitForWorkers waits for the errgroup within the shutdown budget.
//
// A worker that ignores its context must not be able to hang the process: the
// budget expires, the failure is named, and shutdown continues. That is worse
// than a clean stop and better than hanging until SIGKILL, which produces no
// diagnosis at all.
func (a *App[C]) waitForWorkers(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- a.group.Wait() }()

	select {
	case err := <-done:
		// context.Canceled is the NORMAL path: stopWorkers cancelled them.
		if err != nil && !errors.Is(err, context.Canceled) {
			a.Log.Error("worker stopped with an error", zap.Error(err))
			return err
		}
		return nil
	case <-time.After(timeout):
		a.Log.Error("workers did not stop within the shutdown budget",
			zap.Duration("timeout", timeout))
		return fmt.Errorf("workers did not stop within %s", timeout)
	}
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

// serverTimeout resolves a server timeout that a service may legitimately want
// switched OFF: zero takes the fallback, a negative value disables the limit.
//
// The asymmetry with orDuration is deliberate. Zero cannot mean "disabled"
// here because zero is what an unset mapstructure key produces, so a service
// that never mentioned write_timeout would silently get an unbounded one. A
// service that genuinely streams says so with -1.
func serverTimeout(d, fallback time.Duration) time.Duration {
	switch {
	case d == 0:
		return fallback
	case d < 0:
		return 0 // http.Server: no timeout
	default:
		return d
	}
}
