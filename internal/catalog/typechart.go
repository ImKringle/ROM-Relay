package catalog

import "strings"

// typeChart is the standard Gen VI+ type effectiveness table: attacking type ->
// defending type -> damage multiplier. A pair missing from the inner map is
// neutral (x1), which is why only the exceptions are listed.
var typeChart = map[string]map[string]float64{
	"normal":   {"rock": 0.5, "ghost": 0, "steel": 0.5},
	"fire":     {"fire": 0.5, "water": 0.5, "grass": 2, "ice": 2, "bug": 2, "rock": 0.5, "dragon": 0.5, "steel": 2},
	"water":    {"fire": 2, "water": 0.5, "grass": 0.5, "ground": 2, "rock": 2, "dragon": 0.5},
	"electric": {"water": 2, "electric": 0.5, "grass": 0.5, "ground": 0, "flying": 2, "dragon": 0.5},
	"grass":    {"fire": 0.5, "water": 2, "grass": 0.5, "poison": 0.5, "ground": 2, "flying": 0.5, "bug": 0.5, "rock": 2, "dragon": 0.5, "steel": 0.5},
	"ice":      {"fire": 0.5, "water": 0.5, "grass": 2, "ice": 0.5, "ground": 2, "flying": 2, "dragon": 2, "steel": 0.5},
	"fighting": {"normal": 2, "ice": 2, "poison": 0.5, "flying": 0.5, "psychic": 0.5, "bug": 0.5, "rock": 2, "ghost": 0, "dark": 2, "steel": 2, "fairy": 0.5},
	"poison":   {"grass": 2, "poison": 0.5, "ground": 0.5, "rock": 0.5, "ghost": 0.5, "steel": 0, "fairy": 2},
	"ground":   {"fire": 2, "electric": 2, "grass": 0.5, "poison": 2, "flying": 0, "bug": 0.5, "rock": 2, "steel": 2},
	"flying":   {"electric": 0.5, "grass": 2, "fighting": 2, "bug": 2, "rock": 0.5, "steel": 0.5},
	"psychic":  {"fighting": 2, "poison": 2, "psychic": 0.5, "dark": 0, "steel": 0.5},
	"bug":      {"fire": 0.5, "grass": 2, "fighting": 0.5, "poison": 0.5, "flying": 0.5, "psychic": 2, "ghost": 0.5, "dark": 2, "steel": 0.5, "fairy": 0.5},
	"rock":     {"fire": 2, "ice": 2, "fighting": 0.5, "ground": 0.5, "flying": 2, "bug": 2, "steel": 0.5},
	"ghost":    {"normal": 0, "psychic": 2, "ghost": 2, "dark": 0.5},
	"dragon":   {"dragon": 2, "steel": 0.5, "fairy": 0},
	"dark":     {"fighting": 0.5, "psychic": 2, "ghost": 2, "dark": 0.5, "fairy": 0.5},
	"steel":    {"fire": 0.5, "water": 0.5, "electric": 0.5, "ice": 2, "rock": 2, "steel": 0.5, "fairy": 2},
	"fairy":    {"fire": 0.5, "fighting": 2, "poison": 0.5, "dragon": 2, "dark": 2, "steel": 0.5},
}

// AllTypes lists every type name in the chart's canonical presentation order,
// the order type charts and type-select menus have used since Gen VI.
var AllTypes = []string{
	"normal", "fire", "water", "electric", "grass", "ice", "fighting", "poison", "ground",
	"flying", "psychic", "bug", "rock", "ghost", "dragon", "dark", "steel", "fairy",
}

// TypeMatchups computes, for a species carrying one or two types (as stored in
// Species.Types, "fire/flying"), the damage multiplier it takes from each of
// the 18 attacking types. Dual types combine multiplicatively, a Fire/Flying
// species takes x4 from Rock because both of its types are individually weak
// to it, which is exactly how battle damage actually resolves.
func TypeMatchups(types string) map[string]float64 {
	defending := strings.Split(types, "/")
	out := make(map[string]float64, len(AllTypes))
	for _, atk := range AllTypes {
		mult := 1.0
		for _, def := range defending {
			def = strings.TrimSpace(def)
			if def == "" {
				continue
			}
			if m, ok := typeChart[atk][def]; ok {
				mult *= m
			}
		}
		out[atk] = mult
	}
	return out
}
