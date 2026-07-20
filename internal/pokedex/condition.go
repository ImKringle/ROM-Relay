package pokedex

import (
	"fmt"
	"strings"
)

// Condition renders one evolution requirement as a sentence a person can read.
//
// PokeAPI describes a requirement as a dozen mostly-null fields; turning that
// into prose is done once here, at ingest, rather than in the UI on every render.
func Condition(d Detail) string {
	if d.Trigger == nil {
		return "Special condition"
	}
	var base string
	switch d.Trigger.Name {
	case "level-up":
		base = "Level up"
		if d.MinLevel > 0 {
			base = fmt.Sprintf("Level %d", d.MinLevel)
		}
	case "use-item":
		if d.Item != nil {
			return appendWhen("Use a "+title(d.Item.Name), d)
		}
		base = "Use an item"
	case "trade":
		if d.HeldItem != nil {
			return appendWhen("Trade holding a "+title(d.HeldItem.Name), d)
		}
		if d.TradeSpecies != nil {
			return appendWhen("Trade for "+title(d.TradeSpecies.Name), d)
		}
		return appendWhen("Trade", d)
	case "shed":
		return "With an empty party slot and a Poké Ball"
	case "":
		return "Special condition"
	default:
		base = title(d.Trigger.Name)
	}
	return appendWhen(base, d)
}

// appendWhen adds the qualifiers that can accompany any trigger, in the order
// they read most naturally.
func appendWhen(base string, d Detail) string {
	var parts []string
	if d.MinHappiness > 0 {
		parts = append(parts, "with high friendship")
	}
	if d.MinAffection > 0 {
		parts = append(parts, "with high affection")
	}
	if d.MinBeauty > 0 {
		parts = append(parts, "with high beauty")
	}
	if d.KnownMove != nil {
		parts = append(parts, "knowing "+title(d.KnownMove.Name))
	}
	if d.HeldItem != nil && d.Trigger.Name != "trade" {
		parts = append(parts, "holding a "+title(d.HeldItem.Name))
	}
	if d.Location != nil {
		parts = append(parts, "at "+title(d.Location.Name))
	}
	if d.NeedsOverworldRain {
		parts = append(parts, "while it is raining")
	}
	if d.TurnUpsideDown {
		parts = append(parts, "holding the console upside down")
	}
	switch d.TimeOfDay {
	case "day":
		parts = append(parts, "during the day")
	case "night":
		parts = append(parts, "at night")
	case "dusk":
		parts = append(parts, "at dusk")
	}
	if d.Gender != nil {
		switch *d.Gender {
		case 1:
			parts = append(parts, "female only")
		case 2:
			parts = append(parts, "male only")
		}
	}
	if len(parts) == 0 {
		return base
	}
	// "Level up" reads badly with a qualifier bolted on ("Level up with high
	// friendship" is right, but "Level up at night" loses the "with").
	return base + " " + strings.Join(parts, " ")
}

// title turns PokeAPI's kebab-case names into display text: "sun-stone" ->
// "Sun Stone", "mt-coronet" -> "Mt Coronet".
func title(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
