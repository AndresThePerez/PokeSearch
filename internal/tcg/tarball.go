package tcg

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Archive holds everything Pokesearch needs from one pokemon-tcg-data
// tarball, entirely in memory. Keys are set ids ("base1").
type Archive struct {
	Sets  map[string]SourceSet
	Cards map[string][]SourceCard
}

// ParseArchive reads a gzipped GitHub source tarball. Entries live under a
// variable root directory (e.g. "pokemon-tcg-data-master/"); only
// "sets/en.json" and "cards/en/<setid>.json" below it are read. Entry order
// is not assumed.
func ParseArchive(r io.Reader) (*Archive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	a := &Archive{Sets: map[string]SourceSet{}, Cards: map[string][]SourceCard{}}
	foundSets := false
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Strip the "<repo>-<ref>/" root component.
		_, rel, ok := strings.Cut(hdr.Name, "/")
		if !ok {
			continue
		}
		switch {
		case rel == "sets/en.json":
			foundSets = true
			var sets []SourceSet
			if err := json.NewDecoder(tr).Decode(&sets); err != nil {
				return nil, fmt.Errorf("decode %s: %w", hdr.Name, err)
			}
			for _, s := range sets {
				a.Sets[s.ID] = s
			}
		case strings.HasPrefix(rel, "cards/en/") && strings.HasSuffix(rel, ".json"):
			setID := strings.TrimSuffix(path.Base(rel), ".json")
			var cards []SourceCard
			if err := json.NewDecoder(tr).Decode(&cards); err != nil {
				return nil, fmt.Errorf("decode %s: %w", hdr.Name, err)
			}
			a.Cards[setID] = cards
		}
	}
	if !foundSets {
		return nil, fmt.Errorf("tarball contained no sets/en.json")
	}
	return a, nil
}

// Docs joins every card with its set metadata via Transform. Output is
// sorted by set id, cards in file order within a set.
func (a *Archive) Docs() ([]Card, error) {
	setIDs := make([]string, 0, len(a.Cards))
	for id := range a.Cards {
		setIDs = append(setIDs, id)
	}
	sort.Strings(setIDs)

	var docs []Card
	for _, id := range setIDs {
		set, ok := a.Sets[id]
		if !ok {
			return nil, fmt.Errorf("cards/en/%s.json has no matching record in sets/en.json", id)
		}
		for _, sc := range a.Cards[id] {
			docs = append(docs, Transform(sc, set))
		}
	}
	return docs, nil
}
