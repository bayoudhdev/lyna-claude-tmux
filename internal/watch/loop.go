package watch

import (
	"context"
	"sync"
	"time"
)

// Loop is the refresh loop the live views run on: one refresh at the start,
// one after every trigger the debounce settles, and one whenever nothing has
// refreshed for the idle period. It starts no timer while nothing happens, so
// a view of a workspace that is doing nothing costs nothing.
//
// Every goroutine it starts has exited when Run returns, and Refresh is only
// ever called from Run's own goroutine.
type Loop struct {
	// Signal blocks until an external change notification arrives (nil
	// return) or fails, and is called again as soon as it returns. A failing
	// signal source is retried with a backoff, so a tmux server that is
	// restarting is waited out rather than spun on. Nil disables it.
	Signal func(ctx context.Context) error
	// Watch is any other source of triggers, for example file events. It is
	// started once and must return when ctx is done. Nil disables it.
	Watch func(ctx context.Context, poke func())
	// Refresh reads what the view shows. It is called once before anything
	// else, then for every settled trigger.
	Refresh func(ctx context.Context)
	// Debounce is the quiet period after a trigger before refreshing; 0 means
	// DefaultDebounce.
	Debounce time.Duration
	// Idle refreshes when nothing else has for that long, catching changes no
	// event reports. Every refresh puts it off again; 0 disables it.
	Idle time.Duration
}

// Run refreshes until ctx is done.
func (l *Loop) Run(ctx context.Context) {
	if l.Refresh == nil {
		return
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	if l.Watch != nil {
		wg.Go(func() { l.Watch(ctx, poke) })
	}
	if l.Signal != nil {
		wg.Go(func() { l.signalLoop(ctx, poke) })
	}

	debounce := l.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	// The fallback is a timer, not a ticker: it is reset by every refresh, so
	// it only fires when nothing else has.
	var idle *time.Timer
	var tick <-chan time.Time
	if l.Idle > 0 {
		idle = time.NewTimer(l.Idle)
		defer idle.Stop()
		tick = idle.C
	}
	refresh := func() {
		if idle != nil {
			// Go 1.23 timer channels are unbuffered, so a reset drops a tick
			// the loop has not received yet instead of firing at once.
			idle.Reset(l.Idle)
		}
		l.Refresh(ctx)
	}

	refresh()
	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			if timer == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			}
		case <-fire:
			timer, fire = nil, nil
			refresh()
		case <-tick:
			refresh()
		}
	}
}

func (l *Loop) signalLoop(ctx context.Context, poke func()) {
	backoff := signalBackoffMin
	for {
		err := l.Signal(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = signalBackoffMin
			poke()
			continue
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, signalBackoffMax)
	}
}
