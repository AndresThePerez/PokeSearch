package tcg

import (
	"encoding/json"
	"testing"
)

// Real record: cards/en/base1.json, id base1-1 (trimmed to one attack/ability — real values).
const alakazamJSON = `{
  "id": "base1-1",
  "name": "Alakazam",
  "supertype": "Pokémon",
  "subtypes": ["Stage 2"],
  "level": "42",
  "hp": "80",
  "types": ["Psychic"],
  "evolvesFrom": "Kadabra",
  "abilities": [{"name": "Damage Swap", "text": "As often as you like during your turn (before your attack), you may move 1 damage counter from 1 of your Pokémon to another as long as you don't Knock Out that Pokémon.", "type": "Pokémon Power"}],
  "attacks": [{"name": "Confuse Ray", "cost": ["Psychic","Psychic","Psychic"], "convertedEnergyCost": 3, "damage": "30", "text": "Flip a coin. If heads, the Defending Pokémon is now Confused."}],
  "weaknesses": [{"type": "Psychic", "value": "×2"}],
  "retreatCost": ["Colorless","Colorless","Colorless"],
  "convertedRetreatCost": 3,
  "number": "1",
  "artist": "Ken Sugimori",
  "rarity": "Rare Holo",
  "flavorText": "Its brain can outperform a supercomputer. Its intelligence quotient is said to be 5000.",
  "nationalPokedexNumbers": [65],
  "legalities": {"unlimited": "Legal"},
  "images": {"small": "https://images.pokemontcg.io/base1/1.png", "large": "https://images.pokemontcg.io/base1/1_hires.png"}
}`

// Real record: cards/en/ecard3.json, id ecard3-47 — nationalPokedexNumbers is explicit null.
const buriedFossilJSON = `{
  "id": "ecard3-47",
  "name": "Buried Fossil",
  "supertype": "Pokémon",
  "subtypes": ["Basic"],
  "hp": "30",
  "types": ["Colorless"],
  "rarity": "Common",
  "number": "47",
  "nationalPokedexNumbers": null,
  "images": {"small": "https://images.pokemontcg.io/ecard3/47.png", "large": "https://images.pokemontcg.io/ecard3/47_hires.png"}
}`

func TestSourceCardDecode(t *testing.T) {
	var c SourceCard
	if err := json.Unmarshal([]byte(alakazamJSON), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.ID != "base1-1" || c.Name != "Alakazam" || c.Supertype != "Pokémon" {
		t.Errorf("identity fields wrong: %+v", c)
	}
	if c.HP != "80" {
		t.Errorf("hp = %q, want \"80\" (raw string)", c.HP)
	}
	if c.EvolvesFrom != "Kadabra" {
		t.Errorf("evolvesFrom = %q", c.EvolvesFrom)
	}
	if len(c.Attacks) != 1 || c.Attacks[0].Damage != "30" || c.Attacks[0].ConvertedEnergyCost != 3 {
		t.Errorf("attacks wrong: %+v", c.Attacks)
	}
	if len(c.Abilities) != 1 || c.Abilities[0].Type != "Pokémon Power" {
		t.Errorf("abilities wrong: %+v", c.Abilities)
	}
	if len(c.Weaknesses) != 1 || c.Weaknesses[0].Value != "×2" {
		t.Errorf("weaknesses wrong: %+v", c.Weaknesses)
	}
	if c.ConvertedRetreatCost != 3 {
		t.Errorf("convertedRetreatCost = %d", c.ConvertedRetreatCost)
	}
	if c.FlavorText == "" || c.Rarity != "Rare Holo" || c.Artist != "Ken Sugimori" {
		t.Errorf("optional fields wrong: %+v", c)
	}
	if len(c.NationalPokedexNumbers) != 1 || c.NationalPokedexNumbers[0] != 65 {
		t.Errorf("dex numbers wrong: %v", c.NationalPokedexNumbers)
	}
	if c.Images.Small != "https://images.pokemontcg.io/base1/1.png" {
		t.Errorf("images wrong: %+v", c.Images)
	}
}

func TestSourceCardDecodeNullDexNumbers(t *testing.T) {
	var c SourceCard
	if err := json.Unmarshal([]byte(buriedFossilJSON), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.NationalPokedexNumbers != nil {
		t.Errorf("want nil dex numbers, got %v", c.NationalPokedexNumbers)
	}
	if c.FlavorText != "" || c.EvolvesFrom != "" {
		t.Errorf("absent fields must decode to zero values: %+v", c)
	}
}

func TestSourceSetDecode(t *testing.T) {
	// Real record: sets/en.json, id base1 (extra source fields present on purpose).
	raw := `{"id":"base1","name":"Base","series":"Base","printedTotal":102,"total":102,"ptcgoCode":"BS","releaseDate":"1999/01/09","updatedAt":"2022/10/10 15:12:00"}`
	var s SourceSet
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.ID != "base1" || s.Name != "Base" || s.Series != "Base" || s.Total != 102 || s.ReleaseDate != "1999/01/09" {
		t.Errorf("set fields wrong: %+v", s)
	}
}
