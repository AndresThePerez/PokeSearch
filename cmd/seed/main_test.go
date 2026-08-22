package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
