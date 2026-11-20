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

// steppedCount is a fake Elasticsearch whose _count calls each wait for their
// own release, so a test can hold one call, let it go, and hold the next.
// Call n answers what answer(n) returns once release(n) is closed.
type steppedCount struct {
	calls     atomic.Int32
	cancelled atomic.Int32
	entered   chan int32
	mu        sync.Mutex
	releases  map[int32]chan struct{}
	answer    func(call int32) (*http.Response, error)
}

func newSteppedCount(answer func(call int32) (*http.Response, error)) *steppedCount {
	return &steppedCount{
		entered:  make(chan int32, 256),
		releases: map[int32]chan struct{}{},
		answer:   answer,
	}
}

func (f *steppedCount) gate(call int32) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.releases[call]
	if !ok {
		ch = make(chan struct{})
		f.releases[call] = ch
	}
	return ch
}

func (f *steppedCount) release(call int32) { close(f.gate(call)) }

func (f *steppedCount) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/_count") {
		return esResponse(http.StatusNotFound, `{"error":{"type":"index_not_found_exception"}}`), nil
	}
	call := f.calls.Add(1)
	f.entered <- call
	select {
	case <-f.gate(call):
	case <-r.Context().Done():
		f.cancelled.Add(1)
		return nil, r.Context().Err()
	}
	return f.answer(call)
}

func receiveCall(t *testing.T, ch <-chan int32, want int32) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("count call %d reached upstream, want call %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("count call %d never reached upstream", want)
	}
}

// Probes arriving in waves share one call per wave and never more: each wave
// answers from the call it waited on, never from a call that had already
// answered an earlier wave, and the upstream sees one call per wave.
func TestHealthzWavesEachMakeOneFreshCall(t *testing.T) {
	const waves, perWave = 6, 12
	es := newSteppedCount(func(call int32) (*http.Response, error) {
		return esResponse(http.StatusOK, fmt.Sprintf(`{"count":%d}`, 1000+call)), nil
	})
	s, _ := newTestServer(t, es)
	waiting := signalWaits(s)

	for w := int32(1); w <= waves; w++ {
		recs := make([]*httptest.ResponseRecorder, perWave)
		var wg sync.WaitGroup
		for i := range perWave {
			wg.Go(func() {
				recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("wave-%d-probe-%02d", w, i))
			})
		}
		receiveN(t, waiting, perWave, "probes waiting")
		receiveCall(t, es.entered, w)
		if n := es.calls.Load(); n != w {
			t.Fatalf("wave %d: %d count calls upstream, want %d", w, n, w)
		}
		es.release(w)
		wg.Wait()
		want := fmt.Sprintf("{\"docs\":%d,\"status\":\"ok\"}\n", 1000+w)
		for _, rec := range recs {
			checkHealthzAnswer(t, rec, http.StatusOK, want)
		}
	}
	if n := es.calls.Load(); n != waves {
		t.Errorf("%d waves made %d count calls, want %d", waves, n, waves)
	}
}

// A failing call followed at once by a succeeding one: the probes that waited
// on the failure all get the frozen 503, a probe sent as the failure is
// answered gets the answer of whichever call it waited on, the next burst
// shares one fresh call, and the upstream sees exactly two calls.
func TestHealthzFailureThenSuccessAcrossTheBoundary(t *testing.T) {
	const burst = 8
	es := newSteppedCount(func(call int32) (*http.Response, error) {
		if call == 1 {
			return esResponse(http.StatusInternalServerError, `{"error":{"type":"cluster_block_exception"}}`), nil
		}
		return esResponse(http.StatusOK, `{"count":20635}`), nil
	})
	s, _ := newTestServer(t, es)
	waiting := signalWaits(s)

	first := make([]*httptest.ResponseRecorder, burst)
	var wgFirst sync.WaitGroup
	for i := range burst {
		wgFirst.Go(func() { first[i] = healthzProbe(s, context.Background(), fmt.Sprintf("first-%02d", i)) })
	}
	receiveN(t, waiting, burst, "probes waiting")
	receiveCall(t, es.entered, 1)

	straddler := make(chan *httptest.ResponseRecorder, 1)
	go func() { straddler <- healthzProbe(s, context.Background(), "straddler") }()
	es.release(1)
	wgFirst.Wait()
	for _, rec := range first {
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	}

	second := make([]*httptest.ResponseRecorder, burst)
	var wgSecond sync.WaitGroup
	for i := range burst {
		wgSecond.Go(func() { second[i] = healthzProbe(s, context.Background(), fmt.Sprintf("second-%02d", i)) })
	}
	// The straddler's wait plus the second burst's.
	receiveN(t, waiting, burst+1, "probes waiting")
	receiveCall(t, es.entered, 2)
	es.release(2)
	wgSecond.Wait()
	for _, rec := range second {
		checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
	}
	rec := <-straddler
	if rec.Code == http.StatusOK {
		checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
	} else {
		checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
	}
	if n := es.calls.Load(); n != 2 {
		t.Errorf("count calls = %d, want 2", n)
	}
}

// A missing index is the not-seeded-yet signal, not a failure, also when the
// call is shared: every probe waiting on a 404 answers 200 with zero docs and
// nothing is logged.
func TestHealthzSharedMissingIndexAnswersZeroToEveryProbe(t *testing.T) {
	const probes = 10
	es := newSteppedCount(func(int32) (*http.Response, error) {
		return esResponse(http.StatusNotFound, `{"error":{"type":"index_not_found_exception"}}`), nil
	})
	s, logBuf := newTestServer(t, es)
	waiting := signalWaits(s)

	recs := make([]*httptest.ResponseRecorder, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	receiveN(t, waiting, probes, "probes waiting")
	receiveCall(t, es.entered, 1)
	es.release(1)
	wg.Wait()
	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusOK, "{\"docs\":0,\"status\":\"ok\"}\n")
	}
	if n := es.calls.Load(); n != 1 {
		t.Errorf("count calls = %d, want 1", n)
	}
	if strings.Contains(logBuf.String(), "healthz check failed") {
		t.Errorf("a missing index logged a failure: %s", logBuf.String())
	}
}

// Many probes, half of whose clients go away while the call is held: each
// one that left answers 503 and logs one cause line under its own id, the
// ones that stayed all answer 200 and log nothing, and the call is neither
// cancelled nor repeated.
func TestHealthzManyProbesWithHalfLeaving(t *testing.T) {
	const probes = 64
	es := newSteppedCount(countOK(20635))
	s, logBuf := newTestServer(t, es)
	waiting := signalWaits(s)

	cancels := make([]context.CancelFunc, probes)
	ctxs := make([]context.Context, probes)
	for i := range probes {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
		defer cancels[i]()
	}
	recs := make([]*httptest.ResponseRecorder, probes)
	left := make(chan struct{}, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() {
			recs[i] = healthzProbe(s, ctxs[i], fmt.Sprintf("probe-%02d", i))
			if i%2 == 1 {
				left <- struct{}{}
			}
		})
	}
	receiveN(t, waiting, probes, "probes waiting")
	receiveCall(t, es.entered, 1)
	for i := 1; i < probes; i += 2 {
		cancels[i]()
	}
	receiveN(t, left, probes/2, "probes that left")
	es.release(1)
	wg.Wait()

	for i, rec := range recs {
		if i%2 == 1 {
			checkHealthzAnswer(t, rec, http.StatusServiceUnavailable, healthzErrorBody)
		} else {
			checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
		}
	}
	if n, c := es.calls.Load(), es.cancelled.Load(); n != 1 || c != 0 {
		t.Errorf("count calls = %d (cancelled %d), want 1 and 0", n, c)
	}
	logged := map[string]int{}
	for _, m := range logLines(t, logBuf) {
		if m["msg"] == "healthz check failed" {
			id, _ := m["request_id"].(string)
			logged[id]++
		}
	}
	for i := range probes {
		id := fmt.Sprintf("probe-%02d", i)
		want := 0
		if i%2 == 1 {
			want = 1
		}
		if logged[id] != want {
			t.Errorf("request %s logged %d cause lines, want %d", id, logged[id], want)
		}
	}
}

// With the cap on requests in flight at 1 and that one slot taken, other
// requests are refused while concurrent probes still answer, and share one
// call doing it.
func TestHealthzAnswersWhileTheCapIsFull(t *testing.T) {
	searchEntered := make(chan struct{}, 1)
	searchRelease := make(chan struct{})
	counts := newSteppedCount(countOK(20635))
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_count") {
			return counts.RoundTrip(r)
		}
		select {
		case searchEntered <- struct{}{}:
		default:
		}
		select {
		case <-searchRelease:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return esResponse(http.StatusNotFound, `{"error":{"type":"index_not_found_exception"}}`), nil
	})
	s, _ := newTestServer(t, rt)
	if err := s.SetMaxInFlight(1); err != nil {
		t.Fatal(err)
	}
	waiting := signalWaits(s)

	held := make(chan struct{})
	go func() {
		defer close(held)
		get(t, s, "/api/search?q=pikachu")
	}()
	select {
	case <-searchEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("the search holding the only slot never reached upstream")
	}
	if code := get(t, s, "/api/meta").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("with the cap full /api/meta = %d, want 503", code)
	}

	const probes = 6
	recs := make([]*httptest.ResponseRecorder, probes)
	var wg sync.WaitGroup
	for i := range probes {
		wg.Go(func() { recs[i] = healthzProbe(s, context.Background(), fmt.Sprintf("probe-%02d", i)) })
	}
	receiveN(t, waiting, probes, "probes waiting")
	receiveCall(t, counts.entered, 1)
	counts.release(1)
	wg.Wait()
	for _, rec := range recs {
		checkHealthzAnswer(t, rec, http.StatusOK, healthzOKBody)
	}
	if n := counts.calls.Load(); n != 1 {
		t.Errorf("count calls = %d, want 1", n)
	}
	close(searchRelease)
	<-held
}

// Only GET reaches the probe: another method is refused as it always was, and
// makes no call upstream.
func TestHealthzOtherMethodsMakeNoCall(t *testing.T) {
	es := newSteppedCount(countOK(20635))
	s, _ := newTestServer(t, es)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /healthz = %d, want 405", method, rec.Code)
		}
	}
	if n := es.calls.Load(); n != 0 {
		t.Errorf("refused methods made %d count calls", n)
	}
}

// A probe sent after another has had its answer is never answered from that
// same call: back to back, every probe makes its own call and reports that
// call's count, many times over. A repeated count would be a cached answer.
func TestHealthzBackToBackProbesNeverReuseAnAnsweredCall(t *testing.T) {
	const probes = 500
	var calls atomic.Int32
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		return esResponse(http.StatusOK, fmt.Sprintf(`{"count":%d}`, n)), nil
	})
	s, _ := newTestServer(t, rt)
	for i := int32(1); i <= probes; i++ {
		rec := healthzProbe(s, context.Background(), fmt.Sprintf("probe-%03d", i))
		want := fmt.Sprintf("{\"docs\":%d,\"status\":\"ok\"}\n", i)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("probe %d = %d %q, want 200 %q: it was answered from an earlier call", i, rec.Code, rec.Body.String(), want)
		}
	}
	if n := calls.Load(); n != probes {
		t.Errorf("%d probes in a row made %d count calls, want %d", probes, n, probes)
	}
}
