import { useState } from "react";
import { api, bytes } from "../../lib/api";
import { sprite } from "../../lib/pokemon";
import { BackLink } from "../../components/BackLink";
import { ConfirmButton } from "../../components/ConfirmButton";
import { ErrorNote, Hero, Icon, icons, Spinner, useData } from "../../components/ui";

type User = { id: string; username: string };
type Trainer = { game: string; ot: string; tid: number; sid: number; played_minutes?: number; hof_clears?: number | null };
type Mon = { id: string; species: number; species_name: string; nickname?: string; level: number; shiny?: boolean; held_item?: string; home_deposited_at?: string };
type Save = { id: string; file_name: string; size_bytes: number; uploaded_at: string; parsed?: boolean; pokemon_count?: number };

// PlayerData: one user's saves, Pokédex playthroughs and PokeHome deposits.
// Every action here is scoped to the selected user.
export function PlayerData() {
  const users = useData(() => api.get<{ users: User[] }>("/api/admin/users"), []);
  const [userId, setUserId] = useState("");
  return (
    <>
      <BackLink to="/admin" label="Admin" />
      <Hero eyebrow="Player data" title="Player Data">
        Pick a user to see their saves, Pokédex playthroughs and PokeHome deposits.
      </Hero>
      <select className="field mb-6 max-w-sm" value={userId} onChange={(e) => setUserId(e.target.value)} aria-label="User">
        <option value="">Choose a user…</option>
        {users.data?.users.map((u) => (
          <option key={u.id} value={u.id}>
            {u.username}
          </option>
        ))}
      </select>
      {userId && <PlayerDetail key={userId} userId={userId} />}
    </>
  );
}

function PlayerDetail({ userId }: { userId: string }) {
  const id = encodeURIComponent(userId);
  const dex = useData(() => api.get<{ trainers: Trainer[]; pokemon: number }>(`/api/admin/users/${id}/dex`), [id]);
  const home = useData(() => api.get<{ pokemon: Mon[] }>(`/api/admin/users/${id}/pokehome`), [id]);
  const saves = useData(() => api.get<{ saves: Save[] }>(`/api/admin/users/${id}/saves`), [id]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const reloadAll = () => (dex.reload(), home.reload(), saves.reload(), setPicked(new Set()));
  const mins = (m = 0) => `${Math.floor(m / 60)}h ${m % 60}m`;

  return (
    <div className="grid gap-5">
      <section className="panel grid gap-3 p-5">
        <span className="eyebrow">Pokédex / Trainers</span>
        {dex.error ? (
          <ErrorNote error={dex.error} />
        ) : !dex.data ? (
          <Spinner />
        ) : (
          <>
            <p className="m-0 text-sm text-muted">{dex.data.pokemon} Pokémon stored.</p>
            {dex.data.trainers?.map((t) => {
              const key = `${t.game}:${t.tid}:${t.sid}`;
              return (
                <label key={key} className="flex items-start gap-2.5 rounded border-2 border-line p-3">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4 accent-cyan"
                    checked={picked.has(key)}
                    onChange={(e) => {
                      const next = new Set(picked);
                      if (e.target.checked) next.add(key);
                      else next.delete(key);
                      setPicked(next);
                    }}
                  />
                  <span className="min-w-0">
                    <strong className="text-sm">
                      {t.game} · {t.ot} ({t.tid}/{t.sid})
                    </strong>
                    <span className="mt-1 block text-xs text-muted">
                      Played {mins(t.played_minutes)}
                      {t.hof_clears != null && ` · ${t.hof_clears} Hall of Fame clear${t.hof_clears === 1 ? "" : "s"}`}
                    </span>
                  </span>
                </label>
              );
            })}
            <div>
              <ConfirmButton
                label={`Clear dex data for ${picked.size} selected`}
                confirm="Clear now?"
                onConfirm={async () => {
                  if (!picked.size) return;
                  await api.post(`/api/admin/users/${id}/clear-trainers`, { trainer_ids: [...picked] });
                  reloadAll();
                }}
              />
            </div>
            <p className="m-0 text-xs text-faint">Clears Hall of Fame and dex progress for the selected playthroughs only. Saves and PokeHome deposits are kept.</p>
          </>
        )}
      </section>

      <section className="panel grid gap-2 p-5">
        <span className="eyebrow">PokeHOME deposits</span>
        {home.error ? (
          <ErrorNote error={home.error} />
        ) : !home.data ? (
          <Spinner />
        ) : !home.data.pokemon?.length ? (
          <p className="m-0 text-sm text-muted">Nothing deposited.</p>
        ) : (
          home.data.pokemon.map((m) => (
            <div key={m.id} className="flex flex-wrap items-center gap-3 rounded border-2 border-line p-3">
              <img className="size-10 [image-rendering:pixelated]" src={sprite(m.species, m.shiny)} alt="" />
              <div className="min-w-0 flex-1">
                <strong className="text-sm">{m.nickname && m.nickname !== m.species_name ? `${m.nickname} (${m.species_name})` : m.species_name}</strong>
                <div className="text-xs text-muted">
                  Lv. {m.level}
                  {m.held_item && ` · holding ${m.held_item}`}
                  {m.home_deposited_at && ` · deposited ${new Date(m.home_deposited_at).toLocaleDateString()}`}
                </div>
              </div>
              <ConfirmButton label="Delete" confirm="Delete from PokeHome?" onConfirm={async () => (await api.del(`/api/admin/users/${id}/pokehome/${encodeURIComponent(m.id)}`), home.reload())} />
            </div>
          ))
        )}
      </section>

      <section className="panel grid gap-2 p-5">
        <span className="eyebrow">Save vault</span>
        {saves.error ? (
          <ErrorNote error={saves.error} />
        ) : !saves.data ? (
          <Spinner />
        ) : !saves.data.saves?.length ? (
          <p className="m-0 text-sm text-muted">No saves stored.</p>
        ) : (
          saves.data.saves.map((s) => (
            <div key={s.id} className="flex flex-wrap items-center gap-3 rounded border-2 border-line p-3">
              <div className="min-w-0 flex-1">
                <strong className="text-sm [overflow-wrap:anywhere]">{s.file_name}</strong>
                <div className="text-xs text-muted">
                  {bytes(s.size_bytes)} · uploaded {new Date(s.uploaded_at).toLocaleDateString()}
                  {s.parsed && ` · ${s.pokemon_count ?? 0} Pokémon`}
                </div>
              </div>
              <a className="btn btn-sm" href={`/admin/users/${id}/saves/${encodeURIComponent(s.id)}/download`}>
                <Icon d={icons.download} /> Download
              </a>
              <ConfirmButton label="Delete" confirm="Delete save?" onConfirm={async () => (await api.del(`/api/admin/users/${id}/saves/${encodeURIComponent(s.id)}`), reloadAll())} />
            </div>
          ))
        )}
      </section>
    </div>
  );
}
