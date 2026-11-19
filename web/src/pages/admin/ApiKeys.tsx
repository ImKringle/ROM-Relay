import { useState, type FormEvent } from "react";
import { api } from "../../lib/api";
import { BackLink } from "../../components/BackLink";
import { ConfirmButton } from "../../components/ConfirmButton";
import { SecretReveal } from "../../components/SecretReveal";
import { Empty, ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Key = { id: string; name: string; created_by_username: string; created_at: string; last_used_at?: string; revoked: boolean };

export function ApiKeys() {
  const { data, error, loading, reload } = useData(() => api.get<{ keys: Key[] }>("/api/admin/api-keys"), []);
  const [token, setToken] = useState("");
  const [err, setErr] = useState("");
  const create = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    setErr("");
    try {
      setToken((await api.post<{ token: string }>("/api/admin/api-keys", new FormData(form))).token);
      form.reset();
      reload();
    } catch (x) {
      setErr((x as Error).message);
    }
  };
  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Integrations" title="API Keys">
        Scripts authenticate with <code>Authorization: Bearer &lt;key&gt;</code> and get the issuing admin's access. Keys are shown once and can be revoked any time.
      </Hero>
      <form className="panel mb-6 flex flex-wrap gap-2 p-4" onSubmit={create}>
        <input className="field min-w-60 flex-1" name="name" placeholder="Name, e.g. backup script" required maxLength={128} autoComplete="off" />
        <button className="btn btn-primary">Create key</button>
        {err && <p className="m-0 w-full text-sm text-danger">{err}</p>}
        {token && (
          <div className="w-full">
            <SecretReveal secret={token} onDone={() => setToken("")} />
          </div>
        )}
      </form>
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading && !data ? (
        <Spinner />
      ) : !data?.keys.length ? (
        <Empty>No API keys yet.</Empty>
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {data.keys.map((k) => (
            <li key={k.id} className={`panel flex flex-wrap items-center justify-between gap-3 p-4 ${k.revoked ? "opacity-50" : ""}`}>
              <div>
                <strong>{k.name}</strong> {k.revoked && <span className="badge">Revoked</span>}
                <div className="mt-1 text-xs text-muted">
                  Created {k.created_at} by {k.created_by_username} · {k.last_used_at ? `Last used ${k.last_used_at}` : "Never used"}
                </div>
              </div>
              {!k.revoked && <ConfirmButton label="Revoke" confirm="Revoke now?" onConfirm={async () => (await api.del(`/api/admin/api-keys/${encodeURIComponent(k.id)}`), reload())} />}
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
