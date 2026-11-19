import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { api, bytes } from "../../lib/api";
import { useSession } from "../../lib/session";
import { BackLink } from "../../components/BackLink";
import { Hero, useData } from "../../components/ui";
import { JobStatus, usePollJob } from "../Patcher";

type Artifact = { id: string; name: string; extension: string };
type Game = {
  id: string;
  title: string;
  platform: string;
  rom_patch?: boolean;
  encryption?: string;
  artwork_url?: string;
  metadata_source?: string;
  artifacts?: Artifact[];
  [k: string]: unknown;
};
type Recipe = { id: string; platform: string; title: string; output_filename?: string };

const PLATFORMS = ["gb", "gbc", "gba", "nds", "3ds", "nes", "snes", "n64", "genesis", "gc", "ps1", "ps2", "psp", "switch"];

// PatchWorkspace: the admin's workbench: permanent patched builds, metadata
// overrides, keeping temporary results, 3DS conversion, remote (SFTP) import,
// and Pokédex maintenance.
export function PatchWorkspace() {
  const { session } = useSession();
  const games = useData(() => api.get<{ games: Game[] }>("/api/admin/games").then((r) => r.games ?? []), []);
  const sections = [
    ["create", "Create patched ROM"],
    ["metadata", "Metadata & art"],
    ["keep", "Keep a result"],
    ["convert", "3DS → CIA"],
    ...(session?.features.remote ? [["remote", "Remote library"]] : []),
    ["dex", "Pokédex maintenance"],
  ];
  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Workbench" title="Patch Workspace">
        Build permanent patched entries, correct metadata and artwork, import from the remote library, and control retention.
      </Hero>
      <nav className="mb-6 flex flex-wrap gap-1.5" aria-label="Sections">
        {sections.map(([id, label]) => (
          <a key={id} href={`#${id}`} className="chip no-underline">
            {label}
          </a>
        ))}
      </nav>
      <div className="grid gap-6">
        <CreatePatch games={games.data ?? []} />
        <Metadata games={games.data ?? []} onSaved={games.reload} />
        <Keep />
        <Convert games={games.data ?? []} />
        {session?.features.remote && <Remote />}
        <DexMaintenance />
      </div>
    </>
  );
}

function Section({ id, eyebrow, title, children }: { id: string; eyebrow: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="panel scroll-mt-20 p-[clamp(18px,3vw,28px)]">
      <span className="eyebrow">{eyebrow}</span>
      <h2 className="mb-4 mt-2 text-lg font-bold">{title}</h2>
      {children}
    </section>
  );
}

const Out = ({ children }: { children: ReactNode }) => (children ? <div className="mt-3 rounded border-2 border-line bg-void p-3 text-sm [overflow-wrap:anywhere]">{children}</div> : null);

function CreatePatch({ games }: { games: Game[] }) {
  const recipes = useData(() => api.get<{ patch_recipes: Recipe[] }>("/api/admin/patch-files").then((r) => r.patch_recipes ?? []), []);
  const choices = useMemo(() => games.flatMap((g) => (g.artifacts ?? []).map((a) => ({ id: a.id, label: `${g.platform.toUpperCase()} · ${g.title} · ${a.name}` }))), [games]);
  const [label, setLabel] = useState("");
  const [output, setOutput] = useState("");
  const [title, setTitle] = useState("");
  const [jobURL, setJobURL] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const job = usePollJob(jobURL);
  const artifact = choices.find((c) => c.label === label);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErr("");
    if (!artifact) return setErr("Choose an exact base-ROM suggestion first.");
    const f = new FormData(e.currentTarget);
    f.set("artifact_id", artifact.id);
    try {
      const j = await api.post<{ id: string }>("/api/admin/patch-jobs", f);
      setJobURL(`/api/admin/patch-jobs/${encodeURIComponent(j.id)}`);
    } catch (x) {
      setErr((x as Error).message);
    }
  };
  return (
    <Section id="create" eyebrow="Permanent build" title="Create a patched ROM">
      <form className="grid gap-3 sm:grid-cols-2" onSubmit={submit}>
        <label className="grid gap-1.5 text-sm sm:col-span-2">
          Base ROM
          <input className="field" list="ws-artifacts" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Start typing a title…" />
          <datalist id="ws-artifacts">
            {choices.map((c) => (
              <option key={c.id} value={c.label} />
            ))}
          </datalist>
          <span className={`text-xs ${artifact ? "text-leaf" : "text-faint"}`}>{artifact ? "Selected." : "Choose an exact suggestion from the list."}</span>
        </label>
        <label className="grid gap-1.5 text-sm">
          Server recipe
          <select
            className="field"
            name="patch_id"
            onChange={(e) => {
              const r = recipes.data?.find((x) => x.id === e.target.value);
              if (r?.output_filename && !output) setOutput(r.output_filename);
              if (r?.title && !title) setTitle(r.title);
            }}
          >
            <option value="">Upload a one-job patch instead</option>
            {recipes.data?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.platform.toUpperCase()} · {r.title} · {r.id}
              </option>
            ))}
          </select>
        </label>
        <label className="grid gap-1.5 text-sm">
          Patch file
          <input className="field" type="file" name="patch" />
        </label>
        <label className="grid gap-1.5 text-sm sm:col-span-2">
          Or a relative path under the patch inbox (migration only)
          <input className="field" name="local_patch" placeholder="folder/file.ips" />
        </label>
        <label className="grid gap-1.5 text-sm">
          Output filename
          <input className="field" name="output_filename" value={output} onChange={(e) => setOutput(e.target.value)} />
        </label>
        <label className="grid gap-1.5 text-sm">
          Title
          <input className="field" name="title" value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        {["developer", "publisher", "release_date", "genre", "region"].map((n) => (
          <label key={n} className="grid gap-1.5 text-sm capitalize">
            {n.replace("_", " ")}
            <input className="field" name={n} />
          </label>
        ))}
        <label className="grid gap-1.5 text-sm">
          Cover
          <input className="field" type="file" name="cover" accept="image/png,image/jpeg,image/webp" />
        </label>
        <label className="grid gap-1.5 text-sm sm:col-span-2">
          Description
          <textarea className="field" name="description" rows={3} />
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" name="retain" value="true" defaultChecked className="size-4 accent-cyan" /> Keep permanently
        </label>
        <button className="btn btn-primary justify-self-start">Create patched ROM</button>
      </form>
      {err && <p className="mb-0 mt-3 text-sm text-danger">{err}</p>}
      {jobURL && (
        <div className="mt-3">
          <JobStatus job={job} empty="" />
        </div>
      )}
    </Section>
  );
}

const META = ["title", "developer", "publisher", "release_date", "genre", "region", "players", "rating"];

function Metadata({ games, onSaved }: { games: Game[]; onSaved: () => void }) {
  const [id, setId] = useState("");
  const [out, setOut] = useState("");
  const [version, setVersion] = useState(0);
  const g = games.find((x) => x.id === id);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!id) return;
    setOut("Saving manual override…");
    try {
      await api.post(`/api/admin/games/${encodeURIComponent(id)}/metadata`, new FormData(e.currentTarget));
      setOut("Saved. Providers can no longer overwrite this entry.");
      onSaved();
      setVersion((v) => v + 1);
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Section id="metadata" eyebrow="Catalog" title="Edit game metadata and artwork">
      <select className="field mb-4 max-w-xl" value={id} onChange={(e) => (setId(e.target.value), setOut(""))} aria-label="Game">
        <option value="">Choose a game…</option>
        {games.map((x) => (
          <option key={x.id} value={x.id}>
            {x.platform.toUpperCase()} · {x.title}
            {x.rom_patch ? " · PATCHED" : ""}
          </option>
        ))}
      </select>
      {g && (
        <form key={`${g.id}:${version}`} className="grid items-start gap-5 md:grid-cols-[160px_minmax(0,1fr)]" onSubmit={submit}>
          <div className="grid gap-2">
            {g.artwork_url ? <img src={`/media/${encodeURIComponent(g.id)}/cover?v=${version}`} alt="" className="aspect-[3/4] w-full rounded object-cover" /> : <div className="well rounded text-xs text-faint">No art</div>}
            <div className="flex flex-wrap gap-1">
              <span className="badge">{g.platform.toUpperCase()}</span>
              {g.rom_patch && <span className="badge badge-leaf">Patched</span>}
              {g.encryption && <span className="badge badge-cyan">{g.encryption} CIA</span>}
            </div>
            <span className="text-xs text-faint">Source: {g.metadata_source || "local"}</span>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {META.map((n) => (
              <label key={n} className="grid gap-1.5 text-sm capitalize">
                {n.replace("_", " ")}
                <input className="field" name={n} defaultValue={String(g[n] ?? "")} />
              </label>
            ))}
            <label className="grid gap-1.5 text-sm sm:col-span-2">
              Description
              <textarea className="field" name="description" rows={4} defaultValue={String(g.description ?? "")} />
            </label>
            <label className="grid gap-1.5 text-sm">
              New cover
              <input className="field" type="file" name="cover" accept="image/png,image/jpeg,image/webp" />
            </label>
            <label className="flex items-center gap-2 self-end pb-2 text-sm">
              <input type="checkbox" name="remove_cover" value="true" className="size-4 accent-cyan" /> Remove current cover
            </label>
            <button className="btn btn-primary justify-self-start">Save override</button>
          </div>
        </form>
      )}
      <Out>{out}</Out>
    </Section>
  );
}

function Keep() {
  const [out, setOut] = useState("");
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const id = String(new FormData(e.currentTarget).get("job_id")).trim();
    try {
      await api.post(`/api/admin/patch-jobs/${encodeURIComponent(id)}/retain`);
      setOut(`Permanent retention enabled for ${id}.`);
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Section id="keep" eyebrow="Retention" title="Keep a temporary result">
      <form className="flex flex-wrap gap-2" onSubmit={submit}>
        <input className="field max-w-md" name="job_id" placeholder="Patch job ID" required />
        <button className="btn btn-primary">Keep permanently</button>
      </form>
      <Out>{out}</Out>
    </Section>
  );
}

function Convert({ games }: { games: Game[] }) {
  const [out, setOut] = useState("");
  const files = games.flatMap((g) => (g.artifacts ?? []).filter((a) => [".3ds", ".cci"].includes(a.extension.toLowerCase())).map((a) => ({ id: a.id, label: `${g.title} · ${a.name}` })));
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const id = String(new FormData(e.currentTarget).get("artifact_id"));
    if (!id) return;
    try {
      await api.post(`/api/admin/convert/${encodeURIComponent(id)}`);
      setOut("Conversion queued. Progress shows in the activity tray.");
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Section id="convert" eyebrow="3DS" title="Convert 3DS / CCI to CIA">
      <form className="flex flex-wrap gap-2" onSubmit={submit}>
        <select className="field max-w-xl" name="artifact_id" aria-label="File">
          <option value="">Choose a .3ds or .cci file…</option>
          {files.map((f) => (
            <option key={f.id} value={f.id}>
              {f.label}
            </option>
          ))}
        </select>
        <button className="btn btn-primary">Convert</button>
      </form>
      <Out>{out}</Out>
    </Section>
  );
}

type RemoteResult = { ID: string; Platform: string; Name: string; Size: number };

function Remote() {
  const [params] = useSearchParams();
  const [results, setResults] = useState<RemoteResult[] | null>(null);
  const [resultId, setResultId] = useState("");
  const [out, setOut] = useState("");
  const search = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setOut("Searching configured SFTP…");
    try {
      const f = new FormData(e.currentTarget);
      const r = await api.get<{ results: RemoteResult[] }>(`/api/admin/remote-search?${new URLSearchParams({ platform: String(f.get("platform")), q: String(f.get("q")) })}`);
      setResults(r.results ?? []);
      setOut("");
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  const fetchIt = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setOut("Downloading and validating…");
    try {
      const r = await api.post<unknown>("/api/admin/remote-jobs", new FormData(e.currentTarget));
      setOut(typeof r === "string" ? r : `Queued: ${JSON.stringify(r)}`);
    } catch (x) {
      setOut((x as Error).message);
    }
  };
  return (
    <Section id="remote" eyebrow="SFTP" title="Search remote library">
      <form className="grid items-end gap-3 sm:grid-cols-[160px_minmax(0,1fr)_auto]" onSubmit={search}>
        <label className="grid gap-1.5 text-sm">
          Platform
          <select className="field" name="platform">
            {PLATFORMS.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </label>
        <label className="grid gap-1.5 text-sm">
          Title search
          <input className="field" name="q" required minLength={2} defaultValue={params.get("remote_q") ?? ""} placeholder="Pokemon Emerald" />
        </label>
        <button className="btn btn-primary">Search SFTP</button>
      </form>
      {results && (
        <div className="mt-3 grid max-h-72 gap-1 overflow-y-auto">
          {results.length === 0 && <p className="text-sm text-muted">No results.</p>}
          {results.map((r) => (
            <button key={r.ID} className={`flex justify-between gap-2 rounded border-2 p-2 text-left text-sm ${resultId === r.ID ? "border-cyan" : "border-line"}`} onClick={() => setResultId(r.ID)}>
              <span className="[overflow-wrap:anywhere]">{r.Name}</span>
              <span className="shrink-0 text-xs text-muted">
                {r.Platform} · {bytes(r.Size)}
              </span>
            </button>
          ))}
        </div>
      )}
      <form className="mt-4 grid items-end gap-3 border-t-2 border-line pt-4 sm:grid-cols-2" onSubmit={fetchIt}>
        <label className="grid gap-1.5 text-sm">
          Selected result ID
          <input className="field" name="result_id" required value={resultId} onChange={(e) => setResultId(e.target.value)} />
        </label>
        <label className="grid gap-1.5 text-sm">
          Remote 3DS CIA type
          <select className="field" name="encryption" defaultValue="unencrypted">
            <option value="unencrypted">Unencrypted / mobile emulator</option>
            <option value="encrypted">Encrypted / FBI and real 3DS</option>
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" name="retain" value="true" className="size-4 accent-cyan" /> Keep permanently after validation
        </label>
        <label className="grid gap-1.5 text-sm">
          Permanent destination
          <input className="field" name="destination" placeholder="gba or 3ds/cia/unencrypted" />
        </label>
        <button className="btn btn-primary justify-self-start">Download and validate</button>
      </form>
      <Out>{out}</Out>
    </Section>
  );
}

function DexMaintenance() {
  const [out, setOut] = useState("");
  return (
    <Section id="dex" eyebrow="Pokédex" title="Pokédex maintenance">
      <p className="mt-0 text-sm text-muted">Removes stored Pokémon whose source save no longer exists, for every user. PokeHome deposits are kept.</p>
      <button
        className="btn"
        onClick={async () => {
          setOut("Pruning…");
          try {
            const r = await api.post<{ removed: number }>("/api/admin/dex/prune");
            setOut(r.removed === 0 ? "Nothing to prune: every stored Pokémon still has its save." : `Removed ${r.removed} orphaned Pokémon.`);
          } catch (x) {
            setOut((x as Error).message);
          }
        }}
      >
        Prune orphaned Pokémon
      </button>
      <Out>{out}</Out>
    </Section>
  );
}
