package tcg

// Card is the document indexed into ES and, verbatim, the API result shape
// (Design Spec "Document shape"). Optional fields carry omitempty; hp is a
// pointer so 0 and "absent" stay distinct.
type Card struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	Supertype              string    `json:"supertype"`
	Subtypes               []string  `json:"subtypes,omitempty"`
	HP                     *int      `json:"hp,omitempty"`
	Types                  []string  `json:"types,omitempty"`
	EvolvesFrom            string    `json:"evolves_from,omitempty"`
	Attacks                []Attack  `json:"attacks,omitempty"`
	Abilities              []Ability `json:"abilities,omitempty"`
	Weaknesses             []Value   `json:"weaknesses,omitempty"`
	Resistances            []Value   `json:"resistances,omitempty"`
	RetreatCost            int       `json:"retreat_cost,omitempty"`
	Rarity                 string    `json:"rarity,omitempty"`
	Artist                 string    `json:"artist,omitempty"`
	FlavorText             string    `json:"flavor_text,omitempty"`
	NationalPokedexNumbers []int     `json:"national_pokedex_numbers,omitempty"`
	Number                 string    `json:"number"`
	SetID                  string    `json:"set_id"`
	SetName                string    `json:"set_name"`
	SetSeries              string    `json:"set_series"`
	SetTotal               int       `json:"set_total"`
	ReleaseDate            string    `json:"release_date"` // yyyy-MM-dd
	ImageSmall             string    `json:"image_small"`
	ImageLarge             string    `json:"image_large"`
}

type Attack struct {
	Name          string   `json:"name"`
	Cost          []string `json:"cost,omitempty"`
	ConvertedCost int      `json:"converted_cost"`
	Damage        string   `json:"damage"`       // raw printed string, kept for display
	DamageValue   int      `json:"damage_value"` // leading int of Damage, 0 when none — sortable
	Text          string   `json:"text,omitempty"`
}

type Ability struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// Value is a weakness or resistance entry (display-only).
type Value struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
