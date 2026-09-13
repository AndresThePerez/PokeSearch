package tcg

import (
	"math"
	"strconv"
	"strings"
)

// Transform joins one source card with its set metadata and produces the
// document PokéSearch indexes. Pure — no I/O.
func Transform(sc SourceCard, set SourceSet) Card {
	c := Card{
		ID:                     sc.ID,
		Name:                   sc.Name,
		Supertype:              sc.Supertype,
		Subtypes:               sc.Subtypes,
		Types:                  sc.Types,
		EvolvesFrom:            sc.EvolvesFrom,
		RetreatCost:            sc.ConvertedRetreatCost,
		Rarity:                 sc.Rarity,
		Artist:                 sc.Artist,
		FlavorText:             sc.FlavorText,
		NationalPokedexNumbers: sc.NationalPokedexNumbers,
		Number:                 sc.Number,
		SetID:                  set.ID,
		SetName:                set.Name,
		SetSeries:              set.Series,
		SetTotal:               set.Total,
		ReleaseDate:            NormalizeDate(set.ReleaseDate),
		ImageSmall:             sc.Images.Small,
		ImageLarge:             sc.Images.Large,
	}
	if sc.HP != "" {
		if n, err := strconv.Atoi(sc.HP); err == nil {
			c.HP = &n
		}
	}
	for _, a := range sc.Attacks {
		c.Attacks = append(c.Attacks, Attack{
			Name:          a.Name,
			Cost:          a.Cost,
			ConvertedCost: a.ConvertedEnergyCost,
			Damage:        a.Damage,
			DamageValue:   ParseDamage(a.Damage),
			Text:          a.Text,
		})
	}
	for _, ab := range sc.Abilities {
		c.Abilities = append(c.Abilities, Ability(ab))
	}
	for _, w := range sc.Weaknesses {
		c.Weaknesses = append(c.Weaknesses, Value(w))
	}
	for _, r := range sc.Resistances {
		c.Resistances = append(c.Resistances, Value(r))
	}
	return c
}

// ParseDamage returns the leading integer of a printed damage string:
// "30"→30, "10+"→10, "100×"→100, "120-"→120, ""→0.
//
// The result is indexed into attacks.damage_value, an ES integer, so a digit
// run that cannot be one reports 0 — no numeric damage — instead of a
// truncated or overflowed number. Discarding Atoi's error here used to turn a
// long digit run into MaxInt64, which ES rejects at index time and which would
// fail an entire bulk chunk (found by FuzzParseDamage).
func ParseDamage(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil || n > math.MaxInt32 {
		return 0
	}
	return n
}

// NormalizeDate converts the source "1999/01/09" form to ES date "1999-01-09".
func NormalizeDate(s string) string {
	return strings.ReplaceAll(s, "/", "-")
}
