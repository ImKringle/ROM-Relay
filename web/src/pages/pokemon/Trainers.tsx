import { useMemo, useState } from "react";
import { api } from "../../lib/api";
import { minutes, money } from "../../lib/pokemon";
import { Pill, Sprite } from "../../components/PokemonBits";
import { SaveUpload } from "../../components/SaveUpload";
import { Empty, ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Profile = {
  ot?: string;
  tid: number;
  game: string;
  game_name?: string;
  region?: string;
  trusted?: boolean;
  progress_kind?: "badges" | "trials";
  progress_count: number;
  progress_max: number;
  money?: number;
  played_minutes?: number;
  dex_caught?: number;
  hof_clears?: number | null;
  hof?: { entry_index: number; species: number; level: number }[];
  notes?: Record<string, string>;
};

// Badge art in the order each region awards them. The save reports a count,
// so the first N are lit for N badges (what a forward playthrough looks like).
const BADGES: Record<string, string[]> = {
  Kanto: ["boulder", "cascade", "thunder", "rainbow", "soul", "marsh", "volcano", "earth"],
  Johto: ["zephyr", "hive", "plain", "fog", "storm", "mineral", "glacier", "rising"],
  Hoenn: ["stone", "knuckle", "dynamo", "heat", "balance", "feather", "mind", "rain"],
  Sinnoh: ["coal", "forest", "cobble", "fen", "relic", "mine", "icicle", "beacon"],
  Unova: ["trio", "basic", "insect", "bolt", "quake", "jet", "freeze", "legend"],
  Kalos: ["bug", "cliff", "rumble", "plant", "voltage", "fairy", "psychic", "iceberg"],
};
function badgeSet(p: Profile): string[] | null {
  if (p.region === "Galar") {
    const sh = p.game === "SH";
    return ["grass", "water", "fire", sh ? "ghost" : "fighting", "galarfairy", sh ? "ice" : "rock", "dark", "dragon"].map((b) => `${b}-badge`);
  }
  if (p.region === "Paldea") return ["bug", "grass", "electric", "water", "normal", "ghost", "psychic", "ice"].map((t) => `svbadge-victoryroad-${t}`);
  return BADGES[p.region ?? ""]?.map((b) => `${b}-badge`) ?? null;
}

// Trainers: one card per playthrough, from uploaded saves.
export function Trainers() {
  const { data, error, loading, reload } = useData(() => api.get<{ profiles: Profile[]; totals?: { money?: number; played_minutes?: number; clears?: number } }>("/api/userdex"), []);
  const [q, setQ] = useState("");
  const [game, setGame] = useState("");
  const profiles = data?.profiles ?? [];
  const games = useMemo(() => {
    const seen = new Map<string, string>();
    profiles.forEach((p) => p.game && !seen.has(p.game) && seen.set(p.game, p.game_name || p.game));
    return [...seen];
  }, [profiles]);
  const shown = profiles.filter((p) => (!game || p.game === game) && `${p.ot ?? ""} ${p.game_name ?? ""} ${p.game}`.toLowerCase().includes(q.trim().toLowerCase()));

  return (
    <>
      <Hero eyebrow="Pokémon tools" title="Trainers">
        The playthroughs behind your Pokédex, one card per save.
      </Hero>
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_340px]">
        <section className="grid min-w-0 gap-4">
          {data?.totals && (
            <div className="grid grid-cols-3 gap-3">
              {[
                ["Pokédollars on hand", money(data.totals.money)],
                ["Total playtime", minutes(data.totals.played_minutes)],
                ["Hall of Fame clears", String(data.totals.clears ?? 0)],
              ].map(([k, v]) => (
                <div key={k} className="panel p-3">
                  <div className="eyebrow text-[0.5rem] text-faint">{k}</div>
                  <div className="pixel mt-2 text-sm text-cyan">{v}</div>
                </div>
              ))}
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <input className="field max-w-xs" type="search" placeholder="Search trainers…" value={q} onChange={(e) => setQ(e.target.value)} />
            {games.length > 1 && (
              <>
                <Pill active={!game} onClick={() => setGame("")}>
                  All games
                </Pill>
                {games.map(([code, name]) => (
                  <Pill key={code} active={game === code} onClick={() => setGame(code)}>
                    {name}
                  </Pill>
                ))}
              </>
            )}
          </div>
          {error ? (
            <ErrorNote error={error} onRetry={reload} />
          ) : loading && !data ? (
            <Spinner />
          ) : shown.length === 0 ? (
            <Empty>{profiles.length ? "No playthroughs match that filter." : "No playthroughs yet. Upload a Pokémon save and its trainer appears here."}</Empty>
          ) : (
            <div className="grid gap-4 md:grid-cols-2">
              {shown.map((p, i) => (
                <TrainerCard key={`${p.game}:${p.tid}:${i}`} p={p} />
              ))}
            </div>
          )}
        </section>
        <aside className="xl:sticky xl:top-20">
          <SaveUpload intro="Upload a Pokémon save to add its trainer." onUploaded={(r) => r.parsed && reload()} />
        </aside>
      </div>
    </>
  );
}

// A note (from the save parser) replaces a value the game cannot record, so a
// "can't know" never looks like a zero.
const Note = ({ text }: { text: string }) => <span className="inline-flex items-start gap-1 text-xs text-muted">⚠ {text}</span>;

function TrainerCard({ p }: { p: Profile }) {
  const notes = p.notes ?? {};
  const set = p.progress_kind === "badges" ? badgeSet(p) : null;
  const team = (p.hof ?? []).filter((e) => e.entry_index === 0);
  return (
    <section className="panel grid gap-3 p-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <h2 className="m-0 text-base font-bold">{p.ot || "Unknown trainer"}</h2>
        <span className="pixel text-[0.55rem] text-faint">TID {String(p.tid).padStart(5, "0")}</span>
        <span className="badge badge-cyan">{p.game_name || p.game}</span>
        {!p.trusted && <span className="badge">identity only</span>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {p.progress_kind ? (
          <>
            {set ? (
              <span className="flex flex-wrap gap-1.5" role="img" aria-label={`${p.progress_count} of ${p.progress_max} badges`}>
                {set.map((slug, i) => (
                  <img key={slug} src={`/assets/badges/${slug}.png`} alt="" width={22} height={22} className={`size-[22px] object-contain ${i < p.progress_count ? "" : "opacity-25 grayscale"}`} />
                ))}
              </span>
            ) : null}
            <span className={set ? "text-xs text-faint" : "badge badge-cyan"}>
              {!set && (p.progress_kind === "trials" ? "Trials " : "Badges ")}
              {p.progress_count}/{p.progress_max}
            </span>
            {notes.progress && <Note text={notes.progress} />}
          </>
        ) : notes.trainer ? (
          <Note text={notes.trainer} />
        ) : (
          <span className="text-xs text-muted">No progress recorded.</span>
        )}
      </div>
      <div className="flex flex-wrap gap-3 text-sm">
        {p.trusted ? (
          <>
            <span>{money(p.money)} on hand</span>
            <span className="text-muted">{minutes(p.played_minutes)} played</span>
            {(p.dex_caught ?? 0) > 0 && <span className="text-muted">{p.dex_caught} Pokémon caught this run</span>}
          </>
        ) : (
          notes.trainer && <Note text={notes.trainer} />
        )}
      </div>
      <div className="text-sm">
        {p.hof_clears == null ? (
          notes.hall_of_fame_clears || notes.hall_of_fame ? (
            <Note text={notes.hall_of_fame_clears || notes.hall_of_fame} />
          ) : (
            p.trusted && <span className="text-xs text-muted">No Hall of Fame recorded yet.</span>
          )
        ) : (
          <span className="font-semibold" title={notes.hall_of_fame_clears}>
            Hall of Fame clears: {p.hof_clears}
            {notes.hall_of_fame_clears && " ⓘ"}
          </span>
        )}
      </div>
      {team.length > 0 && (
        <div>
          <span className="eyebrow text-[0.5rem] text-faint">Latest Hall of Fame team</span>
          <div className="mt-1 flex flex-wrap items-end gap-2">
            {team.map((m, i) => (
              <span key={i} className="inline-flex flex-col items-center">
                <Sprite dex={m.species} size={48} alt={`#${m.species}`} />
                {m.level > 0 && <span className="text-[0.65rem] text-muted">Lv {m.level}</span>}
              </span>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
