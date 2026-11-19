import { Link } from "react-router";
import { api } from "../../lib/api";
import { useSession } from "../../lib/session";
import { ConfirmButton } from "../../components/ConfirmButton";
import { Hero, Icon, useData } from "../../components/ui";

const cards = [
  { to: "/admin/users", title: "Users", body: "Create accounts, change roles, reset passwords.", icon: "M16 11a4 4 0 1 0-8 0M3 21a9 9 0 0 1 18 0" },
  { to: "/admin/roms", title: "ROM Library", body: "Every cataloged file; delete from disk.", icon: "M4 4h16v16H4zM4 9h16M9 4v16" },
  { to: "/admin/saves", title: "Player Data", body: "Saves, Pokédex and PokeHome per user.", icon: "M5 4h11l3 3v13H5zM8 4v6h7V4" },
  { to: "/admin/patches", title: "Patch Workspace", body: "Apply patches, convert, decrypt, remote search.", icon: "M4 20l7-7M14 4l6 6-9 9-6-6z" },
  { to: "/admin/torrents", title: "Torrent Sources", body: "Curate .torrent sources and review archives.", feature: "torrents", icon: "M12 3v12M7 10l5 5 5-5M5 21h14" },
  { to: "/admin/settings", title: "Settings", body: "Library, metadata, features.", icon: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM19 12a7 7 0 0 0-.1-1l2-1.6-2-3.4-2.4 1a7 7 0 0 0-1.7-1L14.5 3h-5l-.3 2.6a7 7 0 0 0-1.7 1l-2.4-1-2 3.4L5.1 11a7 7 0 0 0 0 2l-2 1.6 2 3.4 2.4-1a7 7 0 0 0 1.7 1l.3 2.6h5l.3-2.6a7 7 0 0 0 1.7-1l2.4 1 2-3.4-2-1.6c.1-.3.1-.7.1-1Z" },
  { to: "/admin/api-keys", title: "API Keys", body: "Named, revocable keys for scripts.", icon: "M15 7a4 4 0 1 1-3.9 5H3v3h3v3h3v-3h2.1A4 4 0 0 1 15 7Z" },
];

export function AdminHub() {
  const { session } = useSession();
  const { data } = useData(() => api.get<{ games: number; files: number; users: number }>("/api/admin/overview"), []);
  return (
    <>
      <Hero eyebrow="Control room" title="Admin">
        Everything that changes the relay for everyone.
      </Hero>
      {data && (
        <div className="mb-6 grid grid-cols-3 gap-3">
          {[
            ["Games", data.games],
            ["Files", data.files],
            ["Users", data.users],
          ].map(([k, v]) => (
            <div key={k} className="panel p-4 text-center">
              <div className="pixel text-xl text-cyan">{Number(v).toLocaleString()}</div>
              <div className="eyebrow mt-2 text-[0.5rem] text-muted">{k}</div>
            </div>
          ))}
        </div>
      )}
      <div className="grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(260px,1fr))]">
        {cards
          .filter((c) => !c.feature || session?.features[c.feature])
          .map((c) => (
            <Link key={c.to} to={c.to} className="panel grid gap-2 p-5 text-text no-underline transition hover:-translate-y-0.5 hover:border-cyan-deep">
              <Icon d={c.icon} className="size-6 text-cyan" />
              <strong className="pixel text-[0.7rem]">{c.title}</strong>
              <span className="text-sm text-muted">{c.body}</span>
            </Link>
          ))}
      </div>
      <section className="panel mt-8 grid gap-3 border-danger/40 p-5">
        <span className="eyebrow text-danger">Server control</span>
        <p className="m-0 text-sm text-muted">Stops the relay cleanly. In Docker it restarts on its own; a native install has to be started again by hand.</p>
        <div>
          <ConfirmButton small={false} label="Shut down ROM Relay" confirm="Shut down now?" onConfirm={() => api.post("/api/admin/shutdown")} />
        </div>
      </section>
    </>
  );
}
