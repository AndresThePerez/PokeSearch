package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
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
