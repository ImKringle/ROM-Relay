import { useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { api, ApiError, bytes } from "../lib/api";
import { useSession } from "../lib/session";
import { useLibrary, type LibArtifact } from "../components/LibraryPicker";
import { Icon, icons, Modal, Spinner } from "../components/ui";

// The ROM Relay library's tools: 3DS batch installer, FTP send, CIA
// conversion, encrypt/decrypt, and uploading a ROM. Local-library only.

type Rom = LibArtifact & { title: string; cover: string; cia: boolean };
const cleanTitle = (t: string) => t.replace(/[([][^)\]]*[)\]]/g, "").replace(/\s+/g, " ").trim() || t;

function useRoms() {
  const lib = useLibrary();
  const roms = useMemo<Rom[]>(
    () =>
      (lib.data ?? []).flatMap((g) =>
        (g.artifacts ?? []).map((a) => {
          const ext = (a.extension || "").toLowerCase();
          return { ...a, extension: ext, platform: a.platform || g.platform, title: cleanTitle(g.title), cover: g.artwork_url ? `/media/${g.id}/cover` : "", cia: ext === ".cia" && a.encryption !== "unencrypted" };
        }),
      ),
    [lib.data],
  );
  return { roms, loading: lib.loading };
}

export function LibraryTools({ onLibraryChanged }: { onLibraryChanged: () => void }) {
  const { session } = useSession();
  const [open, setOpen] = useState<"" | "fbi" | "ftp" | "convert" | "crypt" | "upload">("");
  const close = () => setOpen("");
  const tool = (key: typeof open, label: string, icon: string, show = true) =>
    show && (
      <button className="btn" onClick={() => setOpen(key)}>
        <Icon d={icon} /> {label}
      </button>
    );
  return (
    <>
      <div className="mb-5 flex flex-wrap gap-2">
        {tool("fbi", "3DS Batch Installer", "M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h3M20 14v3M14 20h6")}
        {tool("ftp", "Send via FTP", "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7ZM12 11v6m0-6 3 3m-3-3-3 3")}
        {tool("convert", "Convert to CIA", icons.download, session?.features.convert)}
        {tool("crypt", "Encrypt / Decrypt", "M12 2 4 6v6c0 5 3.5 8 8 10 4.5-2 8-5 8-10V6l-8-4ZM9.5 12h5M12 9.5v5", session?.features.crypt)}
        {tool("upload", "Upload ROM", icons.upload)}
      </div>
      {open === "fbi" && <FBIBatch onClose={close} />}
      {open === "ftp" && <FTPSend onClose={close} />}
      {open === "convert" && <UploadJob kind="convert" onClose={close} />}
      {open === "crypt" && <UploadJob kind="crypt" onClose={close} />}
      {open === "upload" && <UploadROM onClose={close} onDone={onLibraryChanged} />}
    </>
  );
}

// MultiPick: cover cards with checkboxes and platform chips. Hiding a card
// never clears it, so a selection survives switching filters.
function MultiPick({ roms, selected, setSelected, defaultPlatforms, tags }: { roms: Rom[]; selected: Set<string>; setSelected: (s: Set<string>) => void; defaultPlatforms?: string[]; tags?: (r: Rom) => ReactNode }) {
  const platforms = useMemo(() => [...new Set(roms.map((r) => r.platform))].sort(), [roms]);
  const [active, setActive] = useState<Set<string>>(new Set((defaultPlatforms ?? []).filter((p) => platforms.includes(p))));
  const visible = roms.filter((r) => !active.size || active.has(r.platform));
  const toggleAll = () => {
    const next = new Set(selected);
    const all = visible.every((r) => next.has(r.id));
    visible.forEach((r) => (all ? next.delete(r.id) : next.add(r.id)));
    setSelected(next);
  };
  return (
    <>
      <div className="mt-3 flex flex-wrap gap-1.5">
        {platforms.map((p) => (
          <button key={p} className="chip" aria-pressed={active.has(p)} onClick={() => setActive((a) => (a.has(p) ? new Set([...a].filter((x) => x !== p)) : new Set([...a, p])))}>
            {p.toUpperCase()}
          </button>
        ))}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <button className="btn btn-sm" onClick={toggleAll}>
          Select all
        </button>
        <span className="ml-auto text-xs text-muted">{selected.size ? `${selected.size} selected` : ""}</span>
      </div>
      <div className="my-3 grid max-h-[40vh] gap-2.5 overflow-y-auto pr-1 [grid-template-columns:repeat(auto-fill,minmax(130px,1fr))]">
        {visible.length === 0 && <p className="col-span-full text-sm text-muted">Nothing to show.</p>}
        {visible.map((r) => {
          const on = selected.has(r.id);
          return (
            <button
              key={r.id}
              title={r.name}
              className={`card relative ${on ? "border-cyan" : ""}`}
              onClick={() => {
                const next = new Set(selected);
                if (on) next.delete(r.id);
                else next.add(r.id);
                setSelected(next);
              }}
            >
              <span className="well">{r.cover ? <img src={r.cover} alt="" loading="lazy" className="max-h-full max-w-full object-contain" /> : <img src="/assets/logo/brand.png" alt="" className="size-10 opacity-30" />}</span>
              <span className="absolute left-1 top-1 flex flex-wrap gap-1">
                <span className="badge">{r.extension.replace(/^\./, "").toUpperCase() || r.platform.toUpperCase()}</span>
                {tags?.(r)}
              </span>
              {on && <span className="absolute right-1 top-1 grid size-6 place-items-center rounded bg-cyan font-black text-on-cyan">✓</span>}
              <span className="grid gap-0.5 p-2">
                <strong className="text-xs leading-snug [overflow-wrap:anywhere]">{r.title}</strong>
                <span className="truncate text-[0.65rem] text-faint">{r.name}</span>
              </span>
            </button>
          );
        })}
      </div>
    </>
  );
}

type UploadJobState = { status: string; error?: string; filename?: string; download_url?: string; qr_url?: string; direction?: string; artifact_id?: string };

async function waitJob(kind: string, id: string) {
  for (;;) {
    const j = await api.get<UploadJobState>(`/api/upload/${kind}/${id}`);
    if (j.status !== "running") return j;
    await new Promise((r) => setTimeout(r, 1000));
  }
}

function FBIBatch({ onClose }: { onClose: () => void }) {
  const { roms, loading } = useRoms();
  const byId = useMemo(() => new Map(roms.map((r) => [r.id, r])), [roms]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [out, setOut] = useState<ReactNode>(null);
  const [convertAsk, setConvertAsk] = useState<string[] | null>(null);
  const target = useRef<HTMLInputElement>(null);
  const skipped = (asked: number, got: number) => (asked - got > 0 ? ` ${asked - got} of ${asked} selected files aren't FBI-installable CIAs and were left out.` : "");
  const convertible = (ids: string[]) => ids.filter((id) => {
    const r = byId.get(id);
    return r && !r.cia && (r.extension === ".3ds" || r.extension === ".cci" || (r.extension === ".cia" && r.encryption === "unencrypted"));
  });

  const build = async (ids: string[]) => {
    setOut("Building QR batch…");
    try {
      const r = await api.get<{ qr_codes: string[]; count: number; files: number }>(`/api/fbi/batches?ids=${encodeURIComponent(ids.join(","))}`);
      setOut(
        <>
          <p className="mb-2 w-full text-sm text-muted">
            {r.files === 0
              ? "None of the selected files are FBI-installable encrypted CIAs, so there's no QR to scan."
              : r.count === 1
                ? `One QR code installs all ${r.files} selected CIA${r.files === 1 ? "" : "s"}. Scan it in FBI › Remote Install › Scan QR Code.`
                : `${r.files} CIAs across ${r.count} QR codes, each installing several at once. Scan them in order in FBI › Remote Install › Scan QR Code.`}
            {skipped(ids.length, r.files)}
          </p>
          <div className="flex flex-wrap gap-3">
            {r.qr_codes.map((src, i) => (
              <figure key={src} className="m-0">
                <img src={src} alt={`FBI batch QR ${i + 1} of ${r.count}`} className="w-52 rounded bg-white p-2" />
                <figcaption className="text-center text-xs text-muted">{r.count === 1 ? `${r.files} CIAs` : `Scan ${i + 1} of ${r.count}`}</figcaption>
              </figure>
            ))}
          </div>
        </>,
      );
    } catch (e) {
      setOut((e as Error).message);
    }
  };
  const qr = () => {
    const ids = [...selected];
    if (!ids.length) return setOut("Select at least one ROM.");
    const extras = convertible(ids);
    if (extras.length) return setConvertAsk(extras);
    build(ids);
  };
  const convertThenBuild = async (extras: string[]) => {
    setConvertAsk(null);
    setOut(`Converting ${extras.length} file${extras.length === 1 ? "" : "s"}…`);
    const converted: string[] = [];
    try {
      for (const id of extras) {
        const r = byId.get(id)!;
        const isConvert = r.extension === ".3ds" || r.extension === ".cci";
        const kind = isConvert ? "convert" : "crypt";
        const start = await api.post<{ id: string }>(`/api/library/${kind}/${encodeURIComponent(id)}`, new URLSearchParams(isConvert ? {} : { direction: "encrypt" }));
        const job = await waitJob(kind, start.id);
        if (job.status !== "completed") throw new Error(`${r.name}: ${job.error || "conversion failed"}`);
        converted.push(job.artifact_id!);
      }
    } catch (e) {
      return setOut(`Conversion failed: ${(e as Error).message}`);
    }
    build([...selected].filter((id) => !extras.includes(id)).concat(converted));
  };
  const send = async (e: FormEvent) => {
    e.preventDefault();
    const ids = [...selected];
    if (!ids.length) return setOut("Select at least one ROM.");
    try {
      const r = await api.post<{ queued: number }>("/queue", new URLSearchParams({ target: target.current!.value, artifact_ids: ids.join(",") }));
      setOut(r.queued === 0 ? "None of the selected files are FBI-installable encrypted CIAs, so nothing was queued." : `${r.queued} CIA${r.queued === 1 ? "" : "s"} queued in FBI.${skipped(ids.length, r.queued)}`);
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Modal open onClose={onClose} label="3DS Batch Installer" wide>
      <span className="eyebrow">FBI · Remote install</span>
      <h2 className="pixel m-0 mt-2 text-sm">3DS Batch Installer</h2>
      <p className="mt-2 text-sm text-muted">Pick ROMs to send. Encrypted 3DS CIAs scan straight into FBI as a QR batch or over port 5000.</p>
      {loading ? (
        <Spinner />
      ) : (
        <MultiPick
          roms={roms}
          selected={selected}
          setSelected={setSelected}
          defaultPlatforms={["3ds"]}
          tags={(r) => (r.extension === ".cia" ? <span className={`badge ${r.cia ? "badge-leaf" : "badge-gold"}`}>{r.cia ? "🔒" : "🔓"}</span> : r.platform === "3ds" ? <span className="badge badge-gold">Not CIA</span> : null)}
        />
      )}
      <div className="flex flex-wrap items-center gap-2">
        <button className="btn btn-primary" onClick={qr} disabled={!selected.size}>
          Build batch QR
        </button>
        <form className="flex flex-1 flex-wrap gap-2" onSubmit={send}>
          <input ref={target} className="field max-w-xs" placeholder="FBI device IP, e.g. 192.168.1.55" aria-label="FBI device IP" required />
          <button className="btn">Send to FBI port 5000</button>
        </form>
      </div>
      {convertAsk && (
        <div className="mt-3 grid gap-2 rounded border-2 border-gold/50 p-3 text-sm">
          {convertAsk.length} of {selected.size} selected files aren't FBI-installable CIAs yet. Convert/encrypt them now, then build the QR?
          <div className="flex gap-2">
            <button className="btn btn-sm btn-primary" onClick={() => convertThenBuild(convertAsk)}>
              Convert, then build
            </button>
            <button className="btn btn-sm" onClick={() => (setConvertAsk(null), build([...selected]))}>
              Skip them
            </button>
          </div>
        </div>
      )}
      {out && <div className="mt-3 text-sm" aria-live="polite">{out}</div>}
    </Modal>
  );
}

type FtpEntry = { name: string; dir?: boolean; size?: number };

function FTPSend({ onClose }: { onClose: () => void }) {
  const { roms, loading } = useRoms();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [conn, setConn] = useState({ host: "", port: "5000", user: "", pass: "" });
  const [path, setPath] = useState<string | null>(null);
  const [entries, setEntries] = useState<FtpEntry[]>([]);
  const [out, setOut] = useState<ReactNode>(null);
  const browse = async (p: string) => {
    setOut(null);
    try {
      const r = await api.post<{ path?: string; entries?: FtpEntry[] }>("/api/ftp/browse", new URLSearchParams({ ...conn, path: p }));
      setPath(r.path || "");
      setEntries(r.entries ?? []);
    } catch (e) {
      setPath(null);
      setOut((e as Error).message);
    }
  };
  const send = async () => {
    setOut("Sending…");
    try {
      const r = await api.post<{ sent: number; results?: { name: string; error?: string }[] }>("/api/ftp/send", new URLSearchParams({ ...conn, path: path ?? "", artifact_ids: [...selected].join(",") }));
      const total = r.results?.length ?? 0;
      setOut(
        <>
          <p className="m-0 text-muted">{r.sent === total ? `All ${r.sent} files sent.` : `${r.sent} of ${total} files sent.`}</p>
          {r.results?.map((x) => (
            <p key={x.name} className={`m-0 ${x.error ? "text-danger" : ""}`}>
              {x.error ? "✗" : "✓"} {x.name}
              {x.error && `, ${x.error}`}
            </p>
          ))}
        </>,
      );
    } catch (e) {
      setOut((e as Error).message);
    }
  };
  return (
    <Modal open onClose={onClose} label="Send via FTP" wide>
      <span className="eyebrow">FTP · Remote transfer</span>
      <h2 className="pixel m-0 mt-2 text-sm">Send via FTP</h2>
      <p className="mt-2 text-sm text-muted">Pick files, connect to the FTP server on your console (e.g. ftpd), browse to a folder, and send. Only private-network addresses are allowed.</p>
      {loading ? <Spinner /> : <MultiPick roms={roms} selected={selected} setSelected={setSelected} />}
      <form
        className="panel grid gap-2.5 p-3 sm:grid-cols-4"
        onSubmit={(e) => {
          e.preventDefault();
          browse("");
        }}
      >
        <input className="field sm:col-span-2" placeholder="Host, e.g. 192.168.1.55" required value={conn.host} onChange={(e) => setConn({ ...conn, host: e.target.value })} aria-label="FTP host" />
        <input className="field" placeholder="Port" value={conn.port} onChange={(e) => setConn({ ...conn, port: e.target.value })} aria-label="FTP port" />
        <input className="field" placeholder="User (anonymous)" value={conn.user} onChange={(e) => setConn({ ...conn, user: e.target.value })} aria-label="FTP user" />
        <input className="field sm:col-span-3" type="password" placeholder="Password" value={conn.pass} onChange={(e) => setConn({ ...conn, pass: e.target.value })} aria-label="FTP password" autoComplete="off" />
        <button className="btn btn-primary">Connect</button>
      </form>
      {path !== null && (
        <div className="mt-3">
          <div className="text-sm text-muted">
            Folder: <code className="text-xs text-text">{path || "/"}</code>
          </div>
          <div className="mt-2 grid max-h-[30vh] gap-0.5 overflow-y-auto rounded border-2 border-line p-1">
            {path !== "" && (
              <button className="rounded px-2 py-1.5 text-left text-sm hover:bg-panel-2" onClick={() => browse(path.slice(0, path.lastIndexOf("/")))}>
                ⬆ ..
              </button>
            )}
            {entries.length === 0 && <p className="m-0 px-2 py-1.5 text-sm text-muted">This folder is empty.</p>}
            {entries.map((x) =>
              x.dir ? (
                <button key={x.name} className="truncate rounded px-2 py-1.5 text-left text-sm hover:bg-panel-2" onClick={() => browse(path === "" ? `/${x.name}` : `${path}/${x.name}`)}>
                  📁 {x.name}
                </button>
              ) : (
                <div key={x.name} className="flex gap-2 px-2 py-1.5 text-sm text-muted">
                  📄 <span className="truncate">{x.name}</span>
                  {x.size !== undefined && <span className="ml-auto text-xs text-faint">{bytes(x.size)}</span>}
                </div>
              ),
            )}
          </div>
          <button className="btn btn-primary mt-3" disabled={!selected.size} onClick={send}>
            Send {selected.size || ""} file{selected.size === 1 ? "" : "s"} here
          </button>
        </div>
      )}
      {out && <div className="mt-3 grid gap-1 text-sm" aria-live="polite">{out}</div>}
    </Modal>
  );
}

// UploadJob: Convert (.3ds/.cci -> CIA) or Encrypt/Decrypt, from an upload or
// a library file. Crypt asks for a direction only when the file is a genuine
// mix the server cannot decide alone.
function UploadJob({ kind, onClose }: { kind: "convert" | "crypt"; onClose: () => void }) {
  const exts = kind === "convert" ? [".3ds", ".cci"] : [".nds", ".srl", ".dsi", ".ids", ".3ds", ".cci", ".cia"];
  const { roms } = useRoms();
  const [source, setSource] = useState<"upload" | "library">("upload");
  const [platform, setPlatform] = useState("");
  const [pick, setPick] = useState("");
  const [retain, setRetain] = useState(false);
  const [needDirection, setNeedDirection] = useState("");
  const [out, setOut] = useState<ReactNode>(null);
  const file = useRef<HTMLInputElement>(null);
  const choices = roms.filter((r) => exts.includes(r.extension) && (!platform || r.platform === platform));

  const run = async (direction?: string) => {
    setNeedDirection("");
    setOut(kind === "convert" ? "Converting…" : "Working…");
    try {
      let start: { id: string };
      if (source === "library") {
        if (!pick) return setOut("Pick a file from the library.");
        const p = new URLSearchParams();
        if (retain) p.set("retain", "true");
        if (direction) p.set("direction", direction);
        start = await api.post(`/api/library/${kind}/${encodeURIComponent(pick)}`, p);
      } else {
        const f = file.current?.files?.[0];
        if (!f) return setOut("Choose a file first.");
        const body = new FormData();
        body.set("rom", f);
        if (retain) body.set("retain", "true");
        if (direction) body.set("direction", direction);
        start = await api.post(`/api/upload/${kind}`, body);
      }
      const job = await waitJob(kind, start.id);
      if (job.status !== "completed") return setOut(<span className="text-danger">{job.error || "the job failed"}</span>);
      setOut(
        <div className="grid justify-items-start gap-2">
          {job.direction && <span className="text-muted">{job.direction === "decrypt" ? "Decrypted" : "Encrypted"}: {job.filename}</span>}
          <a className="btn btn-primary" href={job.download_url}>
            <Icon d={icons.download} /> Download {job.filename}
          </a>
          {job.qr_url && <img src={job.qr_url} alt="Direct-download QR code" loading="lazy" className="w-40 rounded bg-white p-2" />}
        </div>,
      );
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        try {
          setNeedDirection(JSON.parse(e.message).message);
        } catch {
          setNeedDirection(e.message);
        }
        setOut(null);
        return;
      }
      setOut(<span className="text-danger">{(e as Error).message}</span>);
    }
  };
  return (
    <Modal open onClose={onClose} label={kind === "convert" ? "Convert to CIA" : "Encrypt / Decrypt"}>
      <span className="eyebrow">{kind === "convert" ? "3DS · Convert" : "3DS/DS · Encrypt or decrypt"}</span>
      <h2 className="pixel m-0 mt-2 text-sm">{kind === "convert" ? "Convert .3ds/.cci to CIA" : "Encrypt / Decrypt"}</h2>
      <p className="mt-2 text-sm text-muted">
        {kind === "convert" ? "Converts a raw 3DS cart dump to an encrypted CIA you can download or scan as a QR code." : "Cart dumps either direction, or a .cia (decrypt only). The direction is detected from the file."}
      </p>
      <div className="flex gap-1.5">
        {(["upload", "library"] as const).map((s) => (
          <button key={s} className="chip" aria-pressed={source === s} onClick={() => setSource(s)}>
            {s === "upload" ? "Upload a file" : "From library"}
          </button>
        ))}
      </div>
      <div className="mt-3 grid gap-3">
        {source === "upload" ? (
          <input ref={file} className="field" type="file" accept={exts.join(",")} aria-label="File" />
        ) : (
          <>
            {kind === "crypt" && (
              <div className="flex gap-1.5">
                {["", "3ds", "nds"].map((p) => (
                  <button key={p} className="chip" aria-pressed={platform === p} onClick={() => setPlatform(p)}>
                    {p ? p.toUpperCase() : "All"}
                  </button>
                ))}
              </div>
            )}
            <select className="field" value={pick} onChange={(e) => setPick(e.target.value)} aria-label="Library file">
              <option value="">Choose a file…</option>
              {choices.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.title} · {r.name}
                </option>
              ))}
            </select>
          </>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="size-4 accent-cyan" checked={retain} onChange={(e) => setRetain(e.target.checked)} />
          Keep permanently; otherwise it's removed after 24 hours.
        </label>
        <button className="btn btn-primary justify-self-start" onClick={() => run()}>
          {kind === "convert" ? "Convert" : "Run"}
        </button>
      </div>
      {needDirection && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted">{needDirection}</span>
          <button className="btn btn-sm" onClick={() => run("decrypt")}>
            Decrypt
          </button>
          <button className="btn btn-sm" onClick={() => run("encrypt")}>
            Encrypt
          </button>
        </div>
      )}
      {out && <div className="mt-3 text-sm">{out}</div>}
    </Modal>
  );
}

function UploadROM({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [out, setOut] = useState("");
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setOut("Uploading…");
    try {
      const r = await api.post<{ name: string }>("/api/library/upload", new FormData(e.currentTarget));
      setOut(`Added ${r.name}. Artwork and metadata follow shortly.`);
      onDone();
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Modal open onClose={onClose} label="Upload ROM">
      <span className="eyebrow">Library</span>
      <h2 className="pixel m-0 mt-2 text-sm">Upload ROM</h2>
      <p className="mt-2 text-sm text-muted">Adds a ROM straight into the library. It's scanned and hashed right away; artwork and metadata load shortly after.</p>
      <form className="grid gap-3" onSubmit={submit}>
        <input className="field" type="file" name="rom" required aria-label="ROM file" />
        <button className="btn btn-primary justify-self-start">Upload</button>
      </form>
      {out && <p className="mb-0 mt-3 text-sm text-muted">{out}</p>}
    </Modal>
  );
}
