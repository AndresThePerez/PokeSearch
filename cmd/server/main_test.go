package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// TestRunShutsDownOnContextCancel proves the SIGTERM path: cancelling the
// context returned by signal.NotifyContext must drain and exit, not leak the
// listener or block forever. esURL points at a closed port on purpose — run()
// must not require a reachable cluster to start or to stop.
func TestRunShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, config{port: "0", esURL: "http://127.0.0.1:1"}) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after cancel")
	}
}

// countingTransport answers every round trip with one fixed outcome and
// counts how many the client made.
type countingTransport struct {
	calls  atomic.Int32
	status int
	err    error
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{
		StatusCode: c.status,
		Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

// The production client retries at most once, never on a 503 (an overloaded
// cluster is not helped by more load), and its backoff keeps a failing call
// well under a second.
func TestESConfigRetriesAtMostOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		err       error
		wantCalls int32
	}{
		{"ok", 200, nil, 1},
		{"bad request", 400, nil, 1},
		{"unavailable", 503, nil, 1},
		{"bad gateway", 502, nil, 2},
		{"gateway timeout", 504, nil, 2},
		{"refused", 0, errors.New("dial tcp 192.0.2.10:9200: connect: connection refused"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &countingTransport{status: tc.status, err: tc.err}
			cfg := newESConfig("http://192.0.2.10:9200")
			cfg.Transport = rt
			es, err := elasticsearch.NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			res, err := es.Info()
			if err == nil {
				res.Body.Close()
			}
			if got := rt.calls.Load(); got != tc.wantCalls {
				t.Errorf("%d round trips, want %d", got, tc.wantCalls)
			}
			if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
				t.Errorf("the call took %s, want well under a second", elapsed)
			}
		})
	}
}

// Against a port nothing listens on, the configured client gives up in well
// under a second: the whole retry budget, backoff included.
func TestESConfigDeadClusterFailsFast(t *testing.T) {
	es, err := elasticsearch.NewClient(newESConfig("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := es.Count()
	if err == nil {
		res.Body.Close()
		t.Fatal("a closed port answered")
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("a dead cluster took %s to fail, want under a second", elapsed)
	}
}
