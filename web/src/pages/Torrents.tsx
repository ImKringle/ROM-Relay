import { useEffect, useRef, useState } from "react";
import { api, bytes, qs } from "../lib/api";
import { useSession } from "../lib/session";
import { Empty, ErrorNote, Hero, Spinner } from "../components/ui";

type Item = { torrent: string; index: number; path: string; size: number; platform?: string; archive?: boolean; title: string; regions?: string[]; languages?: string[]; tags?: string[] };
type Facet = { value: string; label?: string; count: number };
type Resp = { results: Item[]; total: number; offset: number; facets: Record<string, Facet[]> };
const facetNames = [
  ["region", "Region"],
  ["language", "Language"],
  ["tag", "Tag"],
  ["source", "Source"],
] as const;

// Torrents: search the admin's sources and grab single games.
export function Torrents() {
  const { session } = useSession();
  const [q, setQ] = useState("");
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [data, setData] = useState<Resp | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [retain, setRetain] = useState(false);
  const seq = useRef(0);

  const load = async (offset: number) => {
    const mine = ++seq.current;
    setLoading(true);
    try {
      const r = await api.get<Resp>(`/api/torrents${qs({ q, offset, ...filters })}`);
      if (mine !== seq.current) return;
      setError(null);
      setData(r);
      setItems((x) => (offset ? [...x, ...r.results] : r.results));
    } catch (e) {
      if (mine === seq.current) setError(e);
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  };
  useEffect(() => {
    const t = setTimeout(() => load(0), 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, filters]);

  return (
    <>
      <Hero eyebrow="Get games" title="Torrents">
        Find a game in this relay's sources and grab it into the library. Each grab downloads just that one game; progress shows in the activity tray.
      </Hero>
      <section className="panel mb-5 grid gap-3 p-4">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,2fr)_repeat(4,minmax(0,1fr))]">
          <input className="field" type="search" placeholder="Game title, e.g. mario kart" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Game" />
          {facetNames.map(([f, label]) => {
            const opts = data?.facets[f] ?? [];
            if (f === "source" && opts.length < 2 && !filters[f]) return null;
            return (
              <select key={f} className="field" aria-label={label} value={filters[f] ?? ""} onChange={(e) => setFilters({ ...filters, [f]: e.target.value })}>
                <option value="">Any {label.toLowerCase()}</option>
                {opts.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label || o.value} ({o.count.toLocaleString()})
                  </option>
                ))}
              </select>
            );
          })}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-muted">
          <span>{data ? `${data.total.toLocaleString()} games` : ""}</span>
          {session?.admin && (
            <label className="flex items-center gap-2">
              <input type="checkbox" className="size-4 accent-cyan" checked={retain} onChange={(e) => setRetain(e.target.checked)} />
              Retain permanently (otherwise grabs expire after 7 days)
            </label>
          )}
        </div>
      </section>
      {error ? (
        <ErrorNote error={error} onRetry={() => load(0)} />
      ) : !loading && items.length === 0 ? (
        <Empty>No games match.</Empty>
      ) : (
        <ul className="m-0 grid list-none gap-4 p-0 [grid-template-columns:repeat(auto-fill,minmax(230px,1fr))]">
          {items.map((it) => (
            <TorrentTile key={`${it.torrent}:${it.index}`} item={it} retain={retain} />
          ))}
        </ul>
      )}
      {loading && <Spinner />}
      {data && items.length < data.total && !loading && (
        <div className="mt-4 text-center">
          <button className="btn" onClick={() => load(items.length)}>
            Load more
          </button>
        </div>
      )}
    </>
  );
}

function TorrentTile({ item: it, retain }: { item: Item; retain: boolean }) {
  const [state, setState] = useState<{ label: string; msg?: string; bad?: boolean; done?: boolean }>({ label: "Grab" });
  const grab = async () => {
    setState({ label: "Sending…" });
    try {
      await api.post("/api/torrents/grab", new URLSearchParams({ torrent: it.torrent, index: String(it.index), retain: String(retain) }));
      setState({ label: "Queued", msg: "Downloading, see the activity tray.", done: true });
    } catch (e) {
      setState({ label: "Retry", msg: (e as Error).message, bad: true });
    }
  };
  return (
    <li className="panel flex flex-col gap-2 p-3">
      <strong className="text-sm leading-snug [overflow-wrap:anywhere]">{it.title}</strong>
      <span className="flex flex-wrap gap-1.5">
        <span className="badge badge-cyan">{it.platform ? it.platform.toUpperCase() : "ARCHIVE"}</span>
        {it.regions?.map((r) => <span key={r} className="badge">{r}</span>)}
        {!!it.languages?.length && <span className="badge">{it.languages.join(", ")}</span>}
        {it.tags?.map((t) => <span key={t} className="badge badge-leaf">{t}</span>)}
      </span>
      <div className="mt-auto flex items-center justify-between gap-2 pt-1">
        <span className="text-xs text-muted">{bytes(it.size)}</span>
        <button className="btn btn-sm btn-primary" disabled={state.label === "Sending…" || state.done} onClick={grab}>
          {state.label}
        </button>
      </div>
      {state.msg && <p className={`m-0 text-xs ${state.bad ? "text-danger" : "text-muted"} [overflow-wrap:anywhere]`}>{state.msg}</p>}
    </li>
  );
}
