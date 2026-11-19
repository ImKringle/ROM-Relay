import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, setCSRF } from "./api";

export type Tool = { id: string; name: string; description: string; icon: string; route?: string; children?: Tool[] };

export type Session = {
  authenticated: boolean;
  username?: string;
  role?: string;
  admin: boolean;
  must_change_password: boolean;
  csrf?: string;
  open: boolean;
  ready: boolean;
  tools: Tool[];
  features: Record<string, boolean>;
};

type SessionState = {
  session: Session | null;
  refresh: () => Promise<Session>;
  signIn: (username: string, password: string) => Promise<Session>;
  signOut: () => Promise<void>;
};

const Ctx = createContext<SessionState | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const adopt = useCallback((s: Session) => {
    setCSRF(s.csrf || "");
    setSession(s);
    return s;
  }, []);
  const refresh = useCallback(() => api.get<Session>("/api/session").then(adopt), [adopt]);
  const signIn = useCallback(
    (username: string, password: string) => api.post<Session>("/api/session", { username, password }).then(adopt),
    [adopt],
  );
  const signOut = useCallback(async () => {
    await api.del("/api/session");
    await refresh();
  }, [refresh]);
  useEffect(() => {
    refresh().catch(() => setSession({ authenticated: false, admin: false, must_change_password: false, open: false, ready: true, tools: [], features: {} }));
  }, [refresh]);
  return <Ctx.Provider value={{ session, refresh, signIn, signOut }}>{children}</Ctx.Provider>;
}

export function useSession() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useSession outside SessionProvider");
  return ctx;
}

// canBrowse: signed in, or an open relay (no viewer token set).
export function canBrowse(s: Session | null) {
  return !!s && (s.authenticated || s.open || s.admin);
}
