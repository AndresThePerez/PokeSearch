package server

import (
	"net/http"
	"strings"
	"testing"
)

// A strong validator promises byte-identical bodies (RFC 9110 §8.8.1), and the
// gzip and identity answers for the same file are not. The two may share a
// tag only if it is weak.
func TestStaticRepresentationsNeverShareAStrongETag(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	for _, path := range []string{"/styles.css", "/"} {
		plain := get(t, s, path)
		zipped := getWith(t, s, path, map[string]string{"Accept-Encoding": "gzip"})
		plainTag, zippedTag := plain.Header().Get("ETag"), zipped.Header().Get("ETag")
		if plainTag == "" || zippedTag == "" {
			t.Fatalf("%s: ETags %q and %q, want both present", path, plainTag, zippedTag)
		}
		if plainTag == zippedTag && !strings.HasPrefix(plainTag, "W/") {
			t.Errorf("%s: identity and gzip share the strong ETag %s", path, plainTag)
		}
	}
	zipped := getWith(t, s, "/styles.css", map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("gzip answer: Content-Encoding %q, Vary %q; want gzip and Accept-Encoding",
			zipped.Header().Get("Content-Encoding"), zipped.Header().Get("Vary"))
	}
}

// Whichever representation a client holds, its tag revalidates under either
// encoding, written as it was received or with the weak prefix added or
// dropped (a proxy may weaken a tag on the way).
func TestStaticETagRevalidatesAcrossEncodings(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	encodings := []string{"", "gzip"}
	for _, path := range []string{"/styles.css", "/"} {
		for _, heldEnc := range encodings {
			tag := getWith(t, s, path, map[string]string{"Accept-Encoding": heldEnc}).Header().Get("ETag")
			opaque := strings.TrimPrefix(tag, "W/")
			for _, inm := range []string{tag, opaque, "W/" + opaque, `"0000000000000000", ` + tag} {
				for _, askEnc := range encodings {
					rec := getWith(t, s, path, map[string]string{"Accept-Encoding": askEnc, "If-None-Match": inm})
					if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
						t.Errorf("%s held as %q, asked as %q with If-None-Match %s: status %d with %d bytes, want a bare 304",
							path, heldEnc, askEnc, inm, rec.Code, rec.Body.Len())
					}
					if got := rec.Header().Get("ETag"); got != tag && strings.TrimPrefix(got, "W/") != opaque {
						t.Errorf("%s: 304 carries ETag %q, want %q", path, got, tag)
					}
				}
			}
		}
	}
	stale := getWith(t, s, "/styles.css", map[string]string{"If-None-Match": `W/"0000000000000000"`})
	if stale.Code != http.StatusOK || stale.Body.Len() == 0 {
		t.Errorf("a stale weak validator: status %d with %d bytes, want the file", stale.Code, stale.Body.Len())
	}
}

// If-None-Match uses the weak comparison (RFC 9110 §8.8.3.2): the W/ prefix is
// ignored on both the candidate and the stored tag.
func TestETagMatchesIsWeakOnBothSides(t *testing.T) {
	for _, tc := range []struct {
		inm, tag string
		want     bool
	}{
		{`"abc"`, `"abc"`, true},
		{`W/"abc"`, `"abc"`, true},
		{`"abc"`, `W/"abc"`, true},
		{`W/"abc"`, `W/"abc"`, true},
		{`"x", W/"abc"`, `W/"abc"`, true},
		{`*`, `W/"abc"`, true},
		{`"abd"`, `W/"abc"`, false},
		{`W/"abd"`, `"abc"`, false},
		{``, `W/"abc"`, false},
		{`W/`, `W/""`, false},
	} {
		if got := etagMatches(tc.inm, tc.tag); got != tc.want {
			t.Errorf("etagMatches(%q, %q) = %v, want %v", tc.inm, tc.tag, got, tc.want)
		}
	}
}
