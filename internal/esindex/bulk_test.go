package esindex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

func TestBulkBody(t *testing.T) {
	docs := make([]tcg.Card, 2)
	for i := range docs {
		docs[i] = tcg.Card{ID: fmt.Sprintf("base1-%d", i+1), Name: "Alakazam", Supertype: "Pokémon"}
	}
	body, err := BulkBody(docs)
	if err != nil {
		t.Fatal(err)
	}
	// 2 docs → 4 NDJSON lines, trailing newline required by _bulk.
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Error("bulk body must end with newline")
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d", len(lines))
	}
	var action struct {
		Index struct {
			ID string `json:"_id"`
		} `json:"index"`
	}
	if err := json.Unmarshal(lines[0], &action); err != nil || action.Index.ID != "base1-1" {
		t.Errorf("action line: %s (err %v)", lines[0], err)
	}
	var doc map[string]any
	if err := json.Unmarshal(lines[1], &doc); err != nil || doc["id"] != "base1-1" {
		t.Errorf("doc line: %s (err %v)", lines[1], err)
	}
	if err := json.Unmarshal(lines[2], &action); err != nil || action.Index.ID != "base1-2" {
		t.Errorf("second action line: %s (err %v)", lines[2], err)
	}
}

// The seeder chunks the corpus itself, so an empty tail chunk must encode to
// an empty body rather than a stray newline _bulk would reject.
func TestBulkBodyEmpty(t *testing.T) {
	body, err := BulkBody(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
}
