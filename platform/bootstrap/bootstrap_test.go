package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/datakaveri/dx-common-go/platform/config"
	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

// These are white-box tests: serve() and runWorker() are the parts carrying the
// ordering guarantees, and exercising them directly is what makes the shutdown
// sequence assertable without binding a port and sending real traffic.

type testConfig struct {
	config.Base `mapstructure:",squash"`
}

func newApp(t *testing.T) (*App[testConfig], *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return &App[testConfig]{
		Name: "test-svc", Log: zap.New(core), Health: health.New(),
	}, logs
}

// TestLoggerHonoursConfiguredLevel is the structural fix for a live defect.
//
// dx-audit-go, dx-subscription-go and dx-gateway-go call zap.NewProduction()
// BEFORE config.Load, so their log_level setting is silently ignored. That is
// not a mistake anyone avoids by being careful — it is a consequence of the
// boot sequence being hand-written 18 times. bootstrap owns the order, so the
// logger cannot precede config.
func TestLoggerHonoursConfiguredLevel(t *testing.T) {
	debug, err := newLogger("debug", "svc", "v1")
	if err != nil {
		t.Fatalf("build debug logger: %v", err)
	}
	if !debug.Core().Enabled(zapcore.DebugLevel) {
		t.Error("a debug logger must emit debug lines — this is the bug the three services have")
	}

	warn, err := newLogger("warn", "svc", "v1")
	if err != nil {
		t.Fatalf("build warn logger: %v", err)
	}
	if warn.Core().Enabled(zapcore.InfoLevel) {
		t.Error("a warn logger must suppress info")
	}
}

// TestLoggerRejectsAnUnparseableLevel: the operator asked for something
// specific, so silently running at info hides a config error.
func TestLoggerRejectsAnUnparseableLevel(t *testing.T) {
	if _, err := newLogger("verbose-ish", "svc", ""); err == nil {
		t.Error("an unparseable level must fail the boot, not fall back to info")
	}
}

// TestShutdownOrder is the other structural fix.
//
// httpserver.Start() installed its own signal handler and blocked, while
// background workers were cancelled by the SAME signal — so the server and the
// workers shut down concurrently and raced. The server must drain FIRST, so a
// consumer is never killed while the server still accepts requests depending on
// it, and closers must run LAST, after in-flight work has finished.
func TestShutdownOrder(t *testing.T) {
	app, _ := newApp(t)

	var mu sync.Mutex
	var events []string
	record := func(s string) { mu.Lock(); events = append(events, s); mu.Unlock() }

	workerStopped := make(chan struct{})
	app.Go("worker", func(ctx context.Context) error {
		<-ctx.Done()
		record("worker-stopped")
		close(workerStopped)
		return nil
	})
	app.Closer("db", func(context.Context) error { record("closer-db"); return nil })

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("request-served")
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(context.Background())
	base := config.Base{}
	// A REAL free port. `Port = 0` does not mean ephemeral here — addr() maps 0
	// to the platform default 8080 — and this line used to claim it did. The
	// test passed anyway only because serve() swallowed the resulting bind
	// error and returned nil (ROADMAP P0-7), so it was asserting shutdown
	// ordering on a server that had never started.
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 5 * time.Second

	done := make(chan error, 1)
	go func() { done <- app.serve(ctx, handler, base) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return")
	}

	<-workerStopped
	mu.Lock()
	defer mu.Unlock()

	// The closer must come after the worker stopped: infrastructure is torn
	// down only once nothing is still using it.
	wi, ci := indexOf(events, "worker-stopped"), indexOf(events, "closer-db")
	if wi < 0 || ci < 0 {
		t.Fatalf("missing events: %v", events)
	}
	if ci < wi {
		t.Errorf("closer ran before the worker stopped: %v", events)
	}
}

// TestClosersRunLIFO: the reverse of construction, so nothing is closed while
// something that depends on it is still open.
func TestClosersRunLIFO(t *testing.T) {
	app, _ := newApp(t)

	var mu sync.Mutex
	var order []string
	for _, name := range []string{"first", "second", "third"} {
		name := name
		app.Closer(name, func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	base := config.Base{}
	base.Server.ShutdownTimeout = 2 * time.Second

	done := make(chan error, 1)
	go func() { done <- app.serve(ctx, http.NotFoundHandler(), base) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	want := []string{"third", "second", "first"}
	for i, w := range want {
		if i >= len(order) || order[i] != w {
			t.Fatalf("closer order = %v, want %v", order, want)
		}
	}
}

// TestBackgroundWorkerRestarts: a broker consumer dies on ordinary connection
// churn. Treating that as fatal would turn a RabbitMQ restart into a
// fleet-wide outage.
func TestBackgroundWorkerRestarts(t *testing.T) {
	app, logs := newApp(t)

	var attempts atomic.Int32
	settled := make(chan struct{})
	var once sync.Once

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = app.runWorker(ctx, worker{
			name:       "consumer",
			supervised: true,
			fn: func(context.Context) error {
				if attempts.Add(1) >= 3 {
					once.Do(func() { close(settled) })
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("broker connection lost")
			},
		})
	}()

	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatalf("worker restarted only %d times", attempts.Load())
	}
	cancel()

	if got := logs.FilterMessage("background worker failed — restarting").Len(); got < 2 {
		t.Errorf("restart log lines = %d, want at least 2", got)
	}
}

// TestUnsupervisedWorkerFailureStopsTheApp: Go() is for a worker whose failure
// means the service is no longer doing its job.
func TestUnsupervisedWorkerFailureStopsTheApp(t *testing.T) {
	app, _ := newApp(t)
	boom := errors.New("worker is broken")

	app.Go("critical", func(context.Context) error { return boom })

	base := config.Base{}
	base.Server.ShutdownTimeout = 2 * time.Second

	done := make(chan error, 1)
	go func() { done <- app.serve(context.Background(), http.NotFoundHandler(), base) }()

	select {
	case <-done:
		// serve returns once the group context is cancelled by the failure.
	case <-time.After(10 * time.Second):
		t.Fatal("an unsupervised worker failure did not stop the app")
	}
}

// TestBackgroundWorkerStopsOnShutdown: a supervised worker must not be
// restarted while the app is shutting down.
func TestBackgroundWorkerStopsOnShutdown(t *testing.T) {
	app, _ := newApp(t)
	ctx, cancel := context.WithCancel(context.Background())

	var attempts atomic.Int32
	done := make(chan struct{})
	go func() {
		_ = app.runWorker(ctx, worker{
			name: "consumer", supervised: true,
			fn: func(c context.Context) error {
				attempts.Add(1)
				<-c.Done()
				return c.Err()
			},
		})
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervised worker did not stop on cancellation")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("worker ran %d times; cancellation must not trigger a restart", n)
	}
}

// TestDepPolicy pins the Required/Degrade contract at the type level.
func TestDepPolicy(t *testing.T) {
	if Required("x").Optional {
		t.Error("Required must not be optional")
	}
	if !Degrade("x").Optional {
		t.Error("Degrade must be optional")
	}
}

// TestMigrationsWithoutADatabaseIsAWiringError: naming the mistake beats
// silently skipping the schema.
func TestMigrationsWithoutADatabaseIsAWiringError(t *testing.T) {
	deps := Deps{Migrations: Migrations(nil, "migrations", "schema_migrations_test")}
	err := runMigrations(context.Background(), deps, config.Base{}, zap.NewNop(), modeServe)
	if err == nil {
		t.Error("declaring migrations with no Postgres dependency must be an error")
	}
}

func TestNoMigrationsIsNotAnError(t *testing.T) {
	if err := runMigrations(context.Background(), Deps{}, config.Base{}, zap.NewNop(), modeServe); err != nil {
		t.Errorf("a service with no migrations must boot: %v", err)
	}
}

func TestProbeRegistersOnTheRegistry(t *testing.T) {
	app, _ := newApp(t)
	app.Probe("custom", health.CheckerFunc(func(context.Context) error { return nil }))

	rep := app.Health.Probe(context.Background())
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "custom" {
		t.Errorf("report = %+v", rep)
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// freePort returns a port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release probe listener: %v", err)
	}
	return port
}
