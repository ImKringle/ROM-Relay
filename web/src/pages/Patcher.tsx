import { useEffect, useState, type FormEvent } from "react";
import { api } from "../lib/api";
import { LibraryPicker, type Picked } from "../components/LibraryPicker";
import { Hero, Icon, icons, useData } from "../components/ui";

type Recipe = { id: string; platform: string; title: string; extension: string; output_filename?: string };
type Job = { id: string; status: string; error?: string; result_artifact_id?: string };

// usePollJob polls a job URL every second until it leaves queued/running.
export function usePollJob(url: string | null) {
  const [job, setJob] = useState<Job | null>(null);
  useEffect(() => {
    if (!url) return;
    let stop = false;
    const tick = async () => {
      try {
        const j = await api.get<Job>(url);
        if (stop) return;
        setJob(j);
        if (j.status === "queued" || j.status === "running") setTimeout(tick, 1000);
      } catch (e) {
        if (!stop) setJob({ id: "", status: "failed", error: (e as Error).message });
      }
    };
    tick();
    return () => {
      stop = true;
    };
  }, [url]);
  return job;
}

export function JobStatus({ job, empty }: { job: Job | null; empty: string }) {
  if (!job) return <p className="m-0 text-sm text-muted">{empty}</p>;
  const running = job.status === "queued" || job.status === "running";
  return (
    <div className="grid gap-2 text-sm" aria-live="polite">
      <span className={job.status === "failed" ? "text-danger" : running ? "text-cyan" : "text-leaf"}>
        <span className="pixel text-[0.6rem]">{job.status.toUpperCase()}</span>
        {running && "…"}
      </span>
      {job.error && <span className="text-danger [overflow-wrap:anywhere]">{job.error}</span>}
      {job.status === "completed" && job.result_artifact_id && (
        <a className="btn btn-primary justify-self-start" href={`/d/${job.result_artifact_id}`}>
          <Icon d={icons.download} /> Download result
        </a>
      )}
    </div>
  );
}

// Patcher: base ROM (library pick or upload) + recipe or patch file -> a
// temporary patched catalog entry.
export function Patcher() {
  const recipes = useData(() => api.get<{ patch_recipes: Recipe[] }>("/api/patch-files").then((r) => r.patch_recipes ?? []), []);
  const [picked, setPicked] = useState<Picked | null>(null);
  const [picking, setPicking] = useState(false);
  const [romFile, setRomFile] = useState(false);
  const [recipe, setRecipe] = useState("");
  const [patchFile, setPatchFile] = useState(false);
  const [output, setOutput] = useState("");
  const [jobURL, setJobURL] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const job = usePollJob(jobURL);

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErr("");
    if (!picked && !romFile) return setErr("Choose a ROM from the library or upload one.");
    if (!recipe && !patchFile) return setErr("Choose a recipe or upload a patch.");
    const f = new FormData(e.currentTarget);
    if (picked) {
      f.set("artifact_id", picked.id);
      f.delete("rom");
    }
    if (recipe) f.delete("patch");
    try {
      const j = await api.post<Job>("/api/patch-jobs", f);
      setJobURL(`/api/patch-jobs/${encodeURIComponent(j.id)}`);
    } catch (x) {
      setErr((x as Error).message);
    }
  };

  return (
    <>
      <Hero eyebrow="Temporary workshop" title="ROM Patcher">
        Pick a clean base ROM from your library or upload one, choose a recipe or your own patch, and ROM Relay makes a separate temporary catalog entry. A library pick is only copied; an upload is deleted after the job.
      </Hero>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <form className="grid gap-5" onSubmit={submit}>
          <Step n={1} title="Clean base ROM">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5 text-sm">
                From your library
                <button type="button" className="btn justify-start" disabled={romFile} onClick={() => setPicking(true)}>
                  {picked ? `${picked.gameTitle} · ${picked.extension.toUpperCase()}` : "Browse library…"}
                </button>
                {picked && (
                  <button type="button" className="justify-self-start text-xs text-cyan" onClick={() => setPicked(null)}>
                    Clear
                  </button>
                )}
              </div>
              <label className="grid gap-1.5 text-sm">
                Or upload a ROM
                <input className="field" type="file" name="rom" disabled={!!picked} onChange={(e) => setRomFile(!!e.target.files?.length)} />
                <span className="text-xs text-faint">Use the exact revision the patch expects.</span>
              </label>
            </div>
          </Step>
          <Step n={2} title="Patch source">
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="grid gap-1.5 text-sm">
                Server recipe
                <select
                  className="field"
                  name="patch_id"
                  value={recipe}
                  disabled={patchFile}
                  onChange={(e) => {
                    setRecipe(e.target.value);
                    const r = recipes.data?.find((x) => x.id === e.target.value);
                    if (r?.output_filename && !output) setOutput(r.output_filename);
                  }}
                >
                  <option value="">Upload my own patch</option>
                  {recipes.data?.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.platform.toUpperCase()} · {r.title} · {r.extension.toUpperCase()}
                    </option>
                  ))}
                </select>
              </label>
              <label className="grid gap-1.5 text-sm">
                Your patch file
                <input className="field" type="file" name="patch" accept=".ips,.ups,.bps,.ppf,.rup,.aps,.bdf,.pmsr,.vcdiff" disabled={!!recipe} onChange={(e) => setPatchFile(!!e.target.files?.length)} />
              </label>
            </div>
          </Step>
          <Step n={3} title="Name the patched entry">
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="grid gap-1.5 text-sm">
                Output filename *
                <input className="field" name="output_filename" placeholder="Pokemon Radical Red.gba" value={output} onChange={(e) => setOutput(e.target.value)} />
              </label>
              <label className="grid gap-1.5 text-sm">
                Display title
                <input className="field" name="title" placeholder="Pokemon Radical Red" />
              </label>
              <label className="grid gap-1.5 text-sm">
                Developer / patch author
                <input className="field" name="developer" />
              </label>
              <label className="grid gap-1.5 text-sm">
                Release date
                <input className="field" name="release_date" />
              </label>
              <label className="grid gap-1.5 text-sm">
                Genre
                <input className="field" name="genre" />
              </label>
              <label className="grid gap-1.5 text-sm">
                Region
                <input className="field" name="region" placeholder="World" />
              </label>
              <label className="grid gap-1.5 text-sm sm:col-span-2">
                Description
                <textarea className="field" name="description" rows={4} placeholder="What does this patch change?" />
              </label>
              <label className="grid gap-1.5 text-sm sm:col-span-2">
                Cover artwork
                <input className="field" type="file" name="cover" accept="image/png,image/jpeg,image/webp" />
                <span className="text-xs text-faint">Optional PNG, JPEG or WebP up to 8 MiB.</span>
              </label>
            </div>
          </Step>
          {err && <p className="m-0 text-sm text-danger">{err}</p>}
          <button className="btn btn-primary justify-self-start">Create temporary patched ROM</button>
        </form>
        <aside className="panel sticky top-20 grid gap-3 p-5">
          <span className="eyebrow">Job</span>
          <JobStatus job={job} empty="No patch job submitted yet." />
          <ol className="m-0 grid gap-1 pl-5 text-xs text-muted">
            <li>Your base ROM is copied into private staging.</li>
            <li>The patch runs against that copy, never your source.</li>
            <li>The output is validated and gets a Patched badge.</li>
            <li>It expires unless an admin keeps it.</li>
          </ol>
        </aside>
      </div>
      <LibraryPicker open={picking} onClose={() => setPicking(false)} onPick={setPicked} />
    </>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <fieldset className="panel grid gap-4 p-5">
      <legend className="sr-only">{title}</legend>
      <div>
        <span className="eyebrow">Step {n}</span>
        <h2 className="m-0 mt-1 text-lg font-bold">{title}</h2>
      </div>
      {children}
    </fieldset>
  );
}
