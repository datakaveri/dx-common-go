package executor_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/executor"
)

// ROADMAP P0-12 acceptance criterion: "shutdown drains or cancels every
// in-flight plan through the application lifecycle".

func TestShutdownWaitsForRunningTasks(t *testing.T) {
	e := executor.New(nil)

	var done atomic.Bool
	release := make(chan struct{})
	if err := e.Go("slow", func(context.Context) {
		<-release
		done.Store(true)
	}); err != nil {
		t.Fatalf("Go: %v", err)
	}

	shutdownReturned := make(chan error, 1)
	go func() { shutdownReturned <- e.Shutdown(context.Background()) }()

	// Shutdown must NOT return while the task is running. This is the whole
	// property: the previous `go func()` was abandoned mid-flight.
	select {
	case <-shutdownReturned:
		t.Fatal("Shutdown returned while a task was still running — the task is abandoned, " +
			"which is the defect this exists to fix")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-shutdownReturned; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !done.Load() {
		t.Error("the task did not finish before Shutdown returned")
	}
}

// TestShutdownCancelsTasks: draining is not only waiting. A task that watches
// its context must be TOLD to stop, or shutdown waits for work that had no
// reason to end.
func TestShutdownCancelsTasks(t *testing.T) {
	e := executor.New(nil)

	started := make(chan struct{})
	var sawCancel atomic.Bool
	if err := e.Go("watcher", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
	}); err != nil {
		t.Fatalf("Go: %v", err)
	}
	<-started

	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !sawCancel.Load() {
		t.Error("the task's context was never cancelled — Shutdown would hang on any task " +
			"that runs until told to stop")
	}
}

// TestGoRefusesDuringShutdown closes the window the fix would otherwise open: a
// request arriving mid-drain that starts a task nobody waits for.
func TestGoRefusesDuringShutdown(t *testing.T) {
	e := executor.New(nil)
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	err := e.Go("late", func(context.Context) {
		t.Error("a task started after Shutdown — nobody is waiting for it")
	})
	if !errors.Is(err, executor.ErrShuttingDown) {
		t.Fatalf("Go after Shutdown = %v, want ErrShuttingDown", err)
	}
}

// TestShutdownReportsATimeout: a task that ignores cancellation must not make
// shutdown hang forever. The caller's deadline wins and the failure is named.
func TestShutdownReportsATimeout(t *testing.T) {
	e := executor.New(nil)

	release := make(chan struct{})
	defer close(release)
	if err := e.Go("stubborn", func(context.Context) { <-release }); err != nil {
		t.Fatalf("Go: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := e.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown reported success with a task still running")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown error = %v, want it to wrap DeadlineExceeded", err)
	}
	if !contains(err.Error(), "1 task") {
		t.Errorf("Shutdown error %q does not say HOW MANY tasks were still running — an "+
			"operator needs to know whether one hung or a hundred were mid-flight", err)
	}
}

// TestPanicInATaskDoesNotKillTheProcess: one malformed tool response must not
// become an outage for every session on the replica.
func TestPanicInATaskDoesNotKillTheProcess(t *testing.T) {
	e := executor.New(nil)

	if err := e.Go("panicky", func(context.Context) { panic("boom") }); err != nil {
		t.Fatalf("Go: %v", err)
	}
	// Reaching Shutdown at all means the panic was contained; a leaked panic
	// would have taken the test binary down.
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after a panicking task: %v", err)
	}
	if n := e.InFlight(); n != 0 {
		t.Errorf("InFlight = %d after a panicking task, want 0 — a panic that skips the "+
			"bookkeeping makes Shutdown hang forever", n)
	}
}

// TestConcurrentGoAndShutdown is the race the WaitGroup bookkeeping exists for:
// registering a task outside the lock that guards `draining` lets Shutdown's
// Wait run at the instant a task is unregistered, reporting a clean drain while
// a goroutine is still starting. Run with -race.
//
// It also caught a SECOND defect, which is why the final Shutdown below is not
// decoration: Shutdown used to return nil immediately once `draining` was set,
// and called that idempotent. The second caller was therefore told the drain
// had completed while the first was still waiting on it. This test failed
// roughly one run in twenty; -count is what surfaced it.
func TestConcurrentGoAndShutdown(t *testing.T) {
	e := executor.New(nil)

	var (
		wg      sync.WaitGroup
		started atomic.Int64
		ended   atomic.Int64
	)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.Go("racer", func(ctx context.Context) {
				started.Add(1)
				<-ctx.Done()
				ended.Add(1)
			})
		}()
	}
	go func() { _ = e.Shutdown(context.Background()) }()
	wg.Wait()

	// Drain whatever was accepted. This is a SECOND Shutdown while the first
	// may still be running, and it must wait for the real drain rather than
	// return on the "already draining" path.
	_ = e.Shutdown(context.Background())

	if got, want := ended.Load(), started.Load(); got != want {
		t.Errorf("%d tasks started but only %d ended — Shutdown returned while work was "+
			"in flight", want, got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
