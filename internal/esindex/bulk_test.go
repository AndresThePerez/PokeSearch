package esindex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

func TestBulkBodies(t *testing.T) {
	docs := make([]tcg.Card, 5)
	for i := range docs {
		docs[i] = tcg.Card{ID: fmt.Sprintf("base1-%d", i+1), Name: "Alakazam", Supertype: "Pokémon"}
	}
	bodies, err := BulkBodies(docs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 3 { // 2 + 2 + 1
		t.Fatalf("want 3 chunks, got %d", len(bodies))
	}
	// First chunk: 2 docs → 4 NDJSON lines, trailing newline required by _bulk.
	first := bodies[0]
	if !bytes.HasSuffix(first, []byte("\n")) {
		t.Error("bulk body must end with newline")
	}
	lines := bytes.Split(bytes.TrimSuffix(first, []byte("\n")), []byte("\n"))
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
	if lastLines := bytes.Split(bytes.TrimSuffix(bodies[2], []byte("\n")), []byte("\n")); len(lastLines) != 2 {
		t.Errorf("last chunk: want 2 lines, got %d", len(lastLines))
	}
}
