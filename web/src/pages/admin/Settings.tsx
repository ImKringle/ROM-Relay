import { useState, type FormEvent } from "react";
import { api } from "../../lib/api";
import { BackLink } from "../../components/BackLink";
import { ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Field = { name: string; label: string; kind: "text" | "number" | "bool"; hint?: string };
const groups: { title: string; fields: Field[] }[] = [
  {
    title: "Library",
    fields: [
      { name: "scan_interval_hours", label: "Rescan every (hours)", kind: "number", hint: "0 turns periodic rescans off." },
      { name: "metadata_refresh_days", label: "Refresh metadata after (days)", kind: "number" },
    ],
  },
  {
    title: "Server",
    fields: [
      { name: "listen_address", label: "Listen address", kind: "text" },
      { name: "port", label: "Port", kind: "number" },
      { name: "public_base_url", label: "Public base URL", kind: "text", hint: "The https address behind a tunnel or reverse proxy. Used in QR codes and download links." },
      { name: "advertise_ip", label: "Advertise IP", kind: "text", hint: "LAN fallback when no public URL is set." },
      { name: "advertise_port", label: "Advertise port", kind: "number" },
    ],
  },
  {
    title: "Metadata",
    fields: [
      { name: "libretro_enabled", label: "Libretro database", kind: "bool" },
      { name: "libretro_auto_update", label: "Auto-update Libretro database", kind: "bool" },
      { name: "libretro_download_thumbnails", label: "Download Libretro box art", kind: "bool" },
      { name: "igdb_enabled", label: "IGDB (needs IGDB_CLIENT_ID/SECRET in .env)", kind: "bool" },
      { name: "pokeapi_enabled", label: "PokéAPI reference data", kind: "bool" },
    ],
  },
  {
    title: "Patching",
    fields: [
      { name: "patching_enabled", label: "Patch workspace", kind: "bool" },
      { name: "retention_hours", label: "Keep unretained output (hours)", kind: "number" },
      { name: "max_rom_size_mb", label: "Max ROM size (MB)", kind: "number" },
      { name: "max_patch_size_mb", label: "Max patch size (MB)", kind: "number" },
      { name: "timeout_seconds", label: "Patch timeout (seconds)", kind: "number" },
    ],
  },
  {
    title: "Remote search (SFTP)",
    fields: [
      { name: "remote_search_enabled", label: "Enabled (password in .env)", kind: "bool" },
      { name: "remote_search_host", label: "Host", kind: "text" },
      { name: "remote_search_port", label: "Port", kind: "number" },
      { name: "remote_search_username", label: "Username", kind: "text" },
      { name: "remote_search_root_directory", label: "Root directory", kind: "text" },
      { name: "remote_search_host_key_sha256", label: "Host key SHA-256", kind: "text", hint: "Pins the server's key; leave blank only on a trusted network." },
      { name: "remote_search_max_results", label: "Max results", kind: "number" },
      { name: "remote_search_max_file_size_mb", label: "Max file size (MB)", kind: "number" },
    ],
  },
];

// Settings: the allowlisted config.ini fields. Secrets stay in .env and are
// never shown here. Changes apply after a restart.
export function Settings() {
  const { data, error, loading, reload } = useData(() => api.get<Record<string, string | number | boolean>>("/api/admin/settings"), []);
  const [out, setOut] = useState<{ ok: boolean; text: string } | null>(null);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    // Unchecked boxes are absent from FormData; the server reads absent as false.
    for (const g of groups) for (const x of g.fields) if (x.kind === "bool") f.set(x.name, f.get(x.name) ? "true" : "false");
    setOut({ ok: true, text: "Saving…" });
    try {
      const r = await api.post<{ detail?: string }>("/api/admin/settings", f);
      setOut({ ok: true, text: r.detail || "Saved." });
    } catch (x) {
      setOut({ ok: false, text: (x as Error).message });
    }
  };
  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Configuration" title="Settings">
        Saved to config.ini. Secrets (tokens, passwords, API keys) live in .env and never appear here. Restart ROM Relay to apply.
      </Hero>
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading || !data ? (
        <Spinner />
      ) : (
        <form className="grid gap-5" onSubmit={submit}>
          {groups.map((g) => (
            <fieldset key={g.title} className="panel grid gap-4 p-5 sm:grid-cols-2">
              <legend className="eyebrow px-1">{g.title}</legend>
              {g.fields.map((x) =>
                x.kind === "bool" ? (
                  <label key={x.name} className="flex items-center gap-2.5 text-sm">
                    <input type="checkbox" name={x.name} defaultChecked={!!data[x.name]} className="size-4 accent-cyan" />
                    {x.label}
                  </label>
                ) : (
                  <label key={x.name} className="grid gap-1.5 text-sm">
                    {x.label}
                    <input className="field" name={x.name} type={x.kind === "number" ? "number" : "text"} defaultValue={String(data[x.name] ?? "")} />
                    {x.hint && <span className="text-xs text-faint">{x.hint}</span>}
                  </label>
                ),
              )}
            </fieldset>
          ))}
          <div className="flex items-center gap-3">
            <button className="btn btn-primary">Save settings</button>
            {out && <span className={`text-sm ${out.ok ? "text-leaf" : "text-danger"}`}>{out.text}</span>}
          </div>
        </form>
      )}
    </>
  );
}
