import { useState } from "react";
import { useParams } from "react-router";
import { api, ApiError, bytes } from "../lib/api";
import { useSession } from "../lib/session";
import { ErrorNote, Fact, Icon, icons, Spinner, useData } from "../components/ui";
import { SaveUpload } from "../components/SaveUpload";

type File = { id: string; name: string; extension: string; size: number; encryption?: string; patched?: boolean; download_url: string; direct_url: string; qr_url: string };
type Version = { id: number; name: string; size: number; regions?: string[]; languages?: string[]; tags?: string[]; grabbable: boolean };
export type GameDetail = {
  source: "local" | "romm";
  id: string;
  title: string;
  platform: string;
  platform_label: string;
  description?: string;
  synopsis_note?: string;
  developer?: string;
  publisher?: string;
  released?: string;
  genre?: string;
  region?: string;
  players?: string;
  rating?: string;
  metadata_source?: string;
  patched?: boolean;
  cover?: string;
  screenshots?: string[];
  files?: File[];
  versions?: Version[];
};

export function GamePage() {
  const { id = "" } = useParams();
  return <GameView id={id} />;
}

// GameView: one game, local (downloads) or RomM (grab into ROM Relay).
export function GameView({ id }: { id: string }) {
  const { data, error, loading, reload } = useData(() => api.get<GameDetail>(`/api/library/${encodeURIComponent(id)}`), [id]);
  const { session } = useSession();
  const [retain, setRetain] = useState(false);
  if (loading && !data) return <Spinner />;
  if (error) return <ErrorNote error={error} onRetry={reload} />;
  if (!data) return null;
  const g = data;
  const isAdmin = session?.admin;

  return (
    <div className="grid items-start gap-[clamp(20px,4vw,40px)] md:grid-cols-[minmax(180px,260px)_minmax(0,1fr)]">
      <aside className="grid gap-3">
        <span className="well rounded border-2 border-line">
          {g.cover ? (
            <img className="max-h-full max-w-full object-contain drop-shadow-[0_8px_16px_#000a]" src={g.cover} alt={`Cover art for ${g.title}`} />
          ) : (
            <img src="/assets/logo/brand.png" alt="" className="size-14 opacity-30 grayscale" />
          )}
        </span>
        {g.source === "local" && g.files?.[0] && (
          <a className="btn btn-primary" href={g.files[0].download_url}>
            <Icon d={icons.download} /> Download
          </a>
        )}
        {g.source === "romm" && g.versions?.[0] && <GrabButton version={g.versions[0]} retain={retain} big />}
        {g.source === "romm" && isAdmin && (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" className="size-4 accent-cyan" checked={retain} onChange={(e) => setRetain(e.target.checked)} />
            Retain permanently (otherwise grabs expire after 7 days)
          </label>
        )}
        {g.source === "local" && session?.features.pokemon !== undefined && <SaveUpload gameId={g.id} />}
      </aside>

      <article className="min-w-0">
        <span className="eyebrow">
          {g.platform_label}
          {g.source === "romm" && " · RomM"}
        </span>
        <h2 className="pixel mb-4 mt-2 text-[clamp(0.95rem,2.4vw,1.3rem)] leading-relaxed">{g.title}</h2>
        <div className="mb-4 flex flex-wrap gap-1.5">
          {g.patched && <span className="badge badge-leaf">Patched</span>}
          {g.region && <span className="badge">{g.region}</span>}
          {g.genre &&
            g.genre.split(", ").map((x) => (
              <span key={x} className="badge">
                {x}
              </span>
            ))}
          {g.released && <span className="badge badge-cyan">{g.released}</span>}
        </div>
        {g.description ? (
          <p className="max-w-3xl text-[0.95rem] leading-7 text-text/90">{g.description}</p>
        ) : (
          g.synopsis_note && <p className="max-w-3xl rounded border-2 border-line p-4 text-sm text-muted">{g.synopsis_note}</p>
        )}
        <div className="my-6 grid grid-cols-[repeat(auto-fit,minmax(140px,1fr))] gap-2.5">
          <Fact label="Developer" value={g.developer} />
          <Fact label="Publisher" value={g.publisher} />
          <Fact label="Released" value={g.released} />
          <Fact label="Players" value={g.players} />
          <Fact label="Rating" value={g.rating} />
          <Fact label="Metadata" value={g.metadata_source} />
        </div>

        {g.source === "local" && g.files && (
          <>
            <span className="eyebrow text-faint">Files</span>
            <h3 className="mb-3 mt-1 text-lg font-bold">{g.files.length > 1 ? "Choose your format" : "Download"}</h3>
            <div className="grid gap-3">
              {g.files.map((f) => (
                <FileRow key={f.id} file={f} />
              ))}
            </div>
          </>
        )}

        {g.source === "romm" && g.versions && (
          <>
            <span className="eyebrow text-faint">Versions in RomM</span>
            <h3 className="mb-3 mt-1 text-lg font-bold">{g.versions.length > 1 ? `${g.versions.length} versions` : "File"}</h3>
            <div className="grid gap-2">
              {g.versions.map((v) => (
                <div key={v.id} className="panel flex flex-wrap items-center justify-between gap-3 p-3">
                  <div className="min-w-0">
                    <div className="mb-1 flex flex-wrap gap-1.5">
                      {v.regions?.map((x) => <span key={x} className="badge">{x}</span>)}
                      {v.languages?.map((x) => <span key={x} className="badge">{x}</span>)}
                      {v.tags?.map((x) => <span key={x} className="badge badge-leaf">{x}</span>)}
                      <span className="badge">{bytes(v.size)}</span>
                    </div>
                    <p className="m-0 text-sm text-muted [overflow-wrap:anywhere]">{v.name}</p>
                  </div>
                  <GrabButton version={v} retain={retain} />
                </div>
              ))}
            </div>
          </>
        )}

        {g.screenshots && g.screenshots.length > 0 && (
          <>
            <span className="eyebrow mt-6 block text-faint">Screenshots</span>
            <div className="mt-2 grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-2.5">
              {g.screenshots.map((s) => (
                <img key={s} src={s} alt="" loading="lazy" className="w-full rounded border-2 border-line [image-rendering:pixelated]" />
              ))}
            </div>
          </>
        )}
      </article>
    </div>
  );
}

function FileRow({ file: f }: { file: File }) {
  const [qr, setQr] = useState(false);
  return (
    <section className="panel p-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="mb-1.5 flex flex-wrap gap-1.5">
            <span className="badge">{f.extension}</span>
            {f.encryption === "encrypted" && <span className="badge badge-cyan">🔒 Console · FBI install</span>}
            {f.encryption === "unencrypted" && <span className="badge badge-leaf">🔓 Emulator</span>}
            <span className="badge">{bytes(f.size)}</span>
          </div>
          <p className="m-0 text-sm font-semibold text-muted [overflow-wrap:anywhere]">{f.name}</p>
        </div>
        <a className="btn btn-primary" href={f.download_url}>
          <Icon d={icons.download} /> Download
        </a>
      </div>
      <button className="mt-3 text-sm font-bold text-cyan" onClick={() => setQr(!qr)} aria-expanded={qr}>
        {qr ? "Hide" : "Direct link & QR"}
      </button>
      {qr && (
        <div className="mt-2 grid items-start gap-3 sm:grid-cols-[minmax(0,1fr)_auto]">
          <code className="block truncate rounded border-2 border-line bg-void p-2 text-xs text-muted">{f.direct_url}</code>
          <img className="w-40 rounded bg-white p-2" src={f.qr_url} alt="Direct-download QR code" loading="lazy" />
        </div>
      )}
    </section>
  );
}

type GrabState = { phase: "idle" | "sending" | "downloading" | "done" | "failed"; message?: string; pct?: number };

// GrabButton downloads one RomM version into ROM Relay and follows its
// activity job until it lands or fails, so the button ends on the outcome.
function GrabButton({ version, retain, big }: { version: Version; retain: boolean; big?: boolean }) {
  const [state, setState] = useState<GrabState>({ phase: "idle" });
  if (!version.grabbable) {
    return (
      <button className="btn" disabled title="ROM Relay does not support this platform or file type">
        Unsupported
      </button>
    );
  }
  const grab = async () => {
    setState({ phase: "sending" });
    try {
      const r = await api.post<{ job: string }>("/api/romm/grab", new URLSearchParams({ id: String(version.id), retain: String(retain) }));
      setState({ phase: "downloading", pct: 0 });
      follow(r.job, setState);
    } catch (e) {
      setState({ phase: "failed", message: e instanceof ApiError && e.status === 409 ? "Already in the ROM Relay library." : String((e as Error).message) });
    }
  };
  const label = {
    idle: big ? "Grab into ROM Relay" : "Grab",
    sending: "Sending…",
    downloading: state.pct ? `Downloading ${state.pct}%` : "Downloading…",
    done: "In ROM Relay ✓",
    failed: "Retry",
  }[state.phase];
  return (
    <div className={`grid gap-1 ${big ? "" : "justify-items-end"}`}>
      <button className={`btn ${state.phase === "done" ? "" : "btn-primary"}`} disabled={state.phase === "sending" || state.phase === "downloading" || state.phase === "done"} onClick={grab}>
        {big && <Icon d={icons.download} />}
        {label}
      </button>
      {state.message && <p className={`m-0 max-w-xs text-xs ${state.phase === "failed" ? "text-danger" : "text-muted"} [overflow-wrap:anywhere]`}>{state.message}</p>}
    </div>
  );
}

async function follow(job: string, set: (s: GrabState) => void) {
  let seen = false;
  for (let i = 0; i < 4800; i++) {
    await new Promise((r) => setTimeout(r, 1500));
    let j: { active?: boolean; error?: string; message?: string; done?: number; total?: number } | undefined;
    try {
      const r = await fetch("/healthz", { credentials: "same-origin" });
      j = ((await r.json()).background_jobs || {})[job];
    } catch {
      continue;
    }
    if (j?.active) {
      seen = true;
      set({ phase: "downloading", pct: (j.total ?? 0) > 1 ? Math.floor((100 * (j.done ?? 0)) / (j.total ?? 1)) : undefined });
      continue;
    }
    if (j?.error) return set({ phase: "failed", message: j.error });
    if (j || seen) return set({ phase: "done", message: `${j?.message ?? "Done"}. Find it in the ROM Relay library.` });
  }
}
