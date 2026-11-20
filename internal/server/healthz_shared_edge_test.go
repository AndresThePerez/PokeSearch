package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A panic in the shared call must not leave the probes waiting on it hanging,
// nor stay behind for the next one: they answer the frozen 503 and the next
// probe makes a fresh call.
func TestHealthzSharedCallPanicFailsWaitersAndIsForgotten(t *testing.T) {
	const probes = 4
	es := newHeldCount(func(call int32) (*http.Response, error) {
		if call == 1 {
			panic("count exploded")
		}
		return esResponse(http.StatusOK, `{"count":20635}`), nil
	})
	s, logBuf := newTestServer(t, es)
	waiting := signalWaits(s)

	recs := make([]*httptest.ResponseRecorder, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	receiveN(t, waiting, probes, "probes waiting")
	close(es.release)
	wg.Wait()

	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	}
	if !strings.Contains(logBuf.String(), "count exploded") {
		t.Errorf("the cause lines do not name the panic: %s", logBuf.String())
	}
	checkHealthzAnswer(t, healthzProbe(s, context.Background(), "probe-next"), http.StatusOK, healthzOKBody)
	if n := es.calls.Load(); n != 2 {
		t.Errorf("count calls = %d, want 2 (the one that panicked, then a fresh one)", n)
	}
}

// A probe that is not the starter and goes away stops waiting on its own: the
// call and the other probes go on as if it had never come.
func TestHealthzWaiterThatLeavesDoesNotAffectOthers(t *testing.T) {
	es := newHeldCount(countOK(20635))
	s, _ := newTestServer(t, es)
	waiting := signalWaits(s)

	starter := make(chan *httptest.ResponseRecorder, 1)
	go func() { starter <- healthzProbe(s, context.Background(), "probe-starter") }()
	receiveN(t, es.entered, 1, "count calls upstream")
	receiveN(t, waiting, 1, "probes waiting")

	leaverCtx, leave := context.WithCancel(context.Background())
	defer leave()
	leaver := make(chan *httptest.ResponseRecorder, 1)
	go func() { leaver <- healthzProbe(s, leaverCtx, "probe-leaver") }()
	stayer := make(chan *httptest.ResponseRecorder, 1)
	go func() { stayer <- healthzProbe(s, context.Background(), "probe-stayer") }()
	receiveN(t, waiting, 2, "probes waiting")

	leave()
	select {
	case rec := <-leaver:
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	case <-time.After(10 * time.Second):
		t.Fatal("the probe whose client went away is still waiting")
	}
	close(es.release)
	checkHealthzAnswer(t, <-starter, http.StatusOK, healthzOKBody)
	checkHealthzAnswer(t, <-stayer, http.StatusOK, healthzOKBody)
	if n, c := es.calls.Load(), es.cancelled.Load(); n != 1 || c != 0 {
		t.Errorf("count calls = %d (cancelled %d), want 1 and 0", n, c)
	}
}

// The shared call carries the Elasticsearch budget of its own: a wedged
// cluster fails the waiting probes within it even though none of their
// requests has a deadline.
func TestHealthzSharedCallHasItsOwnDeadline(t *testing.T) {
	const budget = 100 * time.Millisecond
	var noDeadline atomic.Int32
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if _, ok := r.Context().Deadline(); !ok {
			noDeadline.Add(1)
		}
		<-r.Context().Done() // wedged: never answers
		return nil, r.Context().Err()
	})
	s, _ := newTestServer(t, rt)
	s.esTimeout = budget

	start := time.Now()
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, 4)
	for i := range recs {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("probes against a wedged cluster took %s against a %s budget", elapsed, budget)
	}
	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	}
	if n := noDeadline.Load(); n != 0 {
		t.Errorf("%d count calls ran with no deadline", n)
	}
}

// Sharing is for /healthz alone: concurrent /api/meta requests each make
// their own count call, as they always have.
func TestMetaCountsAreNotShared(t *testing.T) {
	const requests = 4
	es := newHeldCount(countOK(20635))
	s, _ := newTestServer(t, es)

	var wg sync.WaitGroup
	codes := make([]int, requests)
	for i := range requests {
		wg.Go(func() { codes[i] = get(t, s, "/api/meta").Code })
	}
	receiveN(t, es.entered, requests, "count calls upstream")
	close(es.release)
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("meta request %d = %d, want 200", i, code)
		}
	}
	if n := es.calls.Load(); n != requests {
		t.Errorf("%d concurrent meta requests made %d count calls, want %d", requests, n, requests)
	}
}
