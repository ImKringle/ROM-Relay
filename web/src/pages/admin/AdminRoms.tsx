import { useMemo, useState } from "react";
import { api, bytes } from "../../lib/api";
import { ConfirmButton } from "../../components/ConfirmButton";
import { BackLink } from "../../components/BackLink";
import { Empty, ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Artifact = { id: string; name: string; size: number; patched?: boolean; external?: boolean; encryption?: string };
type Game = { id: string; title: string; platform: string; artifacts?: Artifact[] };

// AdminRoms: one row per file (a delete acts on a file), filterable.
export function AdminRoms() {
  const { data, error, loading, reload } = useData(() => api.get<{ games: Game[] }>("/api/admin/games"), []);
  const [q, setQ] = useState("");
  const rows = useMemo(() => {
    const all = (data?.games ?? []).flatMap((g) => (g.artifacts ?? []).map((a) => ({ g, a })));
    const needle = q.trim().toLowerCase();
    return needle ? all.filter(({ g, a }) => `${g.title} ${a.name} ${g.platform}`.toLowerCase().includes(needle)) : all;
  }, [data, q]);
  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Catalog" title="ROM Library">
        Every cataloged file. Deleting one removes the file from disk as well as the catalog entry. This cannot be undone.
      </Hero>
      <input className="field mb-4" type="search" placeholder="Search by title, file name, or platform…" value={q} onChange={(e) => setQ(e.target.value)} />
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty>No files match.</Empty>
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {rows.slice(0, 500).map(({ g, a }) => (
            <li key={a.id} className="panel flex flex-wrap items-center justify-between gap-3 p-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <strong className="text-sm">{g.title}</strong>
                  <span className="badge badge-cyan">{g.platform.toUpperCase()}</span>
                  {a.patched && <span className="badge badge-leaf">Patched</span>}
                  {a.external && <span className="badge badge-gold">External</span>}
                </div>
                <div className="text-xs text-muted [overflow-wrap:anywhere]">
                  {a.name} · {bytes(a.size)}
                </div>
              </div>
              <ConfirmButton label="Delete" confirm="Delete file?" onConfirm={async () => (await api.del(`/api/admin/roms/${encodeURIComponent(a.id)}`), reload())} />
            </li>
          ))}
          {rows.length > 500 && <li className="text-center text-sm text-muted">Showing 500 of {rows.length}. Narrow the search.</li>}
        </ul>
      )}
    </>
  );
}
