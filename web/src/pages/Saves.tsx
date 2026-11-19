import { api } from "../lib/api";
import { bytes } from "../lib/api";
import { ConfirmButton } from "../components/ConfirmButton";
import { SaveUpload } from "../components/SaveUpload";
import { Empty, ErrorNote, Hero, Icon, icons, Spinner, useData } from "../components/ui";

type Save = {
  id: string;
  file_name: string;
  size_bytes: number;
  uploaded_at: string;
  parsed?: boolean;
  parsed_game?: string;
  pokemon_count?: number;
  game_label?: string;
  game_info?: { platform?: string; region?: string; generation?: number; alternates?: { code: string; name: string }[] };
};

// Saves: every stored version, grouped by game, newest first. Every upload is
// a new version, so a bad one never replaces progress.
export function Saves() {
  const { data, error, loading, reload } = useData(() => api.get<{ saves: Save[] }>("/api/saves"), []);
  const groups = new Map<string, Save[]>();
  for (const s of data?.saves ?? []) {
    const k = s.game_label || "Unknown game";
    groups.set(k, [...(groups.get(k) ?? []), s]);
  }
  return (
    <>
      <Hero eyebrow="Memory cards" title="Save Data">
        Your uploaded saves, newest first. Every upload is kept as a new version, so a bad one never replaces your progress.
      </Hero>
      <div className="mb-6 max-w-2xl">
        <SaveUpload intro="Upload any save. Pokémon saves also update your Pokédex." onUploaded={reload} />
      </div>
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading && !data ? (
        <Spinner />
      ) : groups.size === 0 ? (
        <Empty>
          <strong className="text-lg text-text">No saves stored yet</strong>
          <p className="m-0 max-w-sm text-sm">Open a game in the Library and upload its save, or use the panel above.</p>
        </Empty>
      ) : (
        <div className="grid items-start gap-4 [grid-template-columns:repeat(auto-fill,minmax(320px,1fr))]">
          {[...groups].map(([game, versions]) => {
            const info = versions[0].game_info ?? {};
            return (
              <section key={game} className="panel p-4">
                <h2 className="m-0 text-base font-bold">{game}</h2>
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {info.platform && <span className="badge badge-cyan">{info.platform}</span>}
                  {info.region && <span className="badge">{info.region} region</span>}
                  {info.generation && <span className="badge">Generation {info.generation}</span>}
                </div>
                <p className="mb-0 mt-1 text-xs text-muted">
                  {versions.length} version{versions.length > 1 ? "s" : ""} stored
                </p>
                {versions.map((s, i) => (
                  <SaveRow key={s.id} save={s} latest={i === 0} reload={reload} />
                ))}
              </section>
            );
          })}
        </div>
      )}
    </>
  );
}

function SaveRow({ save: s, latest, reload }: { save: Save; latest: boolean; reload: () => void }) {
  const when = new Date(s.uploaded_at).toLocaleString();
  const alternates = s.game_info?.alternates ?? [];
  return (
    <div className={`mt-2 flex flex-wrap items-center gap-3 rounded border-2 p-2.5 ${latest ? "border-cyan-deep" : "border-line"}`}>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-sm font-semibold [overflow-wrap:anywhere]">
          {s.file_name}
          {latest && <span className="badge badge-cyan">Latest</span>}
          {s.parsed && (
            <span className="badge">
              {s.parsed_game || "Pokémon"} · {s.pokemon_count ?? 0} Pokémon
            </span>
          )}
        </div>
        <div className="text-xs text-muted">
          {when} · {bytes(s.size_bytes)}
        </div>
        {/* Gen 3 pairs (FireRed/LeafGreen...) cannot always be told apart from
            SRAM alone, so the uploader can correct PKHeX's guess. */}
        {alternates.length > 0 && (
          <label className="mt-1 flex items-center gap-1.5 text-xs text-muted">
            This is actually…
            <select
              className="field w-auto py-1 text-xs"
              defaultValue=""
              onChange={async (e) => {
                if (!e.target.value) return;
                await api.post(`/api/saves/${encodeURIComponent(s.id)}/game-override`, { game: e.target.value });
                reload();
              }}
            >
              <option value="" />
              {alternates.map((a) => (
                <option key={a.code} value={a.code}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      <a className="btn btn-sm" href={`/saves/${encodeURIComponent(s.id)}/download`}>
        <Icon d={icons.download} /> Download
      </a>
      <ConfirmButton label="Delete" confirm={`Delete ${s.file_name}?`} onConfirm={async () => (await api.del(`/api/saves/${encodeURIComponent(s.id)}`), reload())} />
    </div>
  );
}
