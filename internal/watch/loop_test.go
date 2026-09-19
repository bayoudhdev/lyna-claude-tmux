package watch_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// waitFor blocks until cond holds, and fails the test when it does not hold
// in time.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLoopRefreshesOnce reads the state before anything has happened: a loop
// refreshes as it starts, so a view draws what is there rather than waiting
// for the first change.
func TestLoopRefreshesOnce(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	l := watch.Loop{Refresh: func(context.Context) { runs.Add(1) }}
	go l.Run(ctx)
	waitFor(t, "the first refresh", func() bool { return runs.Load() == 1 })
	cancel()
}

// TestLoopRefreshesOnSignal drives the loop the way a hook drives it: the
// signal returns, the debounce settles, and one refresh follows however many
// signals arrived in the meantime.
func TestLoopRefreshesOnSignal(t *testing.T) {
	var runs atomic.Int64
	signals := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	l := watch.Loop{
		Debounce: 5 * time.Millisecond,
		Signal: func(ctx context.Context) error {
			select {
			case <-signals:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Refresh: func(context.Context) { runs.Add(1) },
	}
	go func() {
		l.Run(ctx)
		close(done)
	}()
	waitFor(t, "the first refresh", func() bool { return runs.Load() == 1 })
	for range 3 {
		signals <- struct{}{}
	}
	waitFor(t, "the refresh after the signals", func() bool { return runs.Load() >= 2 })
	cancel()
	<-done
	if n := runs.Load(); n > 4 {
		t.Fatalf("%d refreshes for three signals in one burst", n)
	}
}

// TestLoopRetriesAFailingSignal keeps the loop alive when the source of the
// signals is not there, which is a tmux server that is restarting.
func TestLoopRetriesAFailingSignal(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := watch.Loop{
		Signal:  func(context.Context) error { calls.Add(1); return errors.New("no server") },
		Refresh: func(context.Context) {},
	}
	go l.Run(ctx)
	waitFor(t, "the signal to be tried again", func() bool { return calls.Load() >= 2 })
}

// TestLoopRefreshesWhenIdle catches what no event reports.
func TestLoopRefreshesWhenIdle(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := watch.Loop{Idle: 5 * time.Millisecond, Refresh: func(context.Context) { runs.Add(1) }}
	go l.Run(ctx)
	waitFor(t, "the idle refresh", func() bool { return runs.Load() >= 3 })
}

// TestLoopWatches drives the other source of triggers, and proves the loop
// waits for it: every goroutine it starts has returned when Run returns.
func TestLoopWatches(t *testing.T) {
	var runs, watching atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	l := watch.Loop{
		Debounce: time.Millisecond,
		Watch: func(ctx context.Context, poke func()) {
			watching.Add(1)
			poke()
			<-ctx.Done()
			watching.Add(-1)
		},
		Refresh: func(context.Context) { runs.Add(1) },
	}
	go func() {
		l.Run(ctx)
		close(done)
	}()
	waitFor(t, "the refresh the watch asked for", func() bool { return runs.Load() >= 2 })
	cancel()
	<-done
	if watching.Load() != 0 {
		t.Fatal("Run returned with its watch still running")
	}
}

// TestLoopWithoutRefresh has nothing to do and does nothing.
func TestLoopWithoutRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var l watch.Loop
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a loop with nothing to refresh did not return")
	}
}
