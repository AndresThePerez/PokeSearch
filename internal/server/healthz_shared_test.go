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

// heldCount is a fake Elasticsearch whose _count answer waits until the test
// releases it, so a test can gather probes around one call in flight. Any
// other path answers the not-seeded-yet 404 at once.
type heldCount struct {
	calls     atomic.Int32
	cancelled atomic.Int32 // calls whose context ended before the release
	entered   chan struct{}
	release   chan struct{}
	answer    func(call int32) (*http.Response, error)
}

func newHeldCount(answer func(call int32) (*http.Response, error)) *heldCount {
	return &heldCount{
		entered: make(chan struct{}, 256),
		release: make(chan struct{}),
		answer:  answer,
	}
}

func (h *heldCount) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/_count") {
		return esResponse(http.StatusNotFound, `{"error":{"type":"index_not_found_exception"}}`), nil
	}
	call := h.calls.Add(1)
	h.entered <- struct{}{}
	select {
	case <-h.release:
	case <-r.Context().Done():
		h.cancelled.Add(1)
		return nil, r.Context().Err()
	}
	return h.answer(call)
}

func countOK(n int) func(int32) (*http.Response, error) {
	return func(int32) (*http.Response, error) {
		return esResponse(http.StatusOK, fmt.Sprintf(`{"count":%d}`, n)), nil
	}
}

// signalWaits makes every /healthz probe report on the returned channel once
// it is waiting on a count call, so a test knows the probes are gathered
// without sleeping.
func signalWaits(s *Server) <-chan struct{} {
	waiting := make(chan struct{}, 256)
	s.healthzCount.onWait = func() { waiting <- struct{}{} }
	return waiting
}

func receiveN(t *testing.T, ch <-chan struct{}, n int, what string) {
	t.Helper()
	for i := range n {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d %s after 10s", i, n, what)
		}
	}
}

// healthzProbe sends one GET /healthz under ctx with its own request id, the
// way an uptime monitor would, asking for gzip it must never get.
func healthzProbe(s *Server, ctx context.Context, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx)
	req.Header.Set("X-Request-Id", id)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

const (
	healthzOKBody    = "{\"docs\":20635,\"status\":\"ok\"}\n"
	healthzErrorBody = "{\"status\":\"error\"}\n"
)

func checkHealthzAnswer(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || rec.Body.String() != body {
		t.Errorf("healthz = %d %q, want %d %q", rec.Code, rec.Body.String(), status, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("healthz was encoded %q; it is never compressed", ce)
	}
}

// Concurrent probes share one _count call: the upstream sees one call while
// they all wait, and every probe answers from it.
func TestHealthzConcurrentProbesShareOneCount(t *testing.T) {
	const probes = 16
	es := newHeldCount(countOK(20635))
	s, _ := newTestServer(t, es)
	waiting := signalWaits(s)

	recs := make([]*httptest.ResponseRecorder, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	receiveN(t, waiting, probes, "probes waiting")
	receiveN(t, es.entered, 1, "count calls upstream")
	if n := es.calls.Load(); n != 1 {
		t.Errorf("%d probes waiting made %d count calls upstream, want 1", probes, n)
	}
	close(es.release)
	wg.Wait()

	if n := es.calls.Load(); n != 1 {
		t.Errorf("%d concurrent probes made %d count calls upstream, want 1", probes, n)
	}
	for i, rec := range recs {
		if rec == nil {
			t.Fatalf("probe %d never answered", i)
		}
		checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
	}
}

// Nothing is kept once the shared call is over: probes one after the other
// each make their own round trip, as a probe always has.
func TestHealthzSequentialProbesEachCallUpstream(t *testing.T) {
	es := newHeldCount(func(call int32) (*http.Response, error) {
		return esResponse(http.StatusOK, fmt.Sprintf(`{"count":%d}`, 20634+call)), nil
	})
	close(es.release)
	s, _ := newTestServer(t, es)

	first := healthzProbe(s, context.Background(), "probe-first")
	second := healthzProbe(s, context.Background(), "probe-second")
	if n := es.calls.Load(); n != 2 {
		t.Errorf("two probes in a row made %d count calls upstream, want 2", n)
	}
	checkHealthzAnswer(t, first, http.StatusOK, healthzOKBody)
	checkHealthzAnswer(t, second, http.StatusOK, "{\"docs\":20636,\"status\":\"ok\"}\n")
}

// A failed shared call fails every probe waiting on it with the frozen body,
// each probe logs its own cause line under its own request id, and the
// failure is not remembered: the next probe asks upstream again.
func TestHealthzSharedFailureFailsEveryWaiterAndIsForgotten(t *testing.T) {
	const probes = 8
	// A 500 is not retried by the client, so the first call is the only one
	// the failing burst makes.
	es := newHeldCount(func(call int32) (*http.Response, error) {
		if call == 1 {
			return esResponse(http.StatusInternalServerError, `{"error":{"type":"cluster_block_exception"}}`), nil
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

	if n := es.calls.Load(); n != 1 {
		t.Errorf("%d concurrent probes made %d count calls upstream, want 1", probes, n)
	}
	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	}
	logged := map[string]int{}
	for _, m := range logLines(t, logBuf) {
		if m["msg"] != "healthz check failed" {
			continue
		}
		id, _ := m["request_id"].(string)
		logged[id]++
		if err, _ := m["err"].(string); !strings.Contains(err, "cluster_block_exception") {
			t.Errorf("cause line %v does not carry the shared call's error", m)
		}
	}
	for i := range probes {
		if id := fmt.Sprintf("probe-%02d", i); logged[id] != 1 {
			t.Errorf("request %s logged %d cause lines, want 1 (all: %v)", id, logged[id], logged)
		}
	}

	next := healthzProbe(s, context.Background(), "probe-next")
	if n := es.calls.Load(); n != 2 {
		t.Errorf("the probe after a failure made the call count %d, want 2: the failure was remembered", n)
	}
	checkHealthzAnswer(t, next, http.StatusOK, healthzOKBody)
}

// The probe that started the shared call goes away: the call goes on under
// its own deadline and the probes waiting on it still get its answer.
func TestHealthzStarterDisconnectDoesNotFailTheOthers(t *testing.T) {
	const probes = 8
	es := newHeldCount(countOK(20635))
	s, _ := newTestServer(t, es)
	waiting := signalWaits(s)

	starterCtx, cancelStarter := context.WithCancel(context.Background())
	defer cancelStarter()
	starterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { starterDone <- healthzProbe(s, starterCtx, "probe-starter") }()
	receiveN(t, es.entered, 1, "count calls upstream")
	receiveN(t, waiting, 1, "probes waiting")

	recs := make([]*httptest.ResponseRecorder, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	receiveN(t, waiting, probes, "probes waiting")

	cancelStarter()
	select {
	case rec := <-starterDone:
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	case <-time.After(10 * time.Second):
		t.Fatal("the probe whose client went away is still waiting")
	}

	close(es.release)
	wg.Wait()
	if n := es.cancelled.Load(); n != 0 {
		t.Errorf("%d count calls were cancelled when the starting probe went away, want 0", n)
	}
	if n := es.calls.Load(); n != 1 {
		t.Errorf("%d probes made %d count calls upstream, want 1", probes+1, n)
	}
	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
	}
}
