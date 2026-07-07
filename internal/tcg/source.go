// Package tcg models the pokemon-tcg-data source format and its
// transformation into the documents Pokesearch indexes.
package tcg

// SourceSet is one record of sets/en.json in the pokemon-tcg-data repo.
// Only fields Pokesearch uses are decoded.
type SourceSet struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Series      string `json:"series"`
	Total       int    `json:"total"`
	ReleaseDate string `json:"releaseDate"` // "1999/01/09"
}

// SourceCard is one card record of cards/en/<set>.json. Fields the site
// neither searches nor displays (legalities, level, rules, evolvesTo,
// ancientTrait, regulationMark, retreatCost list) are not decoded.
type SourceCard struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Supertype              string          `json:"supertype"` // Pokémon | Trainer | Energy
	Subtypes               []string        `json:"subtypes"`
	HP                     string          `json:"hp"` // numeric string when present
	Types                  []string        `json:"types"`
	EvolvesFrom            string          `json:"evolvesFrom"`
	Attacks                []SourceAttack  `json:"attacks"`
	Abilities              []SourceAbility `json:"abilities"`
	Weaknesses             []SourceValue   `json:"weaknesses"`
	Resistances            []SourceValue   `json:"resistances"`
	ConvertedRetreatCost   int             `json:"convertedRetreatCost"`
	Rarity                 string          `json:"rarity"`
	Artist                 string          `json:"artist"`
	FlavorText             string          `json:"flavorText"`
	NationalPokedexNumbers []int           `json:"nationalPokedexNumbers"`
	Number                 string          `json:"number"`
	Images                 SourceImages    `json:"images"`
}

type SourceAttack struct {
	Name                string   `json:"name"`
	Cost                []string `json:"cost"`
	ConvertedEnergyCost int      `json:"convertedEnergyCost"`
	Damage              string   `json:"damage"` // raw printed string: "", "30", "10+", "100×", "120-"
	Text                string   `json:"text"`
}

type SourceAbility struct {
	Name string `json:"name"`
	Type string `json:"type"` // e.g. "Pokémon Power", "Poké-Power", "Ability"
	Text string `json:"text"`
}

// SourceValue is a weakness or resistance entry.
type SourceValue struct {
	Type  string `json:"type"`
	Value string `json:"value"` // e.g. "×2", "-30"
}

type SourceImages struct {
	Small string `json:"small"`
	Large string `json:"large"`
}
