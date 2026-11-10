package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRunner_FiresJobPeriodically(t *testing.T) {
	r := New(zap.NewNop())
	var runs int64
	r.Register(Job{
		Name:  "periodic-" + t.Name(),
		Every: 5 * time.Millisecond,
		Run: func(context.Context) error {
			atomic.AddInt64(&runs, 1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&runs) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d runs after 2s, want >= 3", atomic.LoadInt64(&runs))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_PanicRecoveryDoesNotStopTheLoop(t *testing.T) {
	r := New(zap.NewNop())
	var calls int64
	r.Register(Job{
		Name:  "panicking-" + t.Name(),
		Every: 5 * time.Millisecond,
		Run: func(context.Context) error {
			n := atomic.AddInt64(&calls, 1)
			if n == 1 {
				panic("boom")
			}
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&calls) < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d calls after 2s, want >= 2 (loop must survive a panic)", atomic.LoadInt64(&calls))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_ErrorDoesNotStopTheLoop(t *testing.T) {
	r := New(zap.NewNop())
	var calls int64
	r.Register(Job{
		Name:  "erroring-" + t.Name(),
		Every: 5 * time.Millisecond,
		Run: func(context.Context) error {
			atomic.AddInt64(&calls, 1)
			return errors.New("transient failure")
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d calls after 2s, want >= 3", atomic.LoadInt64(&calls))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_KickTriggersImmediateRun(t *testing.T) {
	r := New(zap.NewNop())
	name := "kicked-" + t.Name()
	var runs int64
	r.Register(Job{
		Name:  name,
		Every: time.Hour, // effectively never fires on its own during this test
		Run: func(context.Context) error {
			atomic.AddInt64(&runs, 1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()

	// Give the loop time to reach its steady-state select before kicking.
	time.Sleep(20 * time.Millisecond)
	r.Kick(name)

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&runs) < 2 { // 1 initial run at loop start + 1 kicked run
		select {
		case <-deadline:
			t.Fatalf("only %d runs after Kick, want >= 2", atomic.LoadInt64(&runs))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunner_RegisterPanicsOnNonPositiveEvery(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for Job.Every <= 0")
		}
	}()
	New(zap.NewNop()).Register(Job{Name: "bad", Every: 0, Run: func(context.Context) error { return nil }})
}

func TestRunner_RegisterPanicsOnDuplicateName(t *testing.T) {
	r := New(zap.NewNop())
	r.Register(Job{Name: "dup", Every: time.Second, Run: func(context.Context) error { return nil }})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for a duplicate job name")
		}
	}()
	r.Register(Job{Name: "dup", Every: time.Second, Run: func(context.Context) error { return nil }})
}
