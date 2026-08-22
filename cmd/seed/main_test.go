package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// The seed's guard states are the part of this binary that decides whether an
// index survives. TestSeedRun drives every one of them against a scripted ES
// and a fixture tarball server, so the state machine is covered without an
// Elasticsearch anywhere near it.
func TestSeedRun(t *testing.T) {
	const ref = "test-ref"

	cases := []struct {
		name     string
		counts   []int // successive _count replies; -1 = 404
		force    bool
		bulkBody string
		wantErr  string // substring of the expected error; "" = must succeed
		check    func(t *testing.T, f *seedFake, tb *tarballServer)
	}{
		{
			name:   "populated index is left alone",
			counts: []int{20324},
			check: func(t *testing.T, f *seedFake, tb *tarballServer) {
				calls, _, _, chunks, _ := f.snapshot()
				if len(calls) != 1 || !strings.HasSuffix(calls[0], "/cards/_count") {
					t.Errorf("calls = %v, want the count probe only", calls)
				}
				if chunks != 0 {
					t.Errorf("bulk chunks = %d, want 0 — a populated index must not be touched", chunks)
				}
				if tb.attemptCount() != 0 {
					t.Errorf("corpus downloaded %d times; nothing should have been fetched", tb.attemptCount())
				}
			},
		},
		{
			name:   "-force deletes a populated index and reseeds",
			counts: []int{20324, seedDocCount},
			force:  true,
			check: func(t *testing.T, f *seedFake, tb *tarballServer) {
				if !f.called("DELETE /cards") {
					t.Error("-force must delete the populated index")
				}
				assertSeeded(t, f, ref)
			},
		},
		{
			name:   "an empty index is an interrupted seed and is recreated",
			counts: []int{0, seedDocCount},
			check: func(t *testing.T, f *seedFake, tb *tarballServer) {
				if !f.called("DELETE /cards") {
					t.Error("an empty index must be deleted before reseeding")
				}
				assertSeeded(t, f, ref)
			},
		},
		{
			name:   "a missing index is created and seeded",
			counts: []int{-1, seedDocCount},
			check: func(t *testing.T, f *seedFake, tb *tarballServer) {
				if f.called("DELETE /cards") {
					t.Error("a missing index must not be deleted")
				}
				assertSeeded(t, f, ref)
				if got := tb.requestedPaths(); len(got) != 1 || got[0] != "/"+ref {
					t.Errorf("corpus paths = %v, want [/%s] — the ref is appended to the base URL", got, ref)
				}
			},
		},
		{
			name:     "a rejected document fails the run",
			counts:   []int{-1},
			bulkBody: bulkItemFailure,
			wantErr:  "bulk",
		},
		{
			name:    "a final count that disagrees fails the run",
			counts:  []int{-1, seedDocCount - 1},
			wantErr: "count mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &seedFake{counts: tc.counts, bulkBody: tc.bulkBody}
			esURL, tarballBase, tarball := newSeedEnv(t, fake)

			err := run(esURL, tarballBase, ref, tc.force)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("run: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("run succeeded, want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("run error = %v, want it to mention %q", err, tc.wantErr)
			}
			if tc.check != nil {
				tc.check(t, fake, tarball)
			}
		})
	}
}

// bulkItemFailure is a real-shaped _bulk reply: HTTP 200, one rejected doc.
const bulkItemFailure = `{"took":3,"errors":true,"items":[
  {"index":{"_index":"cards","_id":"base1-1","status":201}},
  {"index":{"_index":"cards","_id":"base1-2","status":400,
    "error":{"type":"mapper_parsing_exception","reason":"failed to parse field [hp]"}}}
]}`

// assertSeeded checks the happy path's invariants: the index was created,
// stamped with provenance, bulk-loaded once with every fixture document, and
// left with refresh restored.
func assertSeeded(t *testing.T, f *seedFake, ref string) {
	t.Helper()
	if !f.called("PUT /cards") {
		t.Error("index was never created")
	}
	_, settings, mappings, chunks, docs := f.snapshot()

	if len(mappings) != 1 || !strings.Contains(mappings[0], `"seed_ref":"`+ref+`"`) {
		t.Errorf("_meta stamps = %v, want one carrying seed_ref %q", mappings, ref)
	}
	if chunks != 1 || docs != seedDocCount {
		t.Errorf("bulk = %d chunks / %d docs, want 1 / %d", chunks, docs, seedDocCount)
	}
	if !slices.Contains(settings, `{"index":{"refresh_interval":"-1"}}`) {
		t.Errorf("settings = %v, want refresh disabled during the load", settings)
	}
	if !slices.Contains(settings, `{"index":{"refresh_interval":"30s"}}`) {
		t.Errorf("settings = %v, want refresh restored after the load", settings)
	}
	if !f.called("POST /cards/_forcemerge") {
		t.Error("index was never force-merged")
	}
}

// TestMetaBody pins the seed→server provenance contract without touching an
// index: these are the exact bytes PUT into the mapping, and the shape
// internal/server's loadSeedMeta decodes back out. A rename on either side
// silently turns /api/meta's seed field into null, so it is asserted here.
func TestMetaBody(t *testing.T) {
	at := time.Date(2026, 8, 22, 17, 4, 5, 0, time.FixedZone("CDT", -5*60*60))
	body := metaBody("0af6250a22495e4a3e9f60ff45fc3fedc2e0563d", at)

	var got struct {
		Meta struct {
			SeedRef  string `json:"seed_ref"`
			SeededAt string `json:"seeded_at"`
		} `json:"_meta"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if got.Meta.SeedRef != "0af6250a22495e4a3e9f60ff45fc3fedc2e0563d" {
		t.Errorf("seed_ref = %q", got.Meta.SeedRef)
	}
	// Stamped in UTC regardless of the seeding host's zone, so two seeds are
	// comparable.
	if got.Meta.SeededAt != "2026-08-22T22:04:05Z" {
		t.Errorf("seeded_at = %q, want 2026-08-22T22:04:05Z", got.Meta.SeededAt)
	}
	if _, err := time.Parse(time.RFC3339, got.Meta.SeededAt); err != nil {
		t.Errorf("seeded_at is not RFC3339: %v", err)
	}
}

// A ref with a quote in it must not be able to break out of the JSON body.
func TestMetaBodyEscapesRef(t *testing.T) {
	body := metaBody(`x","injected":"y`, time.Unix(0, 0))
	var got map[string]map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(got["_meta"]) != 2 || got["_meta"]["seed_ref"] != `x","injected":"y` {
		t.Errorf("_meta = %v", got["_meta"])
	}
}
