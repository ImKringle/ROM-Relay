import { useEffect, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { useSession, type Tool } from "../lib/session";
import { Icon, icons } from "./ui";

// Shell: the top bar (brand, tool nav, activity, account) around every page.
export function Shell() {
  const { session, signOut } = useSession();
  const [menu, setMenu] = useState(false);
  const location = useLocation();
  const navigate = useNavigate();
  useEffect(() => setMenu(false), [location.pathname]);
  const tools = session?.tools ?? [];

  return (
    <div className="min-h-dvh">
      <header className="sticky top-0 z-40 border-b-2 border-line bg-void/90 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-[1500px] items-center gap-4 px-4">
          <Link to="/" className="flex shrink-0 items-center gap-2.5 text-text no-underline">
            <img src="/assets/logo/brand.png" alt="" className="size-8 rounded" />
            <span className="pixel text-[0.7rem] text-cyan [text-shadow:0_0_10px_#5ee7ff88]">ROM RELAY</span>
          </Link>
          <nav className="ml-2 hidden items-center gap-1 lg:flex" aria-label="Tools">
            {tools.map((t) => (
              <NavItem key={t.id} tool={t} />
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <Activity />
            {session?.authenticated ? (
              <>
                <Link to="/account" className="btn btn-sm hidden sm:inline-flex" title="Account">
                  <Icon d={icons.user} />
                  {session.username}
                </Link>
                <button
                  className="btn btn-sm"
                  onClick={async () => {
                    await signOut();
                    navigate("/login");
                  }}
                >
                  Sign out
                </button>
              </>
            ) : (
              <Link to="/login" className="btn btn-sm btn-primary">
                Sign in
              </Link>
            )}
            <button className="btn btn-sm lg:hidden" aria-label="Menu" aria-expanded={menu} onClick={() => setMenu(!menu)}>
              <Icon d={icons.menu} />
            </button>
          </div>
        </div>
        {menu && (
          <nav className="grid gap-1 border-t-2 border-line p-3 lg:hidden" aria-label="Tools">
            {tools.flatMap((t) => (t.children?.length ? t.children : [t])).map((t) =>
              t.route ? (
                <NavLink key={t.id} to={t.route} className={({ isActive }) => `btn justify-start ${isActive ? "btn-primary" : ""}`}>
                  <Icon d={t.icon} /> {t.name}
                </NavLink>
              ) : null,
            )}
            {session?.authenticated && (
              <NavLink to="/account" className="btn justify-start">
                <Icon d={icons.user} /> Account
              </NavLink>
            )}
          </nav>
        )}
      </header>
      <main className="mx-auto max-w-[1500px] px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}

function NavItem({ tool }: { tool: Tool }) {
  const [open, setOpen] = useState(false);
  const location = useLocation();
  useEffect(() => setOpen(false), [location.pathname]);
  const kids = tool.children ?? [];
  if (!kids.length && tool.route) {
    return (
      <NavLink to={tool.route} end={tool.route === "/"} className={({ isActive }) => `btn btn-sm border-transparent shadow-none ${isActive ? "text-cyan" : "bg-transparent"}`}>
        <Icon d={tool.icon} /> {tool.name}
      </NavLink>
    );
  }
  if (kids.length === 1 && kids[0].route) return <NavItem tool={kids[0]} />;
  const active = kids.some((k) => k.route && location.pathname.startsWith(k.route));
  return (
    <div className="relative" onMouseLeave={() => setOpen(false)}>
      <button className={`btn btn-sm border-transparent bg-transparent shadow-none ${active ? "text-cyan" : ""}`} aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon d={tool.icon} /> {tool.name} ▾
      </button>
      {open && (
        <div className="panel panel-hi absolute left-0 top-full z-50 grid min-w-64 gap-1 p-2">
          {kids.map((k) =>
            k.route ? (
              <NavLink key={k.id} to={k.route} className={({ isActive }) => `grid gap-0.5 rounded p-2 no-underline hover:bg-panel-3 ${isActive ? "text-cyan" : "text-text"}`}>
                <span className="flex items-center gap-2 text-sm font-bold">
                  <Icon d={k.icon} /> {k.name}
                </span>
                <span className="text-xs text-muted">{k.description}</span>
              </NavLink>
            ) : null,
          )}
        </div>
      )}
    </div>
  );
}

type Job = { active: boolean; recent?: boolean; message?: string; done?: number; total?: number; error?: string };

// Activity polls the relay's background jobs (scans, metadata, downloads)
// once a second while open, every 5 s otherwise.
function Activity() {
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let stop = false;
    let timer: number;
    const poll = async () => {
      try {
        const r = await fetch("/healthz", { credentials: "same-origin" });
        if (r.ok) setJobs(((await r.json()).background_jobs as Record<string, Job>) || {});
      } catch {
        /* next poll */
      }
      if (!stop) timer = window.setTimeout(poll, open ? 1000 : 5000);
    };
    poll();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [open]);
  const rows = Object.entries(jobs).filter(([, j]) => j.active || j.error || j.recent);
  const busy = rows.some(([, j]) => j.active);
  return (
    <div className="relative">
      <button className={`btn btn-sm ${busy ? "text-cyan" : ""}`} aria-label="Background activity" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon d={icons.activity} className={`size-4 ${busy ? "animate-spin [animation-duration:3s]" : ""}`} />
        {busy && <span className="pixel text-[0.55rem]">{rows.filter(([, j]) => j.active).length}</span>}
      </button>
      {open && (
        <div className="panel panel-hi absolute right-0 top-full z-50 mt-2 grid w-80 max-w-[90vw] gap-2 p-3">
          <span className="eyebrow">Activity</span>
          {rows.length === 0 && <p className="m-0 text-sm text-muted">Nothing running.</p>}
          {rows.map(([name, j]) => (
            <div key={name} className="grid gap-1 text-sm">
              <div className="flex justify-between gap-2">
                <strong className="truncate">{name.replaceAll("_", " ")}</strong>
                {j.error ? <span className="text-danger">failed</span> : j.active ? null : <span className="text-leaf">done</span>}
              </div>
              {j.message && <span className="text-xs text-muted">{j.message}</span>}
              {j.error && <span className="text-xs text-danger [overflow-wrap:anywhere]">{j.error}</span>}
              {j.active && (j.total ?? 0) > 1 && (
                <div className="h-2 overflow-hidden rounded-sm border border-line bg-void">
                  <div className="h-full bg-cyan" style={{ width: `${Math.min(100, (100 * (j.done ?? 0)) / (j.total ?? 1))}%` }} />
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
