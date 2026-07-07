package esindex

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// BulkBodies encodes docs as _bulk NDJSON bodies of at most chunkSize docs
// each: an action line {"index":{"_id":...}} then the document line.
func BulkBodies(docs []tcg.Card, chunkSize int) ([][]byte, error) {
	if chunkSize < 1 {
		return nil, fmt.Errorf("chunkSize must be >= 1, got %d", chunkSize)
	}
	var bodies [][]byte
	for start := 0; start < len(docs); start += chunkSize {
		end := min(start+chunkSize, len(docs))
		var buf bytes.Buffer
		for _, d := range docs[start:end] {
			action, err := json.Marshal(map[string]any{"index": map[string]any{"_id": d.ID}})
			if err != nil {
				return nil, err
			}
			doc, err := json.Marshal(d)
			if err != nil {
				return nil, fmt.Errorf("marshal %s: %w", d.ID, err)
			}
			buf.Write(action)
			buf.WriteByte('\n')
			buf.Write(doc)
			buf.WriteByte('\n')
		}
		bodies = append(bodies, buf.Bytes())
	}
	return bodies, nil
}
