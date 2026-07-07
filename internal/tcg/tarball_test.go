package tcg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func buildTarball(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

const setsJSON = `[
  {"id":"base1","name":"Base","series":"Base","printedTotal":102,"total":102,"releaseDate":"1999/01/09"},
  {"id":"ex11","name":"Delta Species","series":"EX","printedTotal":113,"total":114,"releaseDate":"2005/10/31"}
]`

func TestParseArchiveAndDocs(t *testing.T) {
	// Note: map iteration order varies, and GitHub tarballs list cards/
	// before sets/ anyway — ParseArchive must not depend on entry order.
	buf := buildTarball(t, map[string]string{
		"pokemon-tcg-data-master/README.md":          "# ignored",
		"pokemon-tcg-data-master/cards/en/base1.json": "[" + alakazamJSON + "]",
		"pokemon-tcg-data-master/cards/en/ex11.json":  "[" + mewtwoDeltaJSON + "]",
		"pokemon-tcg-data-master/sets/en.json":        setsJSON,
	})
	a, err := ParseArchive(buf)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	if len(a.Sets) != 2 || a.Sets["base1"].Name != "Base" || a.Sets["ex11"].ReleaseDate != "2005/10/31" {
		t.Errorf("sets: %+v", a.Sets)
	}
	if len(a.Cards) != 2 || len(a.Cards["base1"]) != 1 || a.Cards["ex11"][0].Name != "Mewtwo δ" {
		t.Errorf("cards: %+v", a.Cards)
	}

	docs, err := a.Docs()
	if err != nil {
		t.Fatalf("Docs: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	// Sorted by set id: base1 before ex11.
	if docs[0].ID != "base1-1" || docs[1].ID != "ex11-12" {
		t.Errorf("order: %q, %q", docs[0].ID, docs[1].ID)
	}
	if docs[0].SetName != "Base" || docs[0].ReleaseDate != "1999-01-09" {
		t.Errorf("join: %+v", docs[0])
	}
	if docs[1].SetSeries != "EX" || docs[1].SetTotal != 114 {
		t.Errorf("join: %+v", docs[1])
	}
}

func TestDocsMissingSet(t *testing.T) {
	buf := buildTarball(t, map[string]string{
		"root/cards/en/base1.json": "[" + alakazamJSON + "]",
		"root/sets/en.json":        "[]",
	})
	a, err := ParseArchive(buf)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	if _, err := a.Docs(); err == nil {
		t.Fatal("want error for card file with no set record")
	}
}
