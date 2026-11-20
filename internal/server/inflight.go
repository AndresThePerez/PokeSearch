package server

import (
	"fmt"
	"net/http"
	"strings"
)

// DefaultMaxInFlight is how many requests the server handles at once; the
// next one is answered 503 at once. MAX_IN_FLIGHT overrides it.
//
// Sized for the 256 MB container, from a build held to that limit with no
// swap, an upstream that answered after 200 ms with a real-sized search
// answer (121 KB, 100 cards) so that requests overlapped, and clients that
// asked for gzip. The costliest accepted request is a search with debug=1,
// page_size=100, both lists at their 1,024-character totals, a 256-character
// q of '<' and a 20,480-byte head. With far more of them arriving at once
// than the cap admits, the process peaked at:
//
//   - 37 MiB under a cap of 1: the process and the refused requests;
//   - 87 MiB under a cap of 64;
//   - 144 MiB under a cap of 128 (148 MiB with GOMEMLIMIT=180MiB).
//
// So one admitted costliest request holds about 0.9 MiB, and 128 of them
// about 128 × 0.9 ≈ 115 MiB over that base of 37 MiB: about 152 MiB, which
// is what was measured, and leaves about 100 MiB of the limit to spare.
//
// The cap bounds requests being worked on, not connections: a connection
// costs memory until its request has been refused and the connection closed,
// so how many connections arrive at once is for the proxy in front to bound.
//
// Honest traffic is far below it: the page makes a handful of requests per
// search, a Courier load run uses at most 50 workers and runs one at a time,
// and PokéPrices sends its requests one after another.
const DefaultMaxInFlight = 128

// overloadedMessage is what a refused request is told, in the envelope under
// /api/ and as plain text elsewhere.
const overloadedMessage = "the server is at capacity; try again shortly"

// overloadedRetryAfter is the refusal's Retry-After, in seconds. A slot frees
// as soon as any admitted request returns, which is a fraction of a second for
// every request this server answers, so one second is enough for a slot to
// have come back; asking for longer would only slow an honest retry, and zero
// would invite one at once.
const overloadedRetryAfter = "1"

// inFlightSlots is the cap's state: one buffered element per admitted request.
type inFlightSlots struct {
	slots chan struct{}
}

// SetMaxInFlight sets how many requests the server handles at once. A number
// below one is refused and the cap stays as it was: zero never means "no cap".
func (s *Server) SetMaxInFlight(n int) error {
	if n < 1 {
		return fmt.Errorf("the number of requests in flight must be at least 1, got %d", n)
	}
	s.inFlight.Store(&inFlightSlots{slots: make(chan struct{}, n)})
	return nil
}

// exemptFromCap names the health probes. They are never refused: the
// container healthcheck (the binary's -ping asks /livez) and an uptime monitor
// must keep seeing the process as up while it sheds load, or a burst would be
// reported as an outage and could get a healthy container restarted. The cost
// is that probe requests are not bounded by the cap: every /healthz makes its
// own _count call upstream, and a flood of probes is for the edge in front to
// bound. /debug/vars is not a probe and is capped like everything else.
func exemptFromCap(path string) bool {
	return path == "/livez" || path == "/healthz"
}

// withInFlightCap admits at most the configured number of requests at a time
// and answers every further one at once, without queueing.
//
// It sits inside withObservability and withSecurityHeaders, so a refused
// request still gets its request id, the security headers and one ordinary
// access line, and outside withAPIKey, the method check and the mux, so a
// refusal parses nothing, builds no query, calls no upstream and writes no
// query-log line.
//
// The slot is given back by a deferred receive, so it returns when the
// handler does, whether it returned normally, because its client went away
// (the request context is cancelled and the upstream call ends) or by a
// panic, which then carries on to net/http exactly as before. A slow client
// that was admitted keeps its slot until the write timeout ends it.
func (s *Server) withInFlightCap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exemptFromCap(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Loaded once, so a slot is given back to the pool it was taken from.
		pool := s.inFlight.Load()
		select {
		case pool.slots <- struct{}{}:
			defer func() { <-pool.slots }()
			next.ServeHTTP(w, r)
		default:
			s.refuseOverloaded(w, r)
		}
	})
}

// refuseOverloaded is the cap's 503. It asks the client to close the
// connection, so a refused connection is not kept open for the idle timeout.
func (s *Server) refuseOverloaded(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Retry-After", overloadedRetryAfter)
	h.Set("Connection", "close")
	if strings.HasPrefix(r.URL.Path, apiPrefix) {
		s.writeError(w, r, http.StatusServiceUnavailable, codeOverloaded, "", overloadedMessage)
		return
	}
	http.Error(w, overloadedMessage, http.StatusServiceUnavailable)
}
