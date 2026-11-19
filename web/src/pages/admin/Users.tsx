import { useState, type FormEvent } from "react";
import { api } from "../../lib/api";
import { useSession } from "../../lib/session";
import { BackLink } from "../../components/BackLink";
import { ConfirmButton } from "../../components/ConfirmButton";
import { SecretReveal } from "../../components/SecretReveal";
import { ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type User = { id: string; username: string; role: string; disabled: boolean; must_change_password: boolean; created_at: string; saves: number; pokemon: number };
type Issued = { username: string; temporary_password: string; detail?: string };

export function Users() {
  const { session } = useSession();
  const { data, error, loading, reload } = useData(() => api.get<{ users: User[] }>("/api/admin/users"), []);
  const [issued, setIssued] = useState<Issued | null>(null);
  const [formErr, setFormErr] = useState("");
  const [err, setErr] = useState("");

  const create = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    setFormErr("");
    try {
      setIssued(await api.post<Issued>("/api/admin/users", new FormData(form)));
      form.reset();
      reload();
    } catch (x) {
      setFormErr((x as Error).message);
    }
  };
  const act = async (fn: () => Promise<unknown>) => {
    setErr("");
    try {
      await fn();
      reload();
    } catch (x) {
      setErr((x as Error).message);
    }
  };

  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Accounts" title="Users">
        New accounts get a temporary password they must change at first sign-in.
      </Hero>
      <div className="grid items-start gap-6 lg:grid-cols-[340px_minmax(0,1fr)]">
        <form className="panel grid gap-3 p-5" onSubmit={create}>
          <span className="eyebrow">New user</span>
          <label className="grid gap-1.5 text-sm">
            Username
            <input className="field" name="username" required maxLength={64} autoComplete="off" pattern="[A-Za-z0-9_.@-]+" />
          </label>
          <label className="grid gap-1.5 text-sm">
            Role
            <select className="field" name="role" defaultValue="user">
              <option value="user">User</option>
              <option value="admin">Admin</option>
            </select>
          </label>
          {formErr && <p className="m-0 text-sm text-danger">{formErr}</p>}
          <button className="btn btn-primary">Create</button>
          {issued && <SecretReveal secret={issued.temporary_password} extra={`Temporary password for ${issued.username}. ${issued.detail ?? ""}`} onDone={() => setIssued(null)} />}
        </form>
        <section className="grid gap-2">
          {err && <ErrorNote error={err} />}
          {error ? (
            <ErrorNote error={error} onRetry={reload} />
          ) : loading && !data ? (
            <Spinner />
          ) : (
            data?.users.map((u) => {
              const owns = `${u.saves} save${u.saves === 1 ? "" : "s"} · ${u.pokemon} Pokémon`;
              const self = u.username === session?.username;
              const id = encodeURIComponent(u.id);
              return (
                <div key={u.id} className={`panel flex flex-wrap items-center gap-3 p-4 ${u.disabled ? "opacity-60" : ""}`}>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <strong>{u.username}</strong>
                      {u.role === "admin" && <span className="badge badge-cyan">Admin</span>}
                      {u.disabled && <span className="badge">Disabled</span>}
                      {u.must_change_password && <span className="badge badge-gold">Must change password</span>}
                      {self && <span className="badge">You</span>}
                    </div>
                    <div className="mt-1 text-xs text-muted">
                      Created {u.created_at} · {owns}
                    </div>
                  </div>
                  <select
                    className="field w-auto py-1.5 text-sm"
                    aria-label={`Role for ${u.username}`}
                    value={u.role}
                    disabled={self}
                    onChange={(e) => act(() => api.post(`/api/admin/users/${id}/role`, new URLSearchParams({ role: e.target.value })))}
                  >
                    <option value="user">User</option>
                    <option value="admin">Admin</option>
                  </select>
                  <button className="btn btn-sm" disabled={self} onClick={() => act(() => api.post(`/api/admin/users/${id}/disabled`, new URLSearchParams({ disabled: String(!u.disabled) })))}>
                    {u.disabled ? "Enable" : "Disable"}
                  </button>
                  <ConfirmButton
                    label="Reset password"
                    confirm="Issue new password?"
                    onConfirm={async () => setIssued(await api.post<Issued>(`/api/admin/users/${id}/reset-password`))}
                  />
                  {u.pokemon > 0 && <ConfirmButton label="Clear Pokédex" confirm={`Clear ${u.pokemon} Pokémon?`} onConfirm={() => act(() => api.post(`/api/admin/users/${id}/clear-dex`))} />}
                  {!self && <ConfirmButton label="Delete" confirm={u.saves || u.pokemon ? `Delete with ${owns}?` : "Delete user?"} onConfirm={() => act(() => api.del(`/api/admin/users/${id}`))} />}
                </div>
              );
            })
          )}
        </section>
      </div>
    </>
  );
}
