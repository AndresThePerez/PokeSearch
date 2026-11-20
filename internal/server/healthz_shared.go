package server

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// sharedCount lets concurrent /healthz probes share one _count call. Probes
// are exempt from the cap on requests in flight, so without it a burst of
// probes would be a burst of calls to Elasticsearch that nothing bounds. It is
// not a cache: a call is shared only while it is in flight, and the first
// probe after it has returned makes a fresh one, so a probe that arrives alone
// is still a real round trip.
//
// The call runs on its own goroutine under its own deadline, detached from the
// request that started it: if that client goes away, the probes waiting on the
// call still get its answer. A waiting probe whose own client goes away stops
// waiting and leaves the call to the others. The goroutine always ends, at the
// latest when the deadline does.
type sharedCount struct {
	mu   sync.Mutex
	call *countCall // the call in flight, nil when there is none

	// onWait, when set, runs each time a probe starts waiting on a call. Tests
	// use it to know the probes are gathered without sleeping.
	onWait func()
}

type countCall struct {
	done chan struct{} // closed once docs and err are set
	docs int
	err  error
}

// do answers from the call in flight, or starts one with count under a
// context of its own bounded by timeout. ctx is the probe's request context:
// it ends the wait, never the call.
func (g *sharedCount) do(ctx context.Context, timeout time.Duration, count func(context.Context) (int, error)) (int, error) {
	g.mu.Lock()
	c := g.call
	if c == nil {
		c = &countCall{done: make(chan struct{})}
		g.call = c
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		go g.run(callCtx, cancel, c, count)
	}
	g.mu.Unlock()

	if g.onWait != nil {
		g.onWait()
	}
	select {
	case <-c.done:
		return c.docs, c.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (g *sharedCount) run(ctx context.Context, cancel context.CancelFunc, c *countCall, count func(context.Context) (int, error)) {
	defer func() {
		cancel()
		// A panic becomes the call's error rather than taking the process
		// down from a goroutine no handler recovers.
		if p := recover(); p != nil {
			c.docs, c.err = 0, fmt.Errorf("count call panicked: %v", p)
		}
		// Forget the call before waking its probes, so that a probe sent
		// after an answer always makes a fresh call.
		g.mu.Lock()
		g.call = nil
		g.mu.Unlock()
		close(c.done)
	}()
	c.docs, c.err = count(ctx)
}
