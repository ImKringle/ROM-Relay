package pokedex

import "testing"

func item(name string) *named { return &named{Name: name} }

func TestConditionReadsAsASentence(t *testing.T) {
	cases := []struct {
		name string
		in   Detail
		want string
	}{
		{"plain level", Detail{Trigger: item("level-up"), MinLevel: 21}, "Level 21"},
		{"stone", Detail{Trigger: item("use-item"), Item: item("sun-stone")}, "Use a Sun Stone"},
		{"plain trade", Detail{Trigger: item("trade")}, "Trade"},
		{"trade holding", Detail{Trigger: item("trade"), HeldItem: item("metal-coat")}, "Trade holding a Metal Coat"},
		{"friendship", Detail{Trigger: item("level-up"), MinHappiness: 220}, "Level up with high friendship"},
		{"friendship at night", Detail{Trigger: item("level-up"), MinHappiness: 220, TimeOfDay: "night"}, "Level up with high friendship at night"},
		{"level at day", Detail{Trigger: item("level-up"), MinLevel: 37, TimeOfDay: "day"}, "Level 37 during the day"},
		{"known move", Detail{Trigger: item("level-up"), KnownMove: item("ancient-power")}, "Level up knowing Ancient Power"},
		{"location", Detail{Trigger: item("level-up"), MinLevel: 20, Location: item("mt-coronet")}, "Level 20 at Mt Coronet"},
		{"upside down", Detail{Trigger: item("level-up"), MinLevel: 30, TurnUpsideDown: true}, "Level 30 holding the console upside down"},
		// An unmapped trigger must still say something truthful rather than "".
		{"unknown trigger", Detail{Trigger: item("spin")}, "Spin"},
		{"nothing at all", Detail{}, "Special condition"},
	}
	for _, tc := range cases {
		if got := Condition(tc.in); got != tc.want {
			t.Errorf("%s: Condition() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
