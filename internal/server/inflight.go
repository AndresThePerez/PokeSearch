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
// swap and an upstream that answered after 200 ms, so requests overlapped:
//
//   - The costliest accepted request (a search with debug=1, both lists at
//     their 1,024-character totals, a 256-character q of '<' and a
//     20,480-byte head) holds about 0.4 MiB while in flight: 800 of them at
//     once peaked at 66, 91 and 142 MiB under caps of 64, 128 and 256, and
//     at 30 MiB under a cap of 1, which is the process plus the refused
//     connections. 128 admitted is 128 × 0.4 ≈ 51 MiB.
//   - A refused request costs its connection's buffers only until the 503 has
//     gone out and the connection is closed: 2,000 of the costliest at once
//     peaked at 89 MiB, 10,000 small searches at once at 116 MiB and 4,000
//     kept-alive connections at 84 MiB, all under half the limit.
//   - Without a cap, 4,000 small searches or 800 of the costliest at once got
//     the process killed at the limit.
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
// reported as an outage and could get a healthy container restarted. /livez
// writes a constant and /healthz makes one _count call, so neither is what
// fills memory. /debug/vars is not a probe and is capped like everything else.
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
