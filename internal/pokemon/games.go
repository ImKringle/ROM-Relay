package pokemon

import "sort"

// GameName turns PKHeX's GameVersion code into the name on the box.
//
// The sidecar reports the save's game as PKHeX's enum name, "HG", "B2", "E",
// which is precise and unreadable. It reached the Dex tabs and the Save Data list
// verbatim, so the UI asked people to recognise "HG" as HeartGold.
//
// An unknown code is returned unchanged: a future PKHeX may know a game this
// table does not, and showing its code beats showing nothing.
func GameName(code string) string {
	return GameInfo(code).Name
}

// Game is the reference data ROM Relay knows about one main-series game: where
// it is set, which generation it belongs to, what it runs on, how far its
// National Pokédex extends, and which PokeAPI regional Pokédex(es) describe its
// own dex. RegionalDexes is empty for a game with no regional dex data
// (Colosseum/XD); Kalos legitimately has three.
type Game struct {
	Code       string
	Name       string
	Region     string
	Generation int
	Platform   string
	// GenMax is the last National dex number the game's generation knows,
	// the honest ceiling for a "National" view of that game when the save never
	// recorded its own range.
	GenMax int
	// RegionalDexes are PokeAPI pokedex names, in display order.
	RegionalDexes []string
	// Versions are the PokeAPI version names this game answers to, for
	// encounter lookups ("heartgold", "black-2").
	Versions []string
}

// GameInfo returns the reference data for a PKHeX game code. Unknown codes
// return a Game carrying only the code as its name, so a future PKHeX game
// degrades to what the old GameName did rather than disappearing.
func GameInfo(code string) Game {
	if g, ok := games[code]; ok {
		// The table keys are the codes; carry the key on the value so callers
		// that keep a []Game can still identify each entry.
		g.Code = code
		return g
	}
	return Game{Code: code, Name: code}
}

// regionByVersion maps a PokeAPI version name ("heartgold") to its game's
// region ("Johto"), built once from the games table rather than duplicated:
// the two are the same fact, and a Game already carries both.
var regionByVersion = func() map[string]string {
	out := map[string]string{}
	for _, g := range games {
		for _, v := range g.Versions {
			out[v] = g.Region
		}
	}
	return out
}()

// RegionForVersion is the region an encounter's PokeAPI version name belongs
// to, for grouping "where to find it" results by region rather than by every
// individual game release. An unrecognised version returns "".
func RegionForVersion(version string) string {
	return regionByVersion[version]
}

// NamedCode pairs a game code with its box name, for callers that match titles.
type NamedCode struct {
	Code string
	Name string
}

// GameNamesByLength lists every known game, longest name first. Matching titles
// in this order is what disambiguates names that prefix each other ("Black 2"
// before "Black", "FireRed" before "Red").
func GameNamesByLength() []NamedCode {
	out := make([]NamedCode, 0, len(games))
	for code, g := range games {
		out = append(out, NamedCode{Code: code, Name: g.Name})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Name) != len(out[j].Name) {
			return len(out[i].Name) > len(out[j].Name)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// games covers the main-series games PKHeX can read a save for. Spin-offs and
// side games are omitted deliberately: they have no save this parses.
var games = map[string]Game{
	// Gen 1
	"RD": {Versions: []string{"red"}, Name: "Red", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	"BU": {Versions: []string{"blue"}, Name: "Blue", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	"GN": {Versions: []string{"blue"}, Name: "Green", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	"YW": {Versions: []string{"yellow"}, Name: "Yellow", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	// Gen 2
	"GD": {Versions: []string{"gold"}, Name: "Gold", Region: "Johto", Generation: 2, Platform: "Game Boy Color", GenMax: 251, RegionalDexes: []string{"original-johto"}},
	"SI": {Versions: []string{"silver"}, Name: "Silver", Region: "Johto", Generation: 2, Platform: "Game Boy Color", GenMax: 251, RegionalDexes: []string{"original-johto"}},
	"C":  {Versions: []string{"crystal"}, Name: "Crystal", Region: "Johto", Generation: 2, Platform: "Game Boy Color", GenMax: 251, RegionalDexes: []string{"original-johto"}},
	// Gen 3
	"R":   {Versions: []string{"ruby"}, Name: "Ruby", Region: "Hoenn", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"hoenn"}},
	"S":   {Versions: []string{"sapphire"}, Name: "Sapphire", Region: "Hoenn", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"hoenn"}},
	"E":   {Versions: []string{"emerald"}, Name: "Emerald", Region: "Hoenn", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"hoenn"}},
	"FR":  {Versions: []string{"firered"}, Name: "FireRed", Region: "Kanto", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"kanto"}},
	"LG":  {Versions: []string{"leafgreen"}, Name: "LeafGreen", Region: "Kanto", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"kanto"}},
	"CXD": {Versions: []string{"colosseum", "xd"}, Name: "Colosseum / XD", Region: "Orre", Generation: 3, Platform: "GameCube", GenMax: 386},
	// Gen 4
	"D":  {Versions: []string{"diamond"}, Name: "Diamond", Region: "Sinnoh", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"original-sinnoh"}},
	"P":  {Versions: []string{"pearl"}, Name: "Pearl", Region: "Sinnoh", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"original-sinnoh"}},
	"Pt": {Versions: []string{"platinum"}, Name: "Platinum", Region: "Sinnoh", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"extended-sinnoh"}},
	"HG": {Versions: []string{"heartgold"}, Name: "HeartGold", Region: "Johto", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"updated-johto"}},
	"SS": {Versions: []string{"soulsilver"}, Name: "SoulSilver", Region: "Johto", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"updated-johto"}},
	// Gen 5
	"B":  {Versions: []string{"black"}, Name: "Black", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"original-unova"}},
	"W":  {Versions: []string{"white"}, Name: "White", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"original-unova"}},
	"B2": {Versions: []string{"black-2"}, Name: "Black 2", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"updated-unova"}},
	"W2": {Versions: []string{"white-2"}, Name: "White 2", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"updated-unova"}},
	// Gen 6
	"X":  {Versions: []string{"x"}, Name: "X", Region: "Kalos", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"kalos-central", "kalos-coastal", "kalos-mountain"}},
	"Y":  {Versions: []string{"y"}, Name: "Y", Region: "Kalos", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"kalos-central", "kalos-coastal", "kalos-mountain"}},
	"OR": {Versions: []string{"omega-ruby"}, Name: "Omega Ruby", Region: "Hoenn", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"updated-hoenn"}},
	"AS": {Versions: []string{"alpha-sapphire"}, Name: "Alpha Sapphire", Region: "Hoenn", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"updated-hoenn"}},
	// Gen 7
	"SN": {Versions: []string{"sun"}, Name: "Sun", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"original-alola"}},
	"MN": {Versions: []string{"moon"}, Name: "Moon", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"original-alola"}},
	"US": {Versions: []string{"ultra-sun"}, Name: "Ultra Sun", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"updated-alola"}},
	"UM": {Versions: []string{"ultra-moon"}, Name: "Ultra Moon", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"updated-alola"}},
	"GP": {Versions: []string{"lets-go-pikachu"}, Name: "Let's Go Pikachu", Region: "Kanto", Generation: 7, Platform: "Nintendo Switch", GenMax: 809, RegionalDexes: []string{"letsgo-kanto"}},
	"GE": {Versions: []string{"lets-go-eevee"}, Name: "Let's Go Eevee", Region: "Kanto", Generation: 7, Platform: "Nintendo Switch", GenMax: 809, RegionalDexes: []string{"letsgo-kanto"}},
	// Gen 8
	"SW":  {Versions: []string{"sword"}, Name: "Sword", Region: "Galar", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"galar", "isle-of-armor", "crown-tundra"}},
	"SH":  {Versions: []string{"shield"}, Name: "Shield", Region: "Galar", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"galar", "isle-of-armor", "crown-tundra"}},
	"BD":  {Versions: []string{"brilliant-diamond"}, Name: "Brilliant Diamond", Region: "Sinnoh", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"original-sinnoh"}},
	"SP":  {Versions: []string{"shining-pearl"}, Name: "Shining Pearl", Region: "Sinnoh", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"original-sinnoh"}},
	"PLA": {Versions: []string{"legends-arceus"}, Name: "Legends: Arceus", Region: "Hisui", Generation: 8, Platform: "Nintendo Switch", GenMax: 905, RegionalDexes: []string{"hisui"}},
	// Gen 9
	"SL": {Versions: []string{"scarlet"}, Name: "Scarlet", Region: "Paldea", Generation: 9, Platform: "Nintendo Switch", GenMax: 1025, RegionalDexes: []string{"paldea", "kitakami", "blueberry"}},
	"VL": {Versions: []string{"violet"}, Name: "Violet", Region: "Paldea", Generation: 9, Platform: "Nintendo Switch", GenMax: 1025, RegionalDexes: []string{"paldea", "kitakami", "blueberry"}},

	// PKHeX also reports lumped GameVersion values for saves it cannot pin to
	// one cartridge of a pair, a Diamond/Pearl save is "DP". Without these a
	// D/P save renders as region "Unknown". Each lump carries both versions so
	// encounter lookups cover the pair.
	"RB":   {Versions: []string{"red", "blue"}, Name: "Red / Blue", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	"RBY":  {Versions: []string{"red", "blue", "yellow"}, Name: "Red / Blue / Yellow", Region: "Kanto", Generation: 1, Platform: "Game Boy", GenMax: 151, RegionalDexes: []string{"kanto"}},
	"GS":   {Versions: []string{"gold", "silver"}, Name: "Gold / Silver", Region: "Johto", Generation: 2, Platform: "Game Boy Color", GenMax: 251, RegionalDexes: []string{"original-johto"}},
	"GSC":  {Versions: []string{"gold", "silver", "crystal"}, Name: "Gold / Silver / Crystal", Region: "Johto", Generation: 2, Platform: "Game Boy Color", GenMax: 251, RegionalDexes: []string{"original-johto"}},
	"RS":   {Versions: []string{"ruby", "sapphire"}, Name: "Ruby / Sapphire", Region: "Hoenn", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"hoenn"}},
	"RSE":  {Versions: []string{"ruby", "sapphire", "emerald"}, Name: "Ruby / Sapphire / Emerald", Region: "Hoenn", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"hoenn"}},
	"FRLG": {Versions: []string{"firered", "leafgreen"}, Name: "FireRed / LeafGreen", Region: "Kanto", Generation: 3, Platform: "Game Boy Advance", GenMax: 386, RegionalDexes: []string{"kanto"}},
	"DP":   {Versions: []string{"diamond", "pearl"}, Name: "Diamond / Pearl", Region: "Sinnoh", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"original-sinnoh"}},
	"DPPt": {Versions: []string{"diamond", "pearl", "platinum"}, Name: "Diamond / Pearl / Platinum", Region: "Sinnoh", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"extended-sinnoh"}},
	"HGSS": {Versions: []string{"heartgold", "soulsilver"}, Name: "HeartGold / SoulSilver", Region: "Johto", Generation: 4, Platform: "Nintendo DS", GenMax: 493, RegionalDexes: []string{"updated-johto"}},
	"BW":   {Versions: []string{"black", "white"}, Name: "Black / White", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"original-unova"}},
	"B2W2": {Versions: []string{"black-2", "white-2"}, Name: "Black 2 / White 2", Region: "Unova", Generation: 5, Platform: "Nintendo DS", GenMax: 649, RegionalDexes: []string{"updated-unova"}},
	"XY":   {Versions: []string{"x", "y"}, Name: "X / Y", Region: "Kalos", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"kalos-central", "kalos-coastal", "kalos-mountain"}},
	"ORAS": {Versions: []string{"omega-ruby", "alpha-sapphire"}, Name: "Omega Ruby / Alpha Sapphire", Region: "Hoenn", Generation: 6, Platform: "Nintendo 3DS", GenMax: 721, RegionalDexes: []string{"updated-hoenn"}},
	"SM":   {Versions: []string{"sun", "moon"}, Name: "Sun / Moon", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"original-alola"}},
	"USUM": {Versions: []string{"ultra-sun", "ultra-moon"}, Name: "Ultra Sun / Ultra Moon", Region: "Alola", Generation: 7, Platform: "Nintendo 3DS", GenMax: 807, RegionalDexes: []string{"updated-alola"}},
	"GG":   {Versions: []string{"lets-go-pikachu", "lets-go-eevee"}, Name: "Let's Go Pikachu / Eevee", Region: "Kanto", Generation: 7, Platform: "Nintendo Switch", GenMax: 809, RegionalDexes: []string{"letsgo-kanto"}},
	"SWSH": {Versions: []string{"sword", "shield"}, Name: "Sword / Shield", Region: "Galar", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"galar", "isle-of-armor", "crown-tundra"}},
	"BDSP": {Versions: []string{"brilliant-diamond", "shining-pearl"}, Name: "Brilliant Diamond / Shining Pearl", Region: "Sinnoh", Generation: 8, Platform: "Nintendo Switch", GenMax: 898, RegionalDexes: []string{"original-sinnoh"}},
	"SV":   {Versions: []string{"scarlet", "violet"}, Name: "Scarlet / Violet", Region: "Paldea", Generation: 9, Platform: "Nintendo Switch", GenMax: 1025, RegionalDexes: []string{"paldea", "kitakami", "blueberry"}},
}
