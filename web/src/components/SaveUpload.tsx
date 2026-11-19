import { useRef, useState } from "react";
import { api } from "../lib/api";
import { Icon, icons } from "./ui";

export type UploadResult = { stored?: boolean; parsed?: boolean; inserted?: number; pokemon_found?: number; detail?: string };

export function describeUpload(r: UploadResult) {
  if (r.stored === false) {
    const same = "Already stored, identical to your latest save.";
    return r.parsed && (r.inserted ?? 0) > 0 ? `${same} Dex updated: ${r.inserted} new from ${r.pokemon_found ?? 0} read.` : same;
  }
  if (r.parsed) return `Stored. ${r.pokemon_found ?? 0} Pokémon read, ${r.inserted ?? 0} new in your Dex.`;
  return r.detail || "Stored.";
}

// SaveUpload: pick one or more saves and upload them, one request each (the
// server's pipeline is built around one save at a time). There is no game
// picker: the server reads the save; gameId is only a fallback.
export function SaveUpload({ gameId, intro, onUploaded }: { gameId?: string; intro?: string; onUploaded?: (r: UploadResult) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [files, setFiles] = useState<File[]>([]);
  const [out, setOut] = useState("");
  const [busy, setBusy] = useState(false);

  const send = async () => {
    if (!files.length) return setOut("Choose a save file first.");
    setBusy(true);
    let ok = 0;
    for (const [i, f] of files.entries()) {
      setOut(files.length > 1 ? `Uploading ${i + 1} of ${files.length}: ${f.name}…` : "Uploading…");
      const body = new FormData();
      if (gameId) body.append("game_id", gameId);
      body.append("save_file", f);
      try {
        const r = await api.post<UploadResult>("/api/saves/upload", body);
        ok++;
        if (files.length === 1) setOut(describeUpload(r));
        onUploaded?.(r);
      } catch (e) {
        setOut((e as Error).message);
      }
    }
    if (files.length > 1) setOut(`Uploaded ${ok} of ${files.length} saves.`);
    if (ok) {
      setFiles([]);
      if (input.current) input.current.value = "";
    }
    setBusy(false);
  };

  return (
    <section className="panel grid gap-2 p-4">
      <h3 className="m-0 flex items-center gap-2 text-base font-bold">
        <Icon d={icons.upload} className="size-4 text-cyan" /> Upload a save
      </h3>
      <p className="m-0 text-sm text-muted">{intro ?? "Store this game's save so you can pick it up on any device. Pokémon saves also update your Pokédex."}</p>
      <div className="flex flex-wrap items-center gap-2">
        <label className="btn btn-sm cursor-pointer">
          Choose files
          <input ref={input} className="sr-only" type="file" multiple onChange={(e) => (setFiles(Array.from(e.target.files ?? [])), setOut(""))} />
        </label>
        <span className="min-w-0 truncate text-sm text-muted">{files.length === 0 ? "No file chosen" : files.length === 1 ? files[0].name : `${files.length} files chosen`}</span>
        <button className="btn btn-sm btn-primary ml-auto" onClick={send} disabled={busy}>
          Upload
        </button>
      </div>
      <p className="m-0 min-h-[1.2em] text-sm text-muted" aria-live="polite">
        {out}
      </p>
    </section>
  );
}
