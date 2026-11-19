import { useMemo, useState, type FormEvent } from "react";
import { api, qs } from "../../lib/api";
import { useSession } from "../../lib/session";
import { ALL_TYPES, dexNumber } from "../../lib/pokemon";
import { DexCell, Pill, ShinyIcon } from "../../components/PokemonBits";
import { SaveUpload } from "../../components/SaveUpload";
import { ErrorNote, Hero, Modal, Spinner, useData } from "../../components/ui";
import { SpeciesPanel, type DexEntry } from "./SpeciesPanel";

type Game = { code: string; name: string; region?: string };
type View = {
  scope_kind: string;
  scope_name?: string;
  scope?: string;
  region?: string;
  owned: number;
  total: number;
  percent: number;
  shiny?: number;
  games?: Game[];
  scopes?: string[];
  entries?: (DexEntry & { owned: boolean; shiny?: boolean; regional?: number; caught_count?: number })[];
};

// Pokedex: every species you hold or have caught, built from uploaded saves.
// Global, per-region, or per-game (regional or national numbering).
export function Pokedex() {
  const { session } = useSession();
  const [region, setRegion] = useState("");
  const [game, setGame] = useState("");
  const [scope, setScope] = useState("");
  const [filter, setFilter] = useState<"all" | "missing" | "owned" | "shiny">("all");
  const [games, setGames] = useState<Game[]>([]);
  const [open, setOpen] = useState<DexEntry | null>(null);
  const [lookup, setLookup] = useState(false);
  const view = useData(async () => {
    const v = await api.get<View>(`/api/dex${qs(game ? { game, scope } : { region })}`);
    if (v.games?.length) setGames(v.games);
    return v;
  }, [region, game, scope]);
  const v = view.data;
  const regions = useMemo(() => [...new Set(games.map((g) => g.region || "Other"))], [games]);
  const shown = (v?.entries ?? []).filter((e) => ({ all: true, missing: !e.owned, owned: e.owned, shiny: e.owned && !!e.shiny })[filter]);
  const held = (v?.entries ?? []).reduce((n, e) => n + (e.caught_count || 0), 0);
  const label = !v
    ? ""
    : v.scope_kind === "global"
      ? "Global Pokédex"
      : v.scope_kind === "region"
        ? `${v.region} Pokédex, every ${v.region} game you play`
        : `${v.scope_name}: ${v.scope_kind === "regional" ? v.region + " Pokédex" : "National Pokédex"}`;
  const scopeName = v?.scope_name || v?.scope || "";
  const names = useMemo(() => Object.fromEntries((v?.entries ?? []).map((e) => [e.dex, e.name])), [v]);

  return (
    <>
      <Hero
        eyebrow="Pokémon tools"
        title="Pokédex"
        actions={
          <button className="btn" onClick={() => setLookup(true)}>
            Pokémon lookup
          </button>
        }
      >
        Every species you hold or have caught, built from the saves you upload.
      </Hero>
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_340px]">
        <section className="min-w-0">
          <div className="panel mb-4 grid gap-3 p-4">
            {v && (
              <>
                <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <span>
                    {label} · {v.owned} / {v.total} caught ({v.percent}%){held > 0 && ` · ${held} held in PC`}
                  </span>
                  {!!v.shiny && (
                    <span className="inline-flex items-center gap-1 text-gold">
                      <ShinyIcon /> {v.shiny} shiny
                    </span>
                  )}
                </div>
                <div className="h-3 overflow-hidden rounded-sm border-2 border-line bg-void" title={`${v.percent}% of ${v.total}`}>
                  <div className="h-full bg-[repeating-linear-gradient(90deg,var(--color-cyan)_0_6px,var(--color-cyan-hi)_6px_8px)]" style={{ width: `${v.percent}%` }} />
                </div>
              </>
            )}
            <Row label="Region">
              <Pill active={!region && !game} onClick={() => (setRegion(""), setGame(""), setScope(""))} title="Every species you hold, across every game.">
                Global
              </Pill>
              {regions.map((r) => (
                <Pill key={r} active={region === r && !game} onClick={() => (setRegion(r), setGame(""), setScope(""))}>
                  {r}
                </Pill>
              ))}
            </Row>
            {region && (
              <Row label="Game">
                <Pill active={!game} onClick={() => (setGame(""), setScope(""))}>
                  All {region} games
                </Pill>
                {games
                  .filter((g) => (g.region || "Other") === region)
                  .map((g) => (
                    <Pill key={g.code} active={game === g.code} onClick={() => (setGame(g.code), setScope(""))}>
                      {g.name}
                    </Pill>
                  ))}
              </Row>
            )}
            {game && (v?.scopes?.length ?? 0) > 1 && (
              <Row label="Numbering">
                <Pill active={v?.scope_kind === "regional"} onClick={() => setScope("regional")}>
                  Regional ({v?.region})
                </Pill>
                <Pill active={v?.scope_kind === "national"} onClick={() => setScope("national")}>
                  National
                </Pill>
              </Row>
            )}
            <Row label="Show">
              {(
                [
                  ["all", "All"],
                  ["missing", "Missing"],
                  ["owned", "Caught"],
                  ["shiny", "Shiny"],
                ] as const
              ).map(([k, l]) => (
                <Pill key={k} active={filter === k} onClick={() => setFilter(k)}>
                  {l}
                </Pill>
              ))}
            </Row>
          </div>
          {view.error ? (
            <ErrorNote error={view.error} onRetry={view.reload} />
          ) : view.loading && !v ? (
            <Spinner />
          ) : (
            <div className="grid gap-2 [grid-template-columns:repeat(auto-fill,minmax(92px,1fr))]">
              {shown.map((e) => {
                const number = e.regional ? `${scopeName} ${dexNumber(e.regional)} (National ${dexNumber(e.dex)})` : dexNumber(e.dex);
                const where = game || region ? `in ${scopeName}` : "in your Pokédex";
                return (
                  <DexCell
                    key={e.dex}
                    dex={e.dex}
                    name={e.name}
                    owned={e.owned}
                    shiny={e.shiny}
                    number={e.regional}
                    title={e.owned ? `${e.name} ${number}, caught ${where}${e.shiny ? " · shiny" : ""}` : `${e.name} ${number}, not caught ${where} yet`}
                    onOpen={() => setOpen(e)}
                  />
                );
              })}
            </div>
          )}
        </section>
        {session && (
          <aside className="xl:sticky xl:top-20">
            <SaveUpload intro="Upload a Pokémon save to fill in your Pokédex." onUploaded={(r) => r.parsed && view.reload()} />
          </aside>
        )}
      </div>

      <Modal open={!!open} onClose={() => setOpen(null)} label="Species details" wide>
        {open && <SpeciesPanel entry={open} game={game} region={region} names={names} />}
      </Modal>
      <Lookup open={lookup} onClose={() => setLookup(false)} onPick={(e) => (setLookup(false), setOpen(e))} />
    </>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="eyebrow mr-1 text-[0.5rem] text-faint">{label}</span>
      {children}
    </div>
  );
}

// Lookup: reference-data search (type, move, game) independent of what you own.
function Lookup({ open, onClose, onPick }: { open: boolean; onClose: () => void; onPick: (e: DexEntry) => void }) {
  const games = useData(() => (open ? api.get<{ games: Game[] }>("/api/pokedex-lookup/games").then((r) => r.games ?? []) : Promise.resolve([] as Game[])), [open]);
  const [results, setResults] = useState<DexEntry[] | null>(null);
  const [err, setErr] = useState("");
  const search = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErr("");
    const p: Record<string, string> = {};
    new FormData(e.currentTarget).forEach((v, k) => v && (p[k] = String(v)));
    try {
      setResults((await api.get<{ species: DexEntry[] }>(`/api/pokedex-lookup${qs(p)}`)).species ?? []);
    } catch (x) {
      setErr((x as Error).message);
    }
  };
  return (
    <Modal open={open} onClose={onClose} label="Pokémon lookup" wide>
      <span className="eyebrow">Reference</span>
      <h2 className="pixel mb-4 mt-2 text-sm">Pokémon lookup</h2>
      <form className="mb-4 grid gap-3 sm:grid-cols-5" onSubmit={search}>
        <select className="field" name="type" aria-label="Type">
          <option value="">Any type</option>
          {ALL_TYPES.map((t) => (
            <option key={t} value={t} className="capitalize">
              {t}
            </option>
          ))}
        </select>
        <input className="field" name="move" placeholder="Move, e.g. surf" aria-label="Move" />
        <select className="field" name="method" aria-label="Learn method">
          <option value="">Any method</option>
          <option value="level-up">Level up</option>
          <option value="machine">TM/HM</option>
        </select>
        <select className="field" name="in_game" aria-label="Game">
          <option value="">Any game</option>
          {games.data?.map((g) => (
            <option key={g.code} value={g.code}>
              {g.name}
            </option>
          ))}
        </select>
        <button className="btn btn-primary">Search</button>
      </form>
      {err && <ErrorNote error={err} />}
      {results && results.length === 0 && <p className="text-sm text-muted">No matches.</p>}
      <div className="grid max-h-[55vh] gap-2 overflow-y-auto [grid-template-columns:repeat(auto-fill,minmax(92px,1fr))]">
        {results?.map((s) => <DexCell key={s.dex} dex={s.dex} name={s.name} onOpen={() => onPick(s)} />)}
      </div>
    </Modal>
  );
}
