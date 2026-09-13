package esindex

// IndexNameV2 is the shadow index the analyzer work is built on. It is never
// read by the server: the server reads IndexName, and nothing in this package
// aliases one onto the other. Putting cards_v2 behind the live name is a
// separate, coordinated change and is deliberately not done here.
const IndexNameV2 = "cards_v2"

// MappingV2 is Mapping with a real analysis chain and nothing else. Field for
// field it is the Design Spec mapping above — same shards, same refresh
// interval, same index:false and enabled:false decisions — changed in exactly
// three ways:
//
//  1. The name text field gets a named analyzer, card_name, instead of the
//     engine default. A standard tokenizer, then lowercase, asciifolding, the
//     suffix synonyms and a word_delimiter_graph.
//  2. card_suffix_synonyms ties the suffix spellings the corpus carries
//     together: -GX, -EX and the modern lowercase "ex" are one group, so a
//     searcher who types one spelling reaches the cards printed with another.
//  3. The lc normalizer folds as well as lowercases, so the keyword side of
//     name, evolves_from, artist and set_name answers an unaccented query.
//
// On asciifolding. The corpus carries an accented name of its own — Pokémon
// Catcher, Pokémon Center Lady, Pokémon Communication and the rest — and the
// analysis configuration it was indexed with folds nothing, so a searcher who
// types "pokemon" and a searcher who types "Pokémon" reach different token
// streams and different results. Folding is the fix and it is not optional
// here; the served index gets it at the next coordinated reseed.
//
// On filter order. The synonym filter analyzes its own rules with whatever
// precedes it, and word_delimiter_graph cannot be used for that — a chain with
// the two in the other order is rejected at create time with "cannot be used
// to parse synonyms". So the synonyms sit between asciifolding and the
// delimiter filter, and flatten_graph closes the chain because a graph filter
// in an index analyzer has to be flattened before the terms are written.
//
// On the delimiter settings. The standard tokenizer already breaks on hyphens,
// so the delimiter filter is here for what the tokenizer keeps whole: LV.X
// splits into its parts, and an English possessive ("Ethan's Ho-Oh ex") is
// stemmed to the bare name. preserve_original keeps the untouched token beside
// the parts so nothing the tokenizer produced is lost. Splitting on numerics
// and on case change are both off on purpose: they would break Porygon2 into
// two terms and VMAX and VSTAR into three, and none of those is a suffix form
// anyone searches by halves.
const MappingV2 = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "refresh_interval": "30s",
    "analysis": {
      "filter": {
        "card_suffix_synonyms": {
          "type": "synonym",
          "lenient": false,
          "synonyms": ["gx, ex"]
        },
        "name_delimiter": {
          "type": "word_delimiter_graph",
          "generate_word_parts": true,
          "generate_number_parts": true,
          "catenate_words": false,
          "catenate_numbers": false,
          "catenate_all": false,
          "split_on_case_change": false,
          "split_on_numerics": false,
          "stem_english_possessive": true,
          "preserve_original": true
        }
      },
      "analyzer": {
        "card_name": {
          "type": "custom",
          "tokenizer": "standard",
          "filter": [
            "lowercase",
            "asciifolding",
            "card_suffix_synonyms",
            "name_delimiter",
            "flatten_graph"
          ]
        }
      },
      "normalizer": {
        "lc": {"type": "custom", "filter": ["lowercase", "asciifolding"]}
      }
    }
  },
  "mappings": {
    "properties": {
      "id":           {"type": "keyword"},
      "name": {
        "type": "text",
        "analyzer": "card_name",
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
