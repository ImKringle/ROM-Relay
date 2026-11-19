import { useState, type FormEvent } from "react";
import { api, bytes } from "../../lib/api";
import { BackLink } from "../../components/BackLink";
import { ConfirmButton } from "../../components/ConfirmButton";
import { ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Source = { file: string; name: string; files: number; size: number };
type Entry = { name: string; size: number; platform?: string; expected: boolean };
type Pending = { path: string; entries?: Entry[]; clean: boolean; error?: string };
type Temp = { id: string; name: string; platform: string; expires_on?: string; retained: boolean };
type Data = { sources: Source[]; pending: Pending[]; extracted: { path: string; pruned_on: string }[]; temporary: Temp[] };

// TorrentSources: the admin's .torrent files, archives waiting for review
// (from torrents and RomM grabs), and temporary grabs that can be kept.
export function TorrentSources() {
  const { data, error, loading, reload } = useData(() => api.get<Data>("/api/admin/torrents"), []);
  const [msg, setMsg] = useState("");
  const upload = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    try {
      const r = await api.post<{ saved: string[] }>("/api/admin/torrents", new FormData(form));
      setMsg(`Saved ${r.saved.length} source${r.saved.length === 1 ? "" : "s"}.`);
      form.reset();
      reload();
    } catch (x) {
      setMsg((x as Error).message);
    }
  };
  const act = async (url: string, path: string) => (await api.post(url, new URLSearchParams({ path })), reload());

  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Downloads" title="Torrent Sources">
        Upload .torrent files users can grab single games from. Grabs never seed, and archives with unexpected contents wait here for review.
      </Hero>
      <form className="panel mb-6 flex flex-wrap items-center gap-3 p-4" onSubmit={upload}>
        <input className="field max-w-md" type="file" name="file" accept=".torrent" multiple required />
        <button className="btn btn-primary">Upload</button>
        {msg && <span className="text-sm text-muted">{msg}</span>}
      </form>
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading && !data ? (
        <Spinner />
      ) : data ? (
        <div className="grid gap-5">
          <section className="panel grid gap-2 p-5">
            <span className="eyebrow">Sources</span>
            {data.sources.length === 0 && <p className="m-0 text-sm text-muted">No sources yet.</p>}
            {data.sources.map((t) => (
              <div key={t.file} className="flex flex-wrap items-center justify-between gap-3 rounded border-2 border-line p-3">
                <div className="min-w-0">
                  <strong className="block truncate">{t.name}</strong>
                  <span className="text-xs text-muted">
                    {t.files.toLocaleString()} grabbable files · {bytes(t.size)} · {t.file}
                  </span>
                </div>
                <ConfirmButton label="Delete" confirm="Delete source?" onConfirm={async () => (await api.del(`/api/admin/torrents/${encodeURIComponent(t.file)}`), reload())} />
              </div>
            ))}
          </section>

          <section className="panel grid gap-2 p-5">
            <span className="eyebrow">Archives pending</span>
            {data.pending.length === 0 && <p className="m-0 text-sm text-muted">Nothing pending.</p>}
            {data.pending.map((a) => (
              <div key={a.path} className="grid gap-2 rounded border-2 border-line p-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <strong className="block text-sm [overflow-wrap:anywhere]">{a.path}</strong>
                    <span className={`text-xs ${a.clean || a.error ? "text-muted" : "text-danger"}`}>
                      {a.error ?? (a.clean ? "Contents look right · unzipping shortly" : "Needs review: unexpected files inside")} · ROMs go to library (
                      {a.path.startsWith("keep/") ? "retained" : "temporary, 7 days"})
                    </span>
                  </div>
                  <div className="flex gap-2">
                    {!a.error && (
                      <button className="btn btn-sm btn-primary" onClick={() => act("/api/admin/torrents/pending/extract", a.path)}>
                        Unpack ROMs
                      </button>
                    )}
                    <ConfirmButton label="Delete" confirm="Delete archive?" onConfirm={() => act("/api/admin/torrents/pending/delete", a.path)} />
                  </div>
                </div>
                {a.entries && a.entries.length > 0 && (
                  <ul className="m-0 grid gap-0.5 pl-5 text-xs">
                    {a.entries.map((e) => (
                      <li key={e.name} className={e.expected ? "text-muted" : "text-danger"}>
                        {e.name} · {bytes(e.size)} · {e.platform ? e.platform.toUpperCase() : e.expected ? "readme" : "unexpected"}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            ))}
            {data.extracted.map((a) => (
              <span key={a.path} className="text-xs text-muted">
                Unpacked: {a.path} · pruned {a.pruned_on}
              </span>
            ))}
          </section>

          <section className="panel grid gap-2 p-5">
            <span className="eyebrow">Temporary grabs</span>
            {data.temporary.length === 0 && <p className="m-0 text-sm text-muted">No temporary grabs.</p>}
            {data.temporary.map((t) => (
              <div key={t.id} className="flex flex-wrap items-center justify-between gap-3 rounded border-2 border-line p-3">
                <div className="min-w-0">
                  <span className="block text-sm [overflow-wrap:anywhere]">{t.name}</span>
                  <span className="text-xs text-muted">
                    {t.platform} · {t.retained ? "Retained" : t.expires_on ? `Expires ${t.expires_on}` : "Expiry starts once catalogued"}
                  </span>
                </div>
                {!t.retained && (
                  <button className="btn btn-sm" onClick={async () => (await api.post(`/api/admin/torrents/keep/${encodeURIComponent(t.id)}`), reload())}>
                    Keep
                  </button>
                )}
              </div>
            ))}
          </section>
        </div>
      ) : null}
    </>
  );
}
