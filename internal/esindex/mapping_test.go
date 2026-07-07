package esindex

import (
	"encoding/json"
	"testing"
)

func dig(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: not an object at %q", path, k)
		}
		cur = mm[k]
	}
	return cur
}

func TestMappingIsValid(t *testing.T) {
	if IndexName != "cards" {
		t.Errorf("IndexName = %q", IndexName)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(Mapping), &m); err != nil {
		t.Fatalf("Mapping is not valid JSON: %v", err)
	}
	if got := dig(t, m, "settings", "number_of_shards"); got != float64(1) {
		t.Errorf("shards = %v", got)
	}
	if got := dig(t, m, "settings", "refresh_interval"); got != "30s" {
		t.Errorf("refresh_interval = %v", got)
	}
	if got := dig(t, m, "mappings", "properties", "name", "fields", "suggest", "type"); got != "completion" {
		t.Errorf("name.suggest type = %v", got)
	}
	if got := dig(t, m, "mappings", "properties", "name", "fields", "sayt", "type"); got != "search_as_you_type" {
		t.Errorf("name.sayt type = %v", got)
	}
	if got := dig(t, m, "mappings", "properties", "release_date", "format"); got != "yyyy-MM-dd" {
		t.Errorf("release_date format = %v", got)
	}
	if got := dig(t, m, "mappings", "properties", "weaknesses", "enabled"); got != false {
		t.Errorf("weaknesses enabled = %v", got)
	}
	if got := dig(t, m, "mappings", "properties", "image_small", "index"); got != false {
		t.Errorf("image_small index = %v", got)
	}
}
