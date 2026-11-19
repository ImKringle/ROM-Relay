import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { api, qs } from "../lib/api";
import { useSession } from "../lib/session";
import { Empty, ErrorNote, Hero, Icon, icons, Modal, Spinner } from "../components/ui";
import { GameView } from "./Game";
import { LibraryTools } from "./LibraryTools";

type Card = { id: string; title: string; platform: string; platform_label: string; region?: string; genre?: string; year?: string; patched?: boolean; files: number; cover?: string };
type Facet = { value: string; label: string; count: number; checked: boolean };
type LibraryResponse = { source: string; games: Card[]; total: number; limit: number; facets: Record<string, Facet[]>; has_encryption: boolean; remote_search?: boolean };

// Library: the home page. ?source=romm switches to the linked RomM library;
// every filter lives in the URL so views can be linked and bookmarked.
export function Library() {
  const { session } = useSession();
  const [params, setParams] = useSearchParams();
  const source = params.get("source") === "romm" ? "romm" : "local";
  const [query, setQuery] = useState(params.get("q") ?? "");
  const [data, setData] = useState<LibraryResponse | null>(null);
  const [games, setGames] = useState<Card[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const open = params.get("game");
  const seq = useRef(0);
  const key = params.toString();

  // Debounced search box -> URL.
  useEffect(() => {
    const t = setTimeout(() => {
      if ((params.get("q") ?? "") !== query) update({ q: query || null });
    }, 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  const load = async (offset: number) => {
    const mine = ++seq.current;
    setLoading(true);
    setError(null);
    try {
      const p = new URLSearchParams(params);
      p.delete("game");
      if (offset) p.set("offset", String(offset));
      const r = await api.get<LibraryResponse>(`/api/library?${p}`);
      if (mine !== seq.current) return;
      setData(r);
      setGames((g) => (offset ? [...g, ...r.games] : r.games));
    } catch (e) {
      if (mine === seq.current) setError(e);
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  };

  // Reload when anything but the open game changes.
  const filterKey = (() => {
    const p = new URLSearchParams(params);
    p.delete("game");
    return p.toString();
  })();
  useEffect(() => {
    load(0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKey]);

  // Infinite scroll: a sentinel below the grid asks for the next page.
  const sentinel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = sentinel.current;
    if (!el) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0].isIntersecting && !loading && data && games.length < data.total) load(games.length);
    }, { rootMargin: "600px" });
    io.observe(el);
    return () => io.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loading, data, games.length, key]);

  function update(changes: Record<string, string | string[] | null>) {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(changes)) {
      p.delete(k);
      if (Array.isArray(v)) v.forEach((x) => p.append(k, x));
      else if (v) p.set(k, v);
    }
    setParams(p, { replace: true });
  }
  const toggle = (facet: string, value: string) => {
    const now = params.getAll(facet);
    update({ [facet]: now.includes(value) ? now.filter((x) => x !== value) : [...now, value] });
  };
  const switchSource = (s: string) => {
    setQuery("");
    setParams(s === "romm" ? { source: "romm" } : {}, { replace: false });
  };
  const activeFilters = ["platform", "status"].reduce((n, f) => n + params.getAll(f).length, 0);

  const rommAvailable = session?.features.romm && (session.authenticated || session.admin);
  return (
    <>
      <Hero
        eyebrow={source === "romm" ? "Linked library" : "Your collection"}
        title="Game Library"
        actions={
          rommAvailable && (
            <div className="flex gap-1.5" role="tablist" aria-label="Library source">
              {[
                ["local", "ROM Relay"],
                ["romm", "RomM"],
              ].map(([v, label]) => (
                <button key={v} role="tab" aria-selected={source === v} className={`chip ${source === v ? "chip-on" : ""}`} onClick={() => switchSource(v)}>
                  {label}
                </button>
              ))}
            </div>
          )
        }
      >
        {source === "romm"
          ? `${data?.total.toLocaleString() ?? "…"} games in RomM. Open one to grab it into ROM Relay.`
          : "Browse, download and send your ROMs."}
      </Hero>

      {source === "local" && <LibraryTools onLibraryChanged={() => load(0)} />}

      <div className="grid items-start gap-6 lg:grid-cols-[250px_minmax(0,1fr)]">
        <aside className={`panel sticky top-20 grid max-h-[calc(100dvh-6rem)] gap-4 overflow-y-auto p-4 ${filtersOpen ? "" : "max-lg:hidden"}`}>
          <div className="flex items-center justify-between">
            <span className="eyebrow">Filters</span>
            {activeFilters > 0 && (
              <button className="text-xs text-cyan" onClick={() => update({ platform: null, status: null })}>
                Clear all
              </button>
            )}
          </div>
          <label className="relative">
            <span className="sr-only">Search</span>
            <input className="field pl-8" type="search" placeholder="Search titles, files…" value={query} onChange={(e) => setQuery(e.target.value)} />
            <Icon d={icons.search} className="absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-faint" />
          </label>
          {data &&
            (["platform", "status"] as const).map((f) =>
              data.facets[f]?.length ? (
                <fieldset key={f} className="grid gap-2 border-0 p-0">
                  <legend className="eyebrow mb-2 text-[0.5rem] text-faint">{f}</legend>
                  <div className="flex flex-wrap gap-1.5">
                    {data.facets[f].map((v) => (
                      <button key={v.value} className="chip" aria-pressed={params.getAll(f).includes(v.value)} onClick={() => toggle(f, v.value)}>
                        {v.label} <span className="text-faint">{v.count}</span>
                      </button>
                    ))}
                  </div>
                </fieldset>
              ) : null,
            )}
          {data?.has_encryption && source === "local" && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="size-4 accent-cyan" checked={params.get("show_encrypted") === "1"} onChange={(e) => update({ show_encrypted: e.target.checked ? "1" : null })} />
              Show encrypted
            </label>
          )}
        </aside>

        <section className="min-w-0">
          <div className="mb-3 flex items-center justify-between gap-3">
            <button className="btn btn-sm lg:hidden" onClick={() => setFiltersOpen(!filtersOpen)} aria-expanded={filtersOpen}>
              <Icon d={icons.filter} /> Filters {activeFilters > 0 && <span className="badge badge-cyan">{activeFilters}</span>}
            </button>
            <span className="text-sm text-muted">{data ? `${data.total.toLocaleString()} game${data.total === 1 ? "" : "s"}` : ""}</span>
          </div>
          {error ? (
            <ErrorNote error={error} onRetry={() => load(0)} />
          ) : !loading && games.length === 0 ? (
            <Empty>
              <p className="m-0">No games match.</p>
              {data?.remote_search && (
                <Link className="btn btn-primary" to={`/admin/patches${qs({ remote_q: query })}`}>
                  <Icon d={icons.search} /> Search remotely
                </Link>
              )}
            </Empty>
          ) : (
            <ul className="m-0 grid list-none grid-cols-[repeat(auto-fill,minmax(170px,1fr))] gap-4 p-0 max-[420px]:grid-cols-2">
              {games.map((g) => (
                <li key={g.id} className="min-w-0">
                  <button className="card h-full w-full" onClick={() => update({ game: g.id })}>
                    <span className="well">
                      {g.cover ? (
                        <img className="max-h-full max-w-full object-contain drop-shadow-[0_6px_12px_#000a]" src={g.cover} alt="" loading="lazy" />
                      ) : (
                        <img src="/assets/logo/brand.png" alt="" className="size-12 opacity-30 grayscale" />
                      )}
                    </span>
                    <span className="grid content-start gap-2 p-3">
                      <strong className="text-sm leading-snug [overflow-wrap:anywhere]">{g.title}</strong>
                      <span className="flex flex-wrap gap-1.5">
                        <span className="badge badge-cyan">{g.platform_label}</span>
                        {g.patched && <span className="badge badge-leaf">Patched</span>}
                        {g.region && <span className="badge">{g.region}</span>}
                        {g.files > 1 && <span className="badge badge-gold">{g.files} {source === "romm" ? "versions" : "files"}</span>}
                      </span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {loading && <Spinner />}
          <div ref={sentinel} />
        </section>
      </div>

      <Modal open={!!open} onClose={() => update({ game: null })} label="Game details" wide>
        {open && <GameView id={open} />}
      </Modal>
    </>
  );
}
