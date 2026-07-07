package tcg

import (
	"encoding/json"
	"testing"
)

// Real record: cards/en/ex11.json, id ex11-12 — δ name, "10×" damage, two
// types, no flavorText, and a source "rules" field that must be dropped.
const mewtwoDeltaJSON = `{
  "id": "ex11-12",
  "name": "Mewtwo δ",
  "supertype": "Pokémon",
  "subtypes": ["Basic"],
  "hp": "70",
  "types": ["Fire", "Metal"],
  "rules": ["This Pokémon is both Fire Metal type."],
  "abilities": [{"name": "Delta Switch", "text": "Once during your turn, when you put Mewtwo from your hand onto your Bench, you may move any number of basic Energy cards attached to your Pokémon to your other Pokémon (excluding Mewtwo) in any way you like.", "type": "Poké-Power"}],
  "attacks": [{"name": "Energy Burst", "cost": ["Fire","Metal"], "convertedEnergyCost": 2, "damage": "10×", "text": "Does 10 damage times the total amount of Energy attached to Mewtwo and the Defending Pokémon."}],
  "weaknesses": [{"type": "Psychic", "value": "×2"}],
  "retreatCost": ["Colorless"],
  "convertedRetreatCost": 1,
  "number": "12",
  "artist": "Ryo Ueda",
  "rarity": "Rare Holo",
  "nationalPokedexNumbers": [150],
  "legalities": {"unlimited": "Legal"},
  "images": {"small": "https://images.pokemontcg.io/ex11/12.png", "large": "https://images.pokemontcg.io/ex11/12_hires.png"}
}`

// Real record: cards/en/cel25c.json, id cel25c-17_A — ★ name, "_" in id,
// EMPTY damage string, resistances.
const umbreonStarJSON = `{
  "id": "cel25c-17_A",
  "name": "Umbreon ★",
  "supertype": "Pokémon",
  "subtypes": ["Basic", "Star"],
  "hp": "70",
  "types": ["Darkness"],
  "abilities": [{"type": "Poké-Power", "name": "Dark Ray", "text": "Once during your turn, when you put Umbreon Star from your hand onto your Bench, you may choose 1 card from your opponent's hand without looking and discard it."}],
  "attacks": [{"cost": ["Darkness","Darkness"], "name": "Feint Attack", "damage": "", "text": "Choose 1 of your opponent's Pokémon. This attack does 30 damage to that Pokémon.", "convertedEnergyCost": 2}],
  "weaknesses": [{"type": "Fighting", "value": "×2"}],
  "resistances": [{"type": "Psychic", "value": "-30"}],
  "retreatCost": ["Colorless"],
  "convertedRetreatCost": 1,
  "number": "17",
  "artist": "Masakazu Fukuda",
  "rarity": "Classic Collection",
  "nationalPokedexNumbers": [197],
  "images": {"small": "https://images.pokemontcg.io/cel25c/17_A.png", "large": "https://images.pokemontcg.io/cel25c/17_A_hires.png"}
}`

// Real record: cards/en/base1.json, id base1-70 — a TRAINER that carries hp
// ("10"), with no flavorText and no types.
const clefairyDollJSON = `{
  "id": "base1-70",
  "name": "Clefairy Doll",
  "supertype": "Trainer",
  "hp": "10",
  "rules": ["Play Clefairy Doll as if it were a Basic Pokémon."],
  "number": "70",
  "artist": "Keiji Kinebuchi",
  "rarity": "Rare",
  "images": {"small": "https://images.pokemontcg.io/base1/70.png", "large": "https://images.pokemontcg.io/base1/70_hires.png"}
}`

// Real record: cards/en/base1.json, id base1-96 — Energy: no hp, no attacks.
const dceJSON = `{
  "id": "base1-96",
  "name": "Double Colorless Energy",
  "supertype": "Energy",
  "subtypes": ["Special"],
  "number": "96",
  "artist": "Keiji Kinebuchi",
  "rarity": "Uncommon",
  "images": {"small": "https://images.pokemontcg.io/base1/96.png", "large": "https://images.pokemontcg.io/base1/96_hires.png"}
}`

// Real records from sets/en.json.
var (
	setBase1  = SourceSet{ID: "base1", Name: "Base", Series: "Base", Total: 102, ReleaseDate: "1999/01/09"}
	setEx11   = SourceSet{ID: "ex11", Name: "Delta Species", Series: "EX", Total: 114, ReleaseDate: "2005/10/31"}
	setCel25c = SourceSet{ID: "cel25c", Name: "Celebrations: Classic Collection", Series: "Sword & Shield", Total: 25, ReleaseDate: "2021/10/08"}
)

func mustDecode(t *testing.T, raw string) SourceCard {
	t.Helper()
	var c SourceCard
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return c
}

func TestParseDamage(t *testing.T) {
	cases := map[string]int{"": 0, "30": 30, "80": 80, "10+": 10, "100×": 100, "120-": 120, "10×": 10}
	for in, want := range cases {
		if got := ParseDamage(in); got != want {
			t.Errorf("ParseDamage(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeDate(t *testing.T) {
	if got := NormalizeDate("1999/01/09"); got != "1999-01-09" {
		t.Errorf("NormalizeDate = %q", got)
	}
}

func TestTransformAlakazam(t *testing.T) {
	c := Transform(mustDecode(t, alakazamJSON), setBase1)
	if c.ID != "base1-1" || c.Name != "Alakazam" || c.Supertype != "Pokémon" {
		t.Errorf("identity: %+v", c)
	}
	if c.HP == nil || *c.HP != 80 {
		t.Errorf("hp: want *80, got %v", c.HP)
	}
	if c.EvolvesFrom != "Kadabra" {
		t.Errorf("evolves_from: %q", c.EvolvesFrom)
	}
	a := c.Attacks[0]
	if a.Name != "Confuse Ray" || a.ConvertedCost != 3 || a.Damage != "30" || a.DamageValue != 30 {
		t.Errorf("attack: %+v", a)
	}
	if c.RetreatCost != 3 || c.SetID != "base1" || c.SetName != "Base" || c.SetSeries != "Base" || c.SetTotal != 102 {
		t.Errorf("set join: %+v", c)
	}
	if c.ReleaseDate != "1999-01-09" {
		t.Errorf("release_date: %q", c.ReleaseDate)
	}
	if c.ImageSmall != "https://images.pokemontcg.io/base1/1.png" || c.ImageLarge != "https://images.pokemontcg.io/base1/1_hires.png" {
		t.Errorf("images: %q %q", c.ImageSmall, c.ImageLarge)
	}
}

func TestTransformMewtwoDelta(t *testing.T) {
	c := Transform(mustDecode(t, mewtwoDeltaJSON), setEx11)
	if c.Name != "Mewtwo δ" {
		t.Errorf("δ name mangled: %q", c.Name)
	}
	if len(c.Types) != 2 || c.Types[0] != "Fire" || c.Types[1] != "Metal" {
		t.Errorf("types: %v", c.Types)
	}
	if c.Attacks[0].Damage != "10×" || c.Attacks[0].DamageValue != 10 {
		t.Errorf("×-damage: %+v", c.Attacks[0])
	}
	if c.FlavorText != "" {
		t.Errorf("flavor must be empty: %q", c.FlavorText)
	}
	if c.ReleaseDate != "2005-10-31" {
		t.Errorf("release_date: %q", c.ReleaseDate)
	}
}

func TestTransformUmbreonStar(t *testing.T) {
	c := Transform(mustDecode(t, umbreonStarJSON), setCel25c)
	if c.ID != "cel25c-17_A" || c.Name != "Umbreon ★" {
		t.Errorf("★ identity: %q %q", c.ID, c.Name)
	}
	if c.Attacks[0].Damage != "" || c.Attacks[0].DamageValue != 0 {
		t.Errorf("empty damage: %+v", c.Attacks[0])
	}
	if len(c.Resistances) != 1 || c.Resistances[0].Value != "-30" {
		t.Errorf("resistances: %+v", c.Resistances)
	}
}

func TestTransformClefairyDollTrainer(t *testing.T) {
	c := Transform(mustDecode(t, clefairyDollJSON), setBase1)
	if c.Supertype != "Trainer" {
		t.Errorf("supertype: %q", c.Supertype)
	}
	if c.HP == nil || *c.HP != 10 {
		t.Errorf("trainer hp must parse: %v", c.HP)
	}
	if c.Types != nil || c.Attacks != nil || c.FlavorText != "" || c.NationalPokedexNumbers != nil {
		t.Errorf("absent fields must stay zero: %+v", c)
	}
}

func TestTransformEnergyNoHP(t *testing.T) {
	c := Transform(mustDecode(t, dceJSON), setBase1)
	if c.HP != nil {
		t.Errorf("hp must be nil (omitted), got %v", c.HP)
	}
	// Marshal check: absent hp must not appear in JSON at all.
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"hp", "types", "attacks", "abilities", "flavor_text", "national_pokedex_numbers", "evolves_from"} {
		if _, ok := m[absent]; ok {
			t.Errorf("field %q must be omitted from JSON when empty", absent)
		}
	}
	for _, present := range []string{"id", "name", "supertype", "number", "set_id", "set_name", "set_series", "set_total", "release_date", "image_small", "image_large"} {
		if _, ok := m[present]; !ok {
			t.Errorf("field %q missing from JSON", present)
		}
	}
}
