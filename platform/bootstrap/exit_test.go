package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/config"
)

// ROADMAP P0-7 / review finding H-05: a failed start is reported as a failure
// to the orchestrator, and no shutdown phase can hang indefinitely.
//
// Both defects lived in three lines of serve(): the errgroup's error was logged
// and discarded while the function ended `return nil`, and the wait was the one
// phase with no timeout. Run() maps a non-nil return to os.Exit(1), so
// everything here asserts on serve()/run() returning an error — that IS the
// exit code.

// TestFatalWorkerFailureIsReturned: App.Go documents that a non-nil error
// "triggers coordinated shutdown — for a worker whose failure means the service
// is no longer doing its job." If that exits 0, Kubernetes records Completed
// rather than CrashLoopBackOff, and a service that has stopped working looks
// exactly like one that was asked to stop.
func TestFatalWorkerFailureIsReturned(t *testing.T) {
	app, _ := newApp(t)
	boom := errors.New("consumer died")

	app.Go("worker", func(context.Context) error { return boom })

	base := config.Base{}
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 2 * time.Second

	err := app.serve(context.Background(), http.NotFoundHandler(), base)
	if err == nil {
		t.Fatal("a fatal worker failure returned nil — the process would exit 0 and the orchestrator would not restart it")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap the worker's own error", err)
	}
}

// TestBindFailureIsReturned is the sharpest case: the port is already taken, so
// the service never serves a single request. It reached the SAME swallowed
// path, because ListenAndServe's error travels through the same errgroup.
func TestBindFailureIsReturned(t *testing.T) {
	// Hold the SAME address the server will bind. serve() uses addr(port) =
	// ":<port>" — all interfaces — so holding 127.0.0.1:<port> does NOT
	// conflict on macOS and the server binds happily, which is how the first
	// version of this test hung for ten minutes instead of failing.
	port := freePort(t)
	held, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("hold port %d: %v", port, err)
	}
	defer held.Close()

	app, _ := newApp(t)
	base := config.Base{}
	base.Server.Port = port
	base.Server.ShutdownTimeout = 2 * time.Second

	done := make(chan error, 1)
	go func() { done <- app.serve(context.Background(), http.NotFoundHandler(), base) }()

	select {
	case serr := <-done:
		if serr == nil {
			t.Fatal("a service that could not bind its port returned nil — it would exit 0 having served nothing")
		}
		if !strings.Contains(serr.Error(), "address already in use") {
			t.Errorf("error = %v, want the bind failure named", serr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return on a bind failure")
	}
}

// TestCleanShutdownReturnsNil: the fix must not make ordinary shutdown look
// like a failure. Cancelling the context is the normal path and the workers'
// context.Canceled must not become a non-zero exit.
func TestCleanShutdownReturnsNil(t *testing.T) {
	app, _ := newApp(t)
	app.Go("worker", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })

	base := config.Base{}
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.serve(ctx, http.NotFoundHandler(), base) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a clean shutdown must exit 0, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return")
	}
}

// TestHangingWorkerDoesNotHangShutdown: a worker that ignores its context used
// to hold the process open until the orchestrator's grace period expired and
// SIGKILL landed — which produces no diagnosis at all. The budget must bound
// it and name the failure.
func TestHangingWorkerDoesNotHangShutdown(t *testing.T) {
	app, logs := newApp(t)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app.Go("stuck", func(context.Context) error {
		<-release // deliberately ignores ctx
		return nil
	})

	base := config.Base{}
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.serve(ctx, http.NotFoundHandler(), base) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a worker that never stopped must be reported, not treated as a clean shutdown")
		}
		if !strings.Contains(err.Error(), "did not stop") {
			t.Errorf("error = %v, want it to name the unstopped workers", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown hung on a stuck worker — the budget did not bound it")
	}

	if logs.FilterMessageSnippet("did not stop within the shutdown budget").Len() == 0 {
		t.Error("the timeout was not logged; an operator would have no diagnosis")
	}
}

// TestClosersRunEvenWhenAWorkerFailed: the error is captured and returned at
// the END, so infrastructure is still released. Closing on the happy path only
// would leak connections in exactly the situation where the process is about to
// die anyway and the leak is most visible.
func TestClosersRunEvenWhenAWorkerFailed(t *testing.T) {
	app, _ := newApp(t)
	closed := false

	app.Go("worker", func(context.Context) error { return errors.New("boom") })
	app.Closer("db", func(context.Context) error { closed = true; return nil })

	base := config.Base{}
	base.Server.Port = freePort(t)
	base.Server.ShutdownTimeout = 2 * time.Second

	if err := app.serve(context.Background(), http.NotFoundHandler(), base); err == nil {
		t.Fatal("expected the worker failure to be returned")
	}
	if !closed {
		t.Error("closers did not run when a worker failed — infrastructure would leak on every crash")
	}
}
