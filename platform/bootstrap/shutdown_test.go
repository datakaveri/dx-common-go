package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"

	"github.com/datakaveri/dx-common-go/platform/config"
)

// serveAsync starts app.serve on a free port and returns the base it used, a
// cancel func that plays the role of a stop signal, and the serve result.
func serveAsync(t *testing.T, app *App[testConfig], base config.Base) (config.Base, context.CancelFunc, <-chan error) {
	t.Helper()
	base.Server.Port = freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	done := make(chan error, 1)
	go func() { done <- app.serve(ctx, handler, base) }()
	waitListening(t, base.Server.Port)
	return base, cancel, done
}

func waitListening(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port)); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server on port %d never started listening", port)
}

func waitResult(t *testing.T, done <-chan error, within time.Duration) (error, time.Duration) {
	t.Helper()
	start := time.Now()
	select {
	case err := <-done:
		return err, time.Since(start)
	case <-time.After(within):
		t.Fatalf("serve did not return within %s", within)
		return nil, 0
	}
}

// TestDrainDelayKeepsServingWhileNotReady: after a stop signal the replica
// reports not-ready but keeps answering requests for drain_delay, so traffic
// already routed to it is not dropped; then the listener closes.
func TestDrainDelayKeepsServingWhileNotReady(t *testing.T) {
	app, logs := newApp(t)
	var base config.Base
	base.Server.ShutdownTimeout = 2 * time.Second
	base.Server.DrainDelay = 600 * time.Millisecond
	base, stop, done := serveAsync(t, app, base)

	if app.Health.Draining() {
		t.Fatal("draining before any stop signal")
	}
	stop()
	time.Sleep(150 * time.Millisecond) // inside the drain delay

	if !app.Health.Draining() {
		t.Error("readiness still advertised after the stop signal")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", base.Server.Port))
	if err != nil {
		t.Fatalf("request during drain delay failed: %v", err)
	}
	resp.Body.Close()

	if err, _ := waitResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", base.Server.Port)); err == nil {
		t.Error("listener still open after shutdown")
	}
	if logs.FilterMessage("reporting not-ready before closing the listener").Len() != 1 {
		t.Error("drain delay was not logged")
	}
}

// TestNoDrainDelayWhenAComponentFails: a failing worker means this replica
// should exit promptly; lingering for drain_delay would only delay restart.
func TestNoDrainDelayWhenAComponentFails(t *testing.T) {
	app, logs := newApp(t)
	app.Go("broken", func(context.Context) error { return errors.New("boom") })

	var base config.Base
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 2 * time.Second
	base.Server.DrainDelay = 5 * time.Second

	done := make(chan error, 1)
	go func() {
		done <- app.serve(context.Background(), http.NotFoundHandler(), base)
	}()
	err, took := waitResult(t, done, 4*time.Second)
	if err == nil {
		t.Fatal("serve returned nil after a worker failed")
	}
	if took > 3*time.Second {
		t.Errorf("shutdown took %s; drain delay must not apply to a failure", took)
	}
	if !app.Health.Draining() {
		t.Error("readiness still advertised while shutting down")
	}
	if logs.FilterMessage("reporting not-ready before closing the listener").Len() != 0 {
		t.Error("drain delay applied to a failure-triggered shutdown")
	}
}

// TestShutdownTimeoutIsATotalBudget: a worker that ignores cancellation and a
// closer that blocks must not stretch shutdown beyond shutdown_timeout. With
// the old per-phase limits this test took three times the budget.
func TestShutdownTimeoutIsATotalBudget(t *testing.T) {
	app, _ := newApp(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app.Go("stuck", func(context.Context) error { <-release; return nil })

	var closerCalled atomic.Bool
	var closerHadTime atomic.Bool
	app.Closer("slow", func(ctx context.Context) error {
		closerCalled.Store(true)
		deadline, ok := ctx.Deadline()
		closerHadTime.Store(ok && time.Until(deadline) > 0)
		<-ctx.Done()
		return ctx.Err()
	})

	var base config.Base
	base.Server.ShutdownTimeout = 800 * time.Millisecond
	_, stop, done := serveAsync(t, app, base)
	stop()

	err, took := waitResult(t, done, 5*time.Second)
	if err == nil {
		t.Error("serve returned nil although a worker outlived the budget")
	}
	if took > 1300*time.Millisecond {
		t.Errorf("shutdown took %s, budget was 800ms", took)
	}
	if !closerCalled.Load() || !closerHadTime.Load() {
		t.Error("closer was skipped or given an already-expired context")
	}
}

// TestShutdownIsObservable: the start-up summary names the close order, and
// every closer and phase is logged, so the order can be read from the logs.
func TestShutdownIsObservable(t *testing.T) {
	app, logs := newApp(t)
	app.Closer("postgres", func(context.Context) error { return nil })
	app.Closer("rabbitmq", func(context.Context) error { return nil })
	app.Closer("auditing", func(context.Context) error { return errors.New("flush failed") })

	var base config.Base
	base.Server.ShutdownTimeout = 2 * time.Second
	_, stop, done := serveAsync(t, app, base)
	stop()
	if err, _ := waitResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("serve: %v", err)
	}

	started := logs.FilterMessage("started").All()
	if len(started) != 1 {
		t.Fatalf("started summary lines = %d, want 1", len(started))
	}
	order := fieldStrings(started[0], "close_order")
	if fmt.Sprint(order) != "[auditing rabbitmq postgres]" {
		t.Errorf("close_order = %v", order)
	}

	var closed []string
	for _, e := range logs.FilterMessage("closed").All() {
		closed = append(closed, e.ContextMap()["name"].(string))
	}
	if fmt.Sprint(closed) != "[rabbitmq postgres]" {
		t.Errorf("closed = %v, want rabbitmq then postgres (auditing failed)", closed)
	}
	if logs.FilterMessage("closer failed").Len() != 1 {
		t.Error("failing closer not logged")
	}
	for _, msg := range []string{"http drained", "workers stopped", "stopped"} {
		if logs.FilterMessage(msg).Len() != 1 {
			t.Errorf("missing %q log line", msg)
		}
	}
}

// fieldStrings reads a zap.Strings field from a logged entry.
func fieldStrings(e observer.LoggedEntry, key string) []string {
	if v, ok := e.ContextMap()[key].([]interface{}); ok {
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, fmt.Sprint(s))
		}
		return out
	}
	return nil
}

// TestSupervisedBackoffResetsAfterAHealthyRun: one failure after a long healthy
// run starts again from the base delay instead of the accumulated one.
func TestSupervisedBackoffResetsAfterAHealthyRun(t *testing.T) {
	setWorkerTiming(t, 10*time.Millisecond, time.Second, 80*time.Millisecond)
	app, logs := newApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	go func() {
		_ = app.runWorker(ctx, worker{name: "consumer", supervised: true, fn: func(c context.Context) error {
			switch n := calls.Add(1); {
			case n <= 3:
				return errors.New("quick failure") // delays 10, 20, 40ms
			case n == 4:
				time.Sleep(120 * time.Millisecond) // a healthy run
				return errors.New("failure after a healthy run")
			default:
				<-c.Done()
				return c.Err()
			}
		}})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	entries := logs.FilterMessage("background worker failed — restarting").All()
	if len(entries) != 4 {
		t.Fatalf("restart log lines = %d, want 4", len(entries))
	}
	got := entries[3].ContextMap()["in"]
	if got != 10*time.Millisecond {
		t.Errorf("delay after a healthy run = %v, want the base 10ms", got)
	}
	if prev := entries[2].ContextMap()["in"]; prev != 40*time.Millisecond {
		t.Errorf("third quick-failure delay = %v, want 40ms (doubling)", prev)
	}
}

// TestSupervisedCleanExitIsLogged: a supervised worker that returns nil is not
// restarted, and that is visible rather than silent.
func TestSupervisedCleanExitIsLogged(t *testing.T) {
	app, logs := newApp(t)
	err := app.runWorker(context.Background(), worker{name: "loop", supervised: true,
		fn: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatalf("runWorker: %v", err)
	}
	if logs.FilterMessage("background worker returned without an error — not restarting").Len() != 1 {
		t.Error("clean exit of a supervised worker was not logged")
	}
}

func setWorkerTiming(t *testing.T, base, max, healthy time.Duration) {
	t.Helper()
	oldBase, oldMax, oldHealthy := workerRetryBase, workerRetryMax, workerHealthyRun
	workerRetryBase, workerRetryMax, workerHealthyRun = base, max, healthy
	t.Cleanup(func() { workerRetryBase, workerRetryMax, workerHealthyRun = oldBase, oldMax, oldHealthy })
}
