package esindex

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// BulkBody encodes docs as one _bulk NDJSON body: an action line
// {"index":{"_id":...}} followed by the document line, per document.
//
// Chunking is the caller's job on purpose. The corpus is 20k documents, and
// encoding every chunk before sending the first one holds the whole corpus in
// memory twice — once as documents, once as JSON — inside a 512m seed
// container. One chunk at a time keeps the peak flat.
func BulkBody(docs []tcg.Card) ([]byte, error) {
	var buf bytes.Buffer
	for _, d := range docs {
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
	return buf.Bytes(), nil
}
