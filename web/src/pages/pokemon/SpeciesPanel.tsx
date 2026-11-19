import { useState, type ReactNode } from "react";
import { api, ApiError, qs } from "../../lib/api";
import { ALL_TYPES, dexNumber, ROMAN, tidy, titleCase } from "../../lib/pokemon";
import { GenderIcon, Pill, Sprite, TypeChip, TypeIcon, TypeTile } from "../../components/PokemonBits";
import { Spinner, useData } from "../../components/ui";

export type DexEntry = { dex: number; name: string; owned?: boolean; shiny?: boolean; regional?: number };

type Move = { move: string; method: string; level?: number; type: string; category?: string; power?: number; accuracy?: number };
type Spot = { version: string; location: string; method: string; min_level?: number; max_level?: number; chance?: number; conditions?: string; region?: string };
type Species = {
  genus?: string;
  flavor_text?: string;
  types?: string;
  egg_groups?: string;
  gender_rate: number;
  hatch_counter?: number;
  stat_hp?: number;
  stat_atk?: number;
  stat_def?: number;
  stat_spa?: number;
  stat_spd?: number;
  stat_spe?: number;
};
type Detail = {
  species: Species;
  evolutions?: { from_dex: number; to_dex: number; condition?: string }[];
  encounters?: Spot[];
  encounter_regions?: string[];
  matchups?: Record<string, number>;
  learnset?: Move[];
  learnset_generation?: number;
  learnset_generations?: number[];
};

const STATS = [
  ["stat_hp", "HP"],
  ["stat_atk", "Atk"],
  ["stat_def", "Def"],
  ["stat_spa", "SpA"],
  ["stat_spd", "SpD"],
  ["stat_spe", "Spe"],
] as const;

// SpeciesPanel: reference data for one species, fetched only when opened.
export function SpeciesPanel({ entry, game, region, names }: { entry: DexEntry; game?: string; region?: string; names: Record<number, string> }) {
  const [gen, setGen] = useState<number | undefined>();
  const [encRegion, setEncRegion] = useState("");
  const d = useData(() => api.get<Detail>(`/api/dex/species/${entry.dex}${qs(game ? { game, gen } : { region, gen })}`), [entry.dex, game, region, gen]);

  const head = (
    <div className="mb-5 flex flex-wrap items-center gap-5">
      <div className="rounded border-2 border-line bg-void p-2">
        <Sprite dex={entry.dex} shiny={entry.owned && entry.shiny} size={112} alt={entry.name} />
      </div>
      <div className="min-w-0">
        <span className="pixel text-[0.6rem] text-muted">
          {entry.regional ? `${dexNumber(entry.regional)} · National ` : ""}
          {dexNumber(entry.dex)}
        </span>
        <h2 className="pixel m-0 mt-2 text-lg capitalize">{entry.name}</h2>
        {d.data?.species.genus && <p className="m-0 mt-1 text-sm text-muted">{d.data.species.genus}</p>}
        <div className="mt-2 flex gap-1.5">
          {(d.data?.species.types ?? "")
            .split("/")
            .filter(Boolean)
            .map((t) => (
              <TypeChip key={t} type={t} />
            ))}
        </div>
      </div>
    </div>
  );
  if (d.loading && !d.data) return (head && <>{head}<Spinner /></>);
  if (d.error) {
    const missing = d.error instanceof ApiError && d.error.status === 404;
    return (
      <>
        {head}
        <p className="text-sm text-muted">{missing ? "Pokédex details are not available. Switch on PokéAPI reference data in Settings to fill them in." : String((d.error as Error).message)}</p>
      </>
    );
  }
  const { species: s, evolutions = [], encounters = [], encounter_regions: regions = [], matchups = {}, learnset = [], learnset_generation: activeGen = 0, learnset_generations: gens = [] } = d.data!;

  const weak = ALL_TYPES.filter((t) => matchups[t] > 1);
  const resist = ALL_TYPES.filter((t) => matchups[t] > 0 && matchups[t] < 1);
  const immune = ALL_TYPES.filter((t) => matchups[t] === 0);
  const bst = STATS.reduce((n, [k]) => n + (s[k] ?? 0), 0);
  const levelMoves = learnset.filter((m) => m.method === "level-up").sort((a, b) => (a.level ?? 0) - (b.level ?? 0));
  const machineMoves = learnset.filter((m) => m.method === "machine").sort((a, b) => a.move.localeCompare(b.move));
  const groups = (s.egg_groups ?? "").split(",").filter(Boolean).map(tidy).map(titleCase).join(" / ") || "Undiscovered";
  const female = s.gender_rate >= 0 ? (s.gender_rate / 8) * 100 : -1;
  const spots = encRegion ? encounters.filter((x) => x.region === encRegion) : encounters;

  return (
    <>
      {head}
      <p className="max-w-3xl text-[0.95rem] leading-7">{s.flavor_text || "No description recorded."}</p>
      <div className="mt-4 grid gap-3">
        <Section title="Type matchups">
          {(
            [
              ["Weak to", "takes extra damage", weak, "weak"],
              ["Resists", "takes less damage", resist, "resist"],
              ["Immune to", "takes none", immune, "immune"],
            ] as const
          ).map(([label, hint, list, state]) => (
            <div key={label} className="mb-3 last:mb-0">
              <div className="mb-1.5 flex items-baseline gap-2">
                <span className="text-sm font-semibold">{label}</span>
                <span className="text-xs text-faint">{hint}</span>
              </div>
              <div className="grid gap-1.5 [grid-template-columns:repeat(auto-fill,minmax(40px,1fr))]">
                {list.length ? list.map((t) => <TypeTile key={t} type={t} mult={matchups[t]} state={state === "weak" && matchups[t] >= 4 ? "quad" : state} />) : <span className="col-span-full text-sm text-faint">None.</span>}
              </div>
            </div>
          ))}
        </Section>
        <Section title="Base stats" glance={bst ? `BST ${bst}` : ""}>
          {bst === 0 ? (
            <p className="m-0 text-sm text-muted">Base stats are not available yet.</p>
          ) : (
            <div className="grid gap-2">
              {STATS.map(([k, label]) => (
                <div key={k} className="grid grid-cols-[2.6rem_1fr_2.4rem] items-center gap-2.5">
                  <span className="pixel text-[0.55rem] text-muted">{label}</span>
                  <div className="h-3 overflow-hidden rounded-sm border-2 border-line bg-void">
                    <div className="h-full bg-cyan" style={{ width: `${Math.min(100, ((s[k] ?? 0) / 255) * 100)}%` }} />
                  </div>
                  <span className="pixel text-right text-[0.6rem]">{s[k] ?? 0}</span>
                </div>
              ))}
            </div>
          )}
        </Section>
        <Section title="Breeding">
          <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
            <Box label="Egg groups">{groups}</Box>
            <Box label="Gender ratio">
              {female < 0 ? (
                "Genderless"
              ) : (
                <span className="inline-flex items-center gap-1">
                  <GenderIcon which="male" /> {(100 - female).toFixed(1)}% <GenderIcon which="female" /> {female.toFixed(1)}%
                </span>
              )}
            </Box>
            <Box label="Egg cycles">{s.hatch_counter ? `${s.hatch_counter} cycles (≈${(s.hatch_counter * 257).toLocaleString()} steps)` : "Unknown"}</Box>
          </div>
        </Section>
        <Section title="Moves" glance={`${ROMAN[activeGen] || activeGen}: ${levelMoves.length} level-up, ${machineMoves.length} TM`}>
          {gens.length > 1 && (
            <div className="mb-3 flex flex-wrap items-center gap-1.5">
              <span className="eyebrow text-[0.5rem] text-faint">Generation</span>
              {gens.map((g) => (
                <Pill key={g} active={g === activeGen} onClick={() => setGen(g)}>
                  {ROMAN[g] || g}
                </Pill>
              ))}
            </div>
          )}
          <p className="mb-1.5 text-sm font-semibold">Level up</p>
          <MoveTable moves={levelMoves} level />
          <p className="mb-1.5 mt-4 text-sm font-semibold">TM / Machine</p>
          <MoveTable moves={machineMoves} />
        </Section>
        <Section title="Evolution" glance={evolutions.length ? evolutions.map((e) => `${names[e.from_dex] || "?"} → ${names[e.to_dex] || "?"}`).join(", ") : "Does not evolve"}>
          {evolutions.length === 0 ? (
            <p className="m-0 text-sm text-muted">This Pokémon does not evolve.</p>
          ) : (
            <Table head={["From", "Into", "How"]} rows={evolutions.map((e) => [<Mini key="f" dex={e.from_dex} name={names[e.from_dex]} />, <Mini key="t" dex={e.to_dex} name={names[e.to_dex]} />, e.condition || "Level up"])} />
          )}
        </Section>
        <Section title="Where to find it" glance={encounters.length ? [...new Set(encounters.map((x) => x.region).filter(Boolean))].join(", ") || `${encounters.length} locations` : "Not found in the wild"}>
          {encounters.length === 0 ? (
            <p className="m-0 text-sm text-muted">{game ? "No wild encounters recorded for this game. Evolve, trade, or transfer it." : "No wild encounters recorded. Pick a game to see per-version locations."}</p>
          ) : (
            <>
              {regions.length > 1 && (
                <div className="mb-3 flex flex-wrap gap-1.5">
                  <Pill active={!encRegion} onClick={() => setEncRegion("")}>
                    All
                  </Pill>
                  {regions.map((r) => (
                    <Pill key={r} active={encRegion === r} onClick={() => setEncRegion(r)}>
                      {r}
                    </Pill>
                  ))}
                </div>
              )}
              <Table
                head={["Version", "Location", "Method", "Level", "Rate", "Conditions"]}
                rows={spots.map((x) => [tidy(x.version), tidy(x.location), tidy(x.method), x.min_level ? (x.min_level === x.max_level ? String(x.min_level) : `${x.min_level}–${x.max_level}`) : "", x.chance ? `${x.chance}%` : "", tidy(x.conditions)])}
              />
            </>
          )}
        </Section>
      </div>
    </>
  );
}

function Section({ title, glance, children }: { title: string; glance?: string; children: ReactNode }) {
  return (
    <details className="panel p-4" open>
      <summary className="flex cursor-pointer flex-wrap items-baseline justify-between gap-2">
        <span className="pixel text-[0.6rem] text-cyan">{title}</span>
        {glance && <span className="text-xs text-muted">{glance}</span>}
      </summary>
      <div className="mt-3">{children}</div>
    </details>
  );
}

function Box({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="rounded border-2 border-line bg-panel-2 px-3 py-1.5">
      <div className="eyebrow text-[0.5rem] text-faint">{label}</div>
      <div className="mt-1 text-sm font-semibold">{children}</div>
    </div>
  );
}

function Mini({ dex, name }: { dex: number; name?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <Sprite dex={dex} size={32} />
      <span className="font-semibold capitalize">{name || "???"}</span>
      <span className="pixel text-[0.5rem] text-faint">{dexNumber(dex)}</span>
    </span>
  );
}

function Table({ head, rows }: { head: string[]; rows: ReactNode[][] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h} className="border-b-2 border-line px-2 py-1.5 text-left text-xs font-bold uppercase text-faint">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="odd:bg-panel-2/40">
              {r.map((c, j) => (
                <td key={j} className="px-2 py-1.5 capitalize">
                  {c}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MoveTable({ moves, level }: { moves: Move[]; level?: boolean }) {
  if (!moves.length) return <p className="m-0 text-sm text-muted">No {level ? "level-up" : "machine"} moves recorded for this generation.</p>;
  return (
    <Table
      head={level ? ["Lv.", "Move", "Cat", "Pwr", "Acc"] : ["Move", "Cat", "Pwr", "Acc"]}
      rows={moves.map((m) => {
        const cells: ReactNode[] = [
          <span key="m" className="inline-flex items-center gap-1.5">
            <TypeIcon type={m.type} size={14} /> {tidy(m.move)}
          </span>,
          m.category ? <img key="c" src={`/assets/movecat/${m.category}.png`} alt={tidy(m.category)} title={tidy(m.category)} className="h-4" /> : "—",
          m.power || "—",
          m.accuracy ? `${m.accuracy}%` : "—",
        ];
        if (level) cells.unshift(m.level === 1 ? "1 (egg?)" : String(m.level || "—"));
        return cells;
      })}
    />
  );
}
