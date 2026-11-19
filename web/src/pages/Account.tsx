import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { ConfirmButton } from "../components/ConfirmButton";
import { Empty, ErrorNote, Hero, Spinner, useData } from "../components/ui";
import { SecretReveal } from "../components/SecretReveal";

export function Account() {
  const { session } = useSession();
  return (
    <>
      <Hero eyebrow="Player profile" title={session?.username ?? "Account"}>
        Your password and the keys that let other apps reach your library.
      </Hero>
      <div className="grid items-start gap-6 lg:grid-cols-2">
        <PasswordForm />
        {session?.features.webdav && <WebDAVKeys />}
      </div>
    </>
  );
}

// PasswordPage is the standalone route a temporary password is forced to.
export function PasswordPage() {
  const { session } = useSession();
  return (
    <>
      <Hero eyebrow="Security" title="Change password">
        {session?.must_change_password ? "An admin set a temporary password for you. Choose your own to continue." : "Set a new password for your account."}
      </Hero>
      <div className="max-w-lg">
        <PasswordForm />
      </div>
    </>
  );
}

function PasswordForm() {
  const { session, refresh } = useSession();
  const navigate = useNavigate();
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const forced = session?.must_change_password;
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    try {
      await api.post("/api/account/password", { current: f.get("current") ?? "", password: f.get("password"), confirm: f.get("confirm") });
      form.reset();
      setMsg({ ok: true, text: "Password changed." });
      if (forced) {
        await refresh();
        navigate("/", { replace: true });
      }
    } catch (err) {
      setMsg({ ok: false, text: (err as Error).message });
    }
  };
  return (
    <form className="panel grid gap-4 p-5" onSubmit={submit}>
      <span className="eyebrow">Password</span>
      {!forced && (
        <label className="grid gap-1.5 text-sm">
          Current password
          <input className="field" name="current" type="password" autoComplete="current-password" required maxLength={72} />
        </label>
      )}
      <label className="grid gap-1.5 text-sm">
        New password
        <input className="field" name="password" type="password" autoComplete="new-password" required maxLength={72} />
      </label>
      <label className="grid gap-1.5 text-sm">
        Confirm new password
        <input className="field" name="confirm" type="password" autoComplete="new-password" required maxLength={72} />
      </label>
      {msg && (
        <p className={`m-0 text-sm ${msg.ok ? "text-leaf" : "text-danger"}`} role="status">
          {msg.text}
        </p>
      )}
      <button className="btn btn-primary justify-self-start">Save password</button>
    </form>
  );
}

type Key = { id: string; label: string; created_at: string; last_used_at?: string; revoked: boolean };

function WebDAVKeys() {
  const { data, error, loading, reload } = useData(() => api.get<{ keys: Key[]; url: string }>("/api/account/webdav-keys"), []);
  const [created, setCreated] = useState<{ token: string; url: string } | null>(null);
  const [err, setErr] = useState("");
  const create = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    try {
      const r = await api.post<{ token: string; url: string }>("/api/account/webdav-keys", new FormData(form));
      setCreated(r);
      form.reset();
      reload();
    } catch (x) {
      setErr((x as Error).message);
    }
  };
  return (
    <section className="panel grid gap-4 p-5">
      <div>
        <span className="eyebrow">WebDAV access</span>
        <p className="mb-0 mt-2 text-sm text-muted">
          Lets an emulator's "cloud drive" import browse and download your library, read-only. Each key is shown once and can be revoked any time.
        </p>
      </div>
      {created && <SecretReveal secret={created.token} extra={`Server: ${created.url} (any username)`} onDone={() => setCreated(null)} />}
      <form className="flex gap-2" onSubmit={create}>
        <input className="field" name="label" placeholder="Label, e.g. Delta on iPhone" required maxLength={80} />
        <button className="btn btn-primary">Create key</button>
      </form>
      {err && <p className="m-0 text-sm text-danger">{err}</p>}
      {error ? (
        <ErrorNote error={error} onRetry={reload} />
      ) : loading && !data ? (
        <Spinner />
      ) : !data?.keys.length ? (
        <Empty>No keys yet.</Empty>
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {data.keys.map((k) => (
            <li key={k.id} className={`flex flex-wrap items-center justify-between gap-3 rounded border-2 border-line p-3 ${k.revoked ? "opacity-50" : ""}`}>
              <div className="min-w-0">
                <strong className="text-sm">{k.label}</strong>
                <div className="text-xs text-muted">
                  Created {new Date(k.created_at).toLocaleDateString()} · {k.last_used_at ? `last used ${new Date(k.last_used_at).toLocaleString()}` : "never used"}
                </div>
              </div>
              {k.revoked ? (
                <span className="badge">Revoked</span>
              ) : (
                <ConfirmButton label="Revoke" confirm="Revoke now?" onConfirm={async () => (await api.del(`/api/account/webdav-keys/${encodeURIComponent(k.id)}`), reload())} />
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
