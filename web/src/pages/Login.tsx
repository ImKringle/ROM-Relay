import { useState, type FormEvent } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { useSession } from "../lib/session";

// safeNext only returns to an in-app path (no open redirect).
function safeNext(next: string | null) {
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
}

export function Login() {
  const { session, signIn } = useSession();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const next = safeNext(params.get("next"));
  if (session?.authenticated) return <Navigate to={next} replace />;

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      const s = await signIn(String(form.get("username")), String(form.get("password")));
      navigate(s.must_change_password ? "/account/password" : next, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign-in failed.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid min-h-dvh place-items-center p-4">
      <div className="crt w-full max-w-sm p-8">
        <div className="relative z-10 grid gap-6">
          <div className="grid justify-items-center gap-3 text-center">
            <img src="/assets/logo/brand.png" alt="" className="size-16" />
            <h1 className="pixel m-0 text-base text-cyan [text-shadow:0_0_14px_#5ee7ff88]">ROM RELAY</h1>
            <p className="pixel m-0 animate-pulse text-[0.55rem] text-muted">PRESS START TO SIGN IN</p>
          </div>
          <form className="grid gap-4" onSubmit={submit}>
            <label className="grid gap-1.5">
              <span className="eyebrow text-[0.5rem] text-muted">Username</span>
              <input className="field" name="username" autoComplete="username" autoFocus required maxLength={64} />
            </label>
            <label className="grid gap-1.5">
              <span className="eyebrow text-[0.5rem] text-muted">Password</span>
              <input className="field" name="password" type="password" autoComplete="current-password" required maxLength={72} />
            </label>
            {error && (
              <p className="m-0 text-sm text-danger" role="alert">
                {error}
              </p>
            )}
            <button className="btn btn-primary w-full py-3" disabled={busy}>
              <span className="pixel text-[0.65rem]">{busy ? "Loading…" : "Start"}</span>
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
