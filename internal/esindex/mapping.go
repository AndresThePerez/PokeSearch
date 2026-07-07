// Package esindex owns the cards index definition and bulk-body encoding.
package esindex

// IndexName is the one index Pokesearch reads and writes.
const IndexName = "cards"

// Mapping is the create-index body: write-once/read-many settings, the
// lowercase keyword normalizer, and the full field mapping from the Design
// Spec. Display-only fields carry index:false / enabled:false.
const Mapping = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "refresh_interval": "30s",
    "analysis": {
      "normalizer": {
        "lc": {"type": "custom", "filter": ["lowercase"]}
      }
    }
  },
  "mappings": {
    "properties": {
      "id":           {"type": "keyword"},
      "name": {
        "type": "text",
        "fields": {
          "kw":      {"type": "keyword", "normalizer": "lc"},
          "sayt":    {"type": "search_as_you_type"},
          "suggest": {"type": "completion"}
        }
      },
      "supertype":    {"type": "keyword"},
      "subtypes":     {"type": "keyword"},
      "hp":           {"type": "integer"},
      "types":        {"type": "keyword"},
      "evolves_from": {"type": "text", "fields": {"kw": {"type": "keyword", "normalizer": "lc"}}},
      "attacks": {
        "properties": {
          "name":           {"type": "text"},
          "cost":           {"type": "keyword", "index": false},
          "converted_cost": {"type": "integer", "index": false},
          "damage":         {"type": "keyword", "index": false},
          "damage_value":   {"type": "integer"},
          "text":           {"type": "text"}
        }
      },
      "abilities": {
        "properties": {
          "name": {"type": "text"},
          "type": {"type": "keyword", "index": false},
          "text": {"type": "text"}
        }
      },
      "weaknesses":  {"type": "object", "enabled": false},
      "resistances": {"type": "object", "enabled": false},
      "retreat_cost": {"type": "integer", "index": false},
      "rarity":       {"type": "keyword"},
      "artist":       {"type": "text", "fields": {"kw": {"type": "keyword", "normalizer": "lc"}}},
      "flavor_text":  {"type": "text"},
      "national_pokedex_numbers": {"type": "integer"},
      "number":       {"type": "keyword"},
      "set_id":       {"type": "keyword"},
      "set_name":     {"type": "text", "fields": {"kw": {"type": "keyword", "normalizer": "lc"}}},
      "set_series":   {"type": "keyword"},
      "set_total":    {"type": "integer", "index": false},
      "release_date": {"type": "date", "format": "yyyy-MM-dd"},
      "image_small":  {"type": "keyword", "index": false},
      "image_large":  {"type": "keyword", "index": false}
    }
  }
}`
