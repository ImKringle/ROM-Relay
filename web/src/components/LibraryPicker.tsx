import { useMemo, useState } from "react";
import { api, bytes } from "../lib/api";
import { Modal, Spinner, useData } from "./ui";

export type LibArtifact = { id: string; name: string; extension: string; size: number; platform: string; encryption?: string; patched?: boolean };
export type LibGame = { id: string; title: string; platform: string; artwork_url?: string; artifacts?: LibArtifact[] };
export type Picked = LibArtifact & { gameTitle: string; gameId: string };

// useLibrary loads the merged catalog once per mount (one entry per release,
// every file form under it).
export function useLibrary() {
  return useData(() => api.get<{ games: LibGame[] }>("/api/games").then((r) => r.games ?? []), []);
}

// LibraryPicker: a searchable modal of library files. `accept` narrows which
// files can be picked (by platform or extension).
export function LibraryPicker({ open, onClose, onPick, accept, title = "Pick from your library" }: { open: boolean; onClose: () => void; onPick: (p: Picked) => void; accept?: (a: LibArtifact) => boolean; title?: string }) {
  const lib = useLibrary();
  const [q, setQ] = useState("");
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (lib.data ?? [])
      .flatMap((g) => (g.artifacts ?? []).map((a) => ({ ...a, platform: a.platform || g.platform, gameTitle: g.title, gameId: g.id, cover: g.artwork_url ? `/media/${g.id}/cover` : "" })))
      .filter((a) => !accept || accept(a))
      .filter((a) => !needle || `${a.gameTitle} ${a.name}`.toLowerCase().includes(needle));
  }, [lib.data, q, accept]);
  return (
    <Modal open={open} onClose={onClose} label={title} wide>
      <span className="eyebrow">Library</span>
      <h2 className="pixel mb-3 mt-2 text-sm">{title}</h2>
      <input className="field mb-3" type="search" autoFocus placeholder="Filter by title or file name…" value={q} onChange={(e) => setQ(e.target.value)} />
      {lib.loading && !lib.data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted">Nothing in the library fits.</p>
      ) : (
        <div className="grid max-h-[60vh] gap-2.5 overflow-y-auto pr-1 [grid-template-columns:repeat(auto-fill,minmax(140px,1fr))]">
          {rows.slice(0, 400).map((a) => (
            <button
              key={a.id}
              className="card"
              onClick={() => {
                onPick(a);
                onClose();
              }}
            >
              <span className="well">{a.cover ? <img src={a.cover} alt="" loading="lazy" className="max-h-full max-w-full object-contain" /> : <img src="/assets/logo/brand.png" alt="" className="size-10 opacity-30" />}</span>
              <span className="grid gap-1 p-2">
                <strong className="text-xs [overflow-wrap:anywhere]">{a.gameTitle}</strong>
                <span className="text-[0.7rem] text-muted [overflow-wrap:anywhere]">
                  {a.extension.toUpperCase()} · {bytes(a.size)}
                  {a.encryption === "encrypted" && " · 🔒"}
                </span>
              </span>
            </button>
          ))}
        </div>
      )}
    </Modal>
  );
}
