package pokedex

import (
	"context"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"romrelay/internal/catalog"
)

// MaxDex is the National Pokédex as PKHeX and PokeAPI currently know it.
const MaxDex = 1025

// Options configures an Ingest. Progress is optional and reports into the
// Active Jobs panel.
type Options struct {
	Client   *Client
	Progress func(done, total int)
}

// Ingest fills the local Pokédex reference tables from PokeAPI, once.
//
// It is deliberately sequential: this is a one-time background fill of static
// data against a free public API, so being polite matters more than being quick.
type Ingest struct {
	store *catalog.Store
	opts  Options
}

func New(store *catalog.Store, opts Options) *Ingest {
	if opts.Client == nil {
		opts.Client = NewClient("")
	}
	return &Ingest{store: store, opts: opts}
}

// Run walks species 1..maxDex, storing each one it does not already have. It is
// resumable: an interrupted run picks up where it stopped, because a species is
// skipped once stored.
//
// A live PokeAPI client stays sequential, one dex at a time, out of courtesy to
// a free public API. A local api-data checkout has no such concern - it is
// disk and CPU bound - so that path fans out across a worker pool instead,
// the same shape catalog.Store.HashPending uses for hashing a ROM library.
func (i *Ingest) Run(ctx context.Context, maxDex int) (stored int, err error) {
	if maxDex <= 0 {
		maxDex = MaxDex
	}
	if i.opts.Client.IsLocal() {
		return i.runParallel(ctx, maxDex)
	}
	return i.runSequential(ctx, maxDex)
}

func (i *Ingest) runSequential(ctx context.Context, maxDex int) (stored int, err error) {
	chains := newChainSet()
	for dex := 1; dex <= maxDex; dex++ {
		if err := ctx.Err(); err != nil {
			return stored, err
		}
		i.report(dex, maxDex)
		ok, err := i.ingestOne(ctx, dex, chains)
		if err != nil {
			return stored, err
		}
		if ok {
			stored++
		}
	}
	i.report(maxDex, maxDex)
	return stored, nil
}

func (i *Ingest) runParallel(ctx context.Context, maxDex int) (stored int, err error) {
	chains := newChainSet()
	// GOMAXPROCS, not NumCPU: see catalog.HashPending's identical worker-pool
	// sizing comment, this stays correct under a container CPU quota.
	workers := runtime.GOMAXPROCS(0)
	if workers > maxDex {
		workers = maxDex
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var done int
	var firstErr error
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dex := range jobs {
				ok, dexErr := i.ingestOne(ctx, dex, chains)
				mu.Lock()
				done++
				i.report(done, maxDex)
				if ok {
					stored++
				}
				if dexErr != nil && firstErr == nil {
					firstErr = dexErr
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for dex := 1; dex <= maxDex; dex++ {
		select {
		case jobs <- dex:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return stored, firstErr
	}
	return stored, ctx.Err()
}

// chainSet tracks which evolution chain URLs have been walked this run, safe
// for concurrent claims from runParallel's worker pool.
type chainSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func newChainSet() *chainSet { return &chainSet{m: map[string]bool{}} }

// claim reports whether this call is the first to see url, atomically marking
// it walked either way.
func (c *chainSet) claim(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[url] {
		return false
	}
	c.m[url] = true
	return true
}

// ingestOne fetches and stores everything for a single dex: encounters,
// learnset, species/pokemon detail, its evolution chain (once per family), and
// regional numbers. It reports whether a new species row was stored, for the
// caller's running total.
func (i *Ingest) ingestOne(ctx context.Context, dex int, chains *chainSet) (bool, error) {
	// One round trip in place of three: on an already-complete Pokédex (every
	// run after the first, including the hourly retry), this status check is
	// the entire cost of visiting a dex, so collapsing it matters a lot more
	// than collapsing an equivalent check on the ingest's slow path.
	encountersDone, learnsetDone, speciesDone, err := i.store.DexStatus(ctx, dex)
	if err != nil {
		return false, err
	}

	// Encounters and the learnset live outside the species-row resumability
	// check: species stored before either existed still need these passes,
	// and each keeps its own done-marker because "no rows" is a real answer
	// (an evolve-only species has no wild encounters; a move-tutor-only kit
	// has no level-up or machine moves). A failed fetch skips without
	// marking, so the hourly retry repairs it.
	if !encountersDone {
		if err := i.ingestEncounters(ctx, dex); err != nil {
			return false, err
		}
	}
	if !learnsetDone {
		if err := i.ingestLearnset(ctx, dex); err != nil {
			return false, err
		}
	}

	// HatchCounter, StatHP, and Abilities, not just row presence, gate the
	// skip: every real species has a hatch counter (PokeAPI's minimum is 5),
	// a nonzero HP stat, and at least one non-hidden ability, so a species
	// stored before breeding, stat, or ability data existed reads as one of
	// them empty/zero and is correctly re-fetched rather than silently left
	// without it forever.
	if speciesDone {
		return false, nil
	}

	species, err := i.opts.Client.Species(ctx, dex)
	if err != nil {
		// A gap in the National dex is not an error worth abandoning the run
		// for, but a cancelled context is.
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	pokemon, err := i.opts.Client.Pokemon(ctx, dex)
	if err != nil {
		// Same treatment as a failed Species() fetch: a cancelled context
		// aborts the run, anything else just skips this species. A species
		// is either stored complete or not stored at all - never half-filled,
		// since resumability only checks that the row exists.
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}

	// The species row is written last, once everything else for this dex
	// has been stored, so "the row exists" honestly implies "everything for
	// this dex was stored". pokedex_evolution and pokedex_regional have no
	// foreign key to pokedex_species, and all three upserts are idempotent,
	// so a cancelled run simply rewrites them on resume.

	// One chain covers a whole family, so it is walked once rather than once
	// per member.
	if url := species.EvolutionChain.URL; url != "" && chains.claim(url) {
		chain, err := i.opts.Client.EvolutionChain(ctx, url)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
		if err := i.storeChain(ctx, chain.Chain); err != nil {
			return false, err
		}
	}

	for _, p := range species.PokedexNumbers {
		if p.Pokedex.Name == "" {
			continue
		}
		if err := i.store.UpsertRegionalNumber(ctx, catalog.RegionalNumber{
			Dex: dex, Pokedex: p.Pokedex.Name, Entry: p.EntryNumber,
		}); err != nil {
			return false, err
		}
	}

	abilities, hidden := pokemon.AbilityNames()
	if err := i.store.UpsertSpecies(ctx, catalog.Species{
		Dex: dex, Name: species.Name, Genus: species.EnglishGenus(),
		Generation: species.Generation.Name, Types: pokemon.TypeList(),
		HeightDM: pokemon.Height, WeightHG: pokemon.Weight,
		FlavorText:  species.EnglishFlavorText(),
		IsLegendary: species.IsLegendary, IsMythical: species.IsMythical,
		EggGroups: species.EggGroupNames(), GenderRate: species.GenderRate,
		HatchCounter: species.HatchCounter,
		StatHP:       pokemon.BaseStat("hp"),
		StatAtk:      pokemon.BaseStat("attack"),
		StatDef:      pokemon.BaseStat("defense"),
		StatSpA:      pokemon.BaseStat("special-attack"),
		StatSpD:      pokemon.BaseStat("special-defense"),
		StatSpe:      pokemon.BaseStat("speed"),
		Abilities:    abilities, HiddenAbility: hidden,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// ingestEncounters pulls one species' wild encounters and stores a row per
// (version, location, method, conditions), aggregating level range and best
// chance across the raw slots. The caller (ingestOne) has already checked
// this pass is not done yet. Returns nil on a fetch failure (retried next
// run); returns the error on a store failure or cancellation.
func (i *Ingest) ingestEncounters(ctx context.Context, dex int) error {
	areas, err := i.opts.Client.Encounters(ctx, dex)
	if err != nil {
		return ctx.Err() // nil unless cancelled: a failed fetch just waits for the retry
	}
	type key struct{ version, location, method, conditions string }
	merged := map[key]*catalog.Encounter{}
	for _, area := range areas {
		for _, vd := range area.VersionDetails {
			for _, d := range vd.EncounterDetails {
				names := make([]string, 0, len(d.ConditionValues))
				for _, c := range d.ConditionValues {
					names = append(names, c.Name)
				}
				k := key{vd.Version.Name, area.LocationArea.Name, d.Method.Name, strings.Join(names, ", ")}
				e := merged[k]
				if e == nil {
					e = &catalog.Encounter{Dex: dex, Version: k.version, Location: k.location, Method: k.method, Conditions: k.conditions, MinLevel: d.MinLevel, MaxLevel: d.MaxLevel, Chance: d.Chance}
					merged[k] = e
					continue
				}
				if d.MinLevel > 0 && (e.MinLevel == 0 || d.MinLevel < e.MinLevel) {
					e.MinLevel = d.MinLevel
				}
				if d.MaxLevel > e.MaxLevel {
					e.MaxLevel = d.MaxLevel
				}
				if d.Chance > e.Chance {
					e.Chance = d.Chance
				}
			}
		}
	}
	for _, e := range merged {
		if err := i.store.UpsertEncounter(ctx, *e); err != nil {
			return err
		}
	}
	return i.store.MarkEncountersFetched(ctx, dex)
}

// generationOf maps a PokeAPI version-group name to its generation number.
// Hardcoded rather than fetched: it is franchise-fixed data (a version group
// never moves to a different generation), so ingesting it as if it might
// change would just be a slower way to store a constant. Entries PokeAPI
// carries for non-mainline or unreleased contexts (event distributions, an
// announced future title) are included too, an unrecognised name simply
// contributes no generation to a move's set, which is the correct behaviour.
var generationOf = map[string]int{
	"red-blue": 1, "yellow": 1, "red-green-japan": 1, "blue-japan": 1,
	"gold-silver": 2, "crystal": 2,
	"ruby-sapphire": 3, "emerald": 3, "firered-leafgreen": 3, "colosseum": 3, "xd": 3,
	"diamond-pearl": 4, "platinum": 4, "heartgold-soulsilver": 4,
	"black-white": 5, "black-2-white-2": 5,
	"x-y": 6, "omega-ruby-alpha-sapphire": 6,
	"sun-moon": 7, "ultra-sun-ultra-moon": 7, "lets-go-pikachu-lets-go-eevee": 7,
	"sword-shield": 8, "the-isle-of-armor": 8, "the-crown-tundra": 8,
	"brilliant-diamond-shining-pearl": 8, "legends-arceus": 8,
	"scarlet-violet": 9, "the-teal-mask": 9, "the-indigo-disk": 9,
}

// ingestLearnset pulls one species' level-up and machine (TM/HM) moves. Egg
// and tutor moves are skipped entirely, the request this serves asked for
// level-up and TM only, and skipping them also means their move reference
// data is never fetched, which is most of the ingest's request volume. The
// caller (ingestOne) has already checked this pass is not done yet.
func (i *Ingest) ingestLearnset(ctx context.Context, dex int) error {
	pokemon, err := i.opts.Client.Pokemon(ctx, dex)
	if err != nil {
		return ctx.Err() // nil unless cancelled: a failed fetch just waits for the retry
	}
	for _, mv := range pokemon.Moves {
		byMethod := map[string]struct {
			level int
			gens  map[int]bool
		}{}
		for _, d := range mv.VersionGroupDetails {
			method := d.MoveLearnMethod.Name
			if method != "level-up" && method != "machine" {
				continue
			}
			gen := generationOf[d.VersionGroup.Name]
			if gen == 0 {
				continue
			}
			bucket := byMethod[method]
			if bucket.gens == nil {
				bucket.gens = map[int]bool{}
			}
			bucket.gens[gen] = true
			if method == "level-up" && d.LevelLearnedAt > 0 && (bucket.level == 0 || d.LevelLearnedAt < bucket.level) {
				bucket.level = d.LevelLearnedAt
			}
			byMethod[method] = bucket
		}
		if len(byMethod) == 0 {
			continue // egg/tutor-only move: not what this ingest tracks
		}
		if err := i.ensureMove(ctx, mv.Move); err != nil {
			return err
		}
		for method, bucket := range byMethod {
			if err := i.store.UpsertLearnsetEntry(ctx, dex, catalog.LearnsetEntry{
				Move: mv.Move.Name, Method: method, Level: bucket.level, Generations: generationList(bucket.gens),
			}); err != nil {
				return err
			}
		}
	}
	return i.store.MarkLearnsetFetched(ctx, dex)
}

// ensureMove fetches and stores a move's own reference data exactly once, on
// whichever species first references it: ~900 distinct moves are shared
// across 1,025 species, so fetching per species would repeat almost all of it.
func (i *Ingest) ensureMove(ctx context.Context, move named) error {
	has, err := i.store.HasMove(ctx, move.Name)
	if err != nil || has {
		return err
	}
	detail, err := i.opts.Client.MoveByURL(ctx, move.URL)
	if err != nil {
		return ctx.Err()
	}
	return i.store.UpsertMove(ctx, catalog.Move{
		Name: move.Name, Type: detail.Type.Name, Category: detail.DamageClass.Name,
		Power: detail.Power, Accuracy: detail.Accuracy,
	})
}

// generationList is a set of generation numbers as a sorted, comma-joined
// string ("1,3,6,9"), the form the learnset row and the API response share.
func generationList(gens map[int]bool) string {
	nums := make([]int, 0, len(gens))
	for g := range gens {
		nums = append(nums, g)
	}
	sort.Ints(nums)
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// storeChain walks the nested chain, writing one row per edge.
func (i *Ingest) storeChain(ctx context.Context, link ChainLink) error {
	from := dexFromURL(link.Species.URL)
	for _, next := range link.EvolvesTo {
		to := dexFromURL(next.Species.URL)
		if from > 0 && to > 0 {
			for _, d := range next.EvolutionDetails {
				var triggerName string
				if d.Trigger != nil {
					triggerName = d.Trigger.Name
				}
				e := catalog.Evolution{
					FromDex: from, ToDex: to, Trigger: triggerName,
					MinLevel: d.MinLevel, Condition: Condition(d),
				}
				if d.Item != nil {
					e.Item = d.Item.Name
				}
				if err := i.store.UpsertEvolution(ctx, e); err != nil {
					return err
				}
				break // one row per edge: the first detail is the canonical one
			}
		}
		if err := i.storeChain(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

// dexFromURL pulls 182 out of ".../pokemon-species/182/". The chain refers to
// species by URL only.
func dexFromURL(url string) int {
	trimmed := strings.Trim(url, "/")
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return 0
	}
	n, err := strconv.Atoi(trimmed[slash+1:])
	if err != nil {
		return 0
	}
	return n
}

func (i *Ingest) report(done, total int) {
	if i.opts.Progress != nil {
		i.opts.Progress(done, total)
	}
}
