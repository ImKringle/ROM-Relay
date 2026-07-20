package pokemon

// Ball catalog and nature stat effects: fixed, game-defined tables that never
// change between PKHeX versions, so they are static data here rather than
// another sidecar round trip. Ball IDs are PKHeX's own Ball enum, Poké Ball
// is 4, matching the default this package's callers already assumed before
// this table existed.
type Ball struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

var ballList = []Ball{
	{1, "Master Ball"}, {2, "Ultra Ball"}, {3, "Great Ball"}, {4, "Poké Ball"},
	{5, "Safari Ball"}, {6, "Net Ball"}, {7, "Dive Ball"}, {8, "Nest Ball"},
	{9, "Repeat Ball"}, {10, "Timer Ball"}, {11, "Luxury Ball"}, {12, "Premier Ball"},
	{13, "Dusk Ball"}, {14, "Heal Ball"}, {15, "Quick Ball"}, {16, "Cherish Ball"},
	{17, "Fast Ball"}, {18, "Level Ball"}, {19, "Lure Ball"}, {20, "Heavy Ball"},
	{21, "Love Ball"}, {22, "Friend Ball"}, {23, "Moon Ball"}, {24, "Sport Ball"},
	{25, "Dream Ball"}, {26, "Beast Ball"},
}

// Balls lists every catchable-game Poké Ball, in PKHeX's own enum order.
func Balls() []Ball { return ballList }

// NatureEffect is one of the 25 natures with the stat pair it shifts,
// Effect is a ready-to-display "+Stat / -Stat" (or "Neutral" for the five
// natures that raise and lower the same stat, which cancels out).
type NatureEffect struct {
	Name   string `json:"name"`
	Effect string `json:"effect"`
}

var natureEffectList = []NatureEffect{
	{"Hardy", "Neutral"}, {"Lonely", "+Atk / -Def"}, {"Brave", "+Atk / -Spe"},
	{"Adamant", "+Atk / -SpA"}, {"Naughty", "+Atk / -SpD"}, {"Bold", "+Def / -Atk"},
	{"Docile", "Neutral"}, {"Relaxed", "+Def / -Spe"}, {"Impish", "+Def / -SpA"},
	{"Lax", "+Def / -SpD"}, {"Timid", "+Spe / -Atk"}, {"Hasty", "+Spe / -Def"},
	{"Serious", "Neutral"}, {"Jolly", "+Spe / -SpA"}, {"Naive", "+Spe / -SpD"},
	{"Modest", "+SpA / -Atk"}, {"Mild", "+SpA / -Def"}, {"Quiet", "+SpA / -Spe"},
	{"Bashful", "Neutral"}, {"Rash", "+SpA / -SpD"}, {"Calm", "+SpD / -Atk"},
	{"Gentle", "+SpD / -Def"}, {"Sassy", "+SpD / -Spe"}, {"Careful", "+SpD / -SpA"},
	{"Quirky", "Neutral"},
}

// NatureEffects lists all 25 natures with their stat shift, in the same
// order the editor's existing NATURES list already used.
func NatureEffects() []NatureEffect { return natureEffectList }
