import { useState, type FormEvent } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { OPTION_SCHEMA, type RField } from "../lib/randomizer-schema";
import { LibraryPicker, type Picked } from "../components/LibraryPicker";
import { Hero } from "../components/ui";
import { JobStatus, usePollJob } from "./Patcher";

const SUPPORTED = new Set(["gb", "gbc", "gba", "nds"]);
type Values = Record<string, boolean | number | string>;

function defaults(): Values {
  const v: Values = {};
  for (const c of OPTION_SCHEMA) {
    for (const f of c.fields ?? []) v[f.key] = f.default;
    for (const t of c.tweaks ?? []) v[t.key] = false;
  }
  return v;
}

function withExtension(name: string, ext: string) {
  const t = name.trim();
  if (!t || !ext) return t;
  return t.replace(/\.[^.]+$/, "") + ext;
}

// Randomizer: the Universal Pokemon Randomizer ZX options as a form, run by
// the randomizer sidecar against a copy of a library ROM or an upload.
export function Randomizer() {
  const { session } = useSession();
  const [values, setValues] = useState<Values>(defaults);
  const [picked, setPicked] = useState<Picked | null>(null);
  const [picking, setPicking] = useState(false);
  const [upload, setUpload] = useState<File | null>(null);
  const [output, setOutput] = useState("");
  const [jobURL, setJobURL] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const job = usePollJob(jobURL);
  const ext = picked ? `.${picked.extension.replace(/^\./, "")}` : upload ? `.${upload.name.split(".").pop()?.toLowerCase()}` : "";

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErr("");
    if (!picked && !upload) return setErr("Choose a ROM from the library or upload one.");
    const f = new FormData(e.currentTarget);
    f.set("output_filename", withExtension(output, ext));
    if (picked) {
      f.set("artifact_id", picked.id);
      f.delete("rom");
    }
    // Same shape the sidecar always read: plain fields, custom_starters as a
    // list, misc_tweaks as the chosen keys.
    const settings: Values & { custom_starters?: number[]; misc_tweaks?: string[] } = {};
    const starters: number[] = [];
    const tweaks: string[] = [];
    for (const c of OPTION_SCHEMA) {
      for (const t of c.tweaks ?? []) if (values[t.key]) tweaks.push(t.key);
      for (const fld of c.fields ?? []) {
        if (fld.key.startsWith("custom_starter_")) starters[Number(fld.key.slice(-1)) - 1] = Number(values[fld.key]);
        else settings[fld.key] = values[fld.key];
      }
    }
    if (starters.length) settings.custom_starters = starters;
    settings.misc_tweaks = tweaks;
    f.set("settings", JSON.stringify(settings));
    try {
      const j = await api.post<{ id: string }>("/api/randomize-jobs", f);
      setJobURL(`/api/randomize-jobs/${encodeURIComponent(j.id)}`);
    } catch (x) {
      setErr((x as Error).message);
    }
  };

  return (
    <>
      <Hero eyebrow="Pokémon tools" title="ROM Randomizer">
        Randomize a copy of a Game Boy, GBA or DS Pokémon game. Your original stays untouched; the result is a temporary library entry.
      </Hero>
      <form className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_340px]" onSubmit={submit}>
        <div className="grid gap-4">
          <section className="panel grid gap-3 p-5 sm:grid-cols-2">
            <span className="eyebrow sm:col-span-2">Base ROM</span>
            <div className="grid gap-1.5 text-sm">
              From your library
              <button type="button" className="btn justify-start" disabled={!!upload} onClick={() => setPicking(true)}>
                {picked ? `${picked.gameTitle} · ${picked.extension.toUpperCase()}` : "Browse library…"}
              </button>
            </div>
            <label className="grid gap-1.5 text-sm">
              Or upload a ROM
              <input
                className="field"
                type="file"
                name="rom"
                disabled={!!picked}
                accept=".gb,.gbc,.gba,.nds"
                onChange={(e) => {
                  const file = e.target.files?.[0] ?? null;
                  setUpload(file);
                  if (file && !output) setOutput(file.name.replace(/\.[^.]+$/, "") + " (Randomized)");
                }}
              />
            </label>
            <label className="grid gap-1.5 text-sm sm:col-span-2">
              Output filename
              <input className="field" value={output} onChange={(e) => setOutput(e.target.value)} placeholder="Pokemon Emerald (Randomized).gba" required />
            </label>
          </section>
          {OPTION_SCHEMA.map((c) => (
            <details key={c.id} className="panel p-4" open={c.id === "general"}>
              <summary className="pixel cursor-pointer text-[0.65rem] text-cyan">{c.title}</summary>
              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                {c.fields?.map((f) => <OptionField key={f.key} field={f} values={values} set={(v) => setValues({ ...values, [f.key]: v })} />)}
                {c.tweaks?.map((t) => (
                  <label key={t.key} className="flex items-center gap-2 text-sm">
                    <input type="checkbox" className="size-4 accent-cyan" checked={!!values[t.key]} onChange={(e) => setValues({ ...values, [t.key]: e.target.checked })} />
                    {t.label}
                  </label>
                ))}
              </div>
            </details>
          ))}
          <details className="panel p-4">
            <summary className="pixel cursor-pointer text-[0.65rem] text-cyan">Library entry details</summary>
            <div className="mt-4 grid gap-3 sm:grid-cols-2">
              {["title", "developer", "release_date", "genre", "region"].map((n) => (
                <label key={n} className="grid gap-1.5 text-sm capitalize">
                  {n.replace("_", " ")}
                  <input className="field" name={n} />
                </label>
              ))}
              <label className="grid gap-1.5 text-sm sm:col-span-2">
                Description
                <textarea className="field" name="description" rows={3} />
              </label>
            </div>
          </details>
        </div>
        <aside className="panel sticky top-20 grid gap-4 p-5">
          {session?.admin && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" name="retain" value="true" className="size-4 accent-cyan" />
              Keep permanently
            </label>
          )}
          {err && <p className="m-0 text-sm text-danger">{err}</p>}
          <button className="btn btn-primary">Randomize</button>
          <JobStatus job={job} empty="Randomizing can take a while, especially on Gen 5." />
        </aside>
      </form>
      <LibraryPicker
        open={picking}
        onClose={() => setPicking(false)}
        accept={(a) => SUPPORTED.has(a.platform.toLowerCase())}
        title="Pick a Pokémon game"
        onPick={(p) => {
          setPicked(p);
          if (!output) setOutput(`${p.gameTitle} (Randomized)`);
        }}
      />
    </>
  );
}

function OptionField({ field: f, values, set }: { field: RField; values: Values; set: (v: boolean | number | string) => void }) {
  const enabled = !f.when || values[f.when.key] === f.when.equals;
  if (f.type === "bool") {
    return (
      <label className={`flex items-center gap-2 text-sm ${enabled ? "" : "opacity-40"}`} title={f.help}>
        <input type="checkbox" className="size-4 accent-cyan" disabled={!enabled} checked={!!values[f.key]} onChange={(e) => set(e.target.checked)} />
        {f.label}
      </label>
    );
  }
  return (
    <label className={`grid gap-1.5 text-sm ${enabled ? "" : "opacity-40"}`} title={f.help}>
      {f.label}
      {f.type === "enum" ? (
        <select className="field" disabled={!enabled} value={String(values[f.key])} onChange={(e) => set(e.target.value)}>
          {f.options?.map((o) => (
            <option key={o} value={o}>
              {o.replaceAll("_", " ")}
            </option>
          ))}
        </select>
      ) : (
        <input className="field" type="number" disabled={!enabled} min={f.min} max={f.max} value={Number(values[f.key])} onChange={(e) => set(Number(e.target.value))} />
      )}
    </label>
  );
}
