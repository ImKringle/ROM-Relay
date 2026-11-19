import { useState } from "react";
import { api } from "../../lib/api";
import { Empty, ErrorNote, Hero, Modal, Spinner, useData } from "../../components/ui";
import { describeMon, EditorModal, MonDetail, MonTile, PcSlot, SaveList, WithdrawFlow, type Mon } from "./mons";

const BOX_SIZE = 30;

// useMonModals: the detail -> edit / send chain both Pokémon pages share.
export function useMonModals(onChanged: () => void) {
  const [detail, setDetail] = useState<Mon | null>(null);
  const [editing, setEditing] = useState<Mon | "create" | null>(null);
  const [sending, setSending] = useState<Mon | null>(null);
  const modals = (
    <>
      {detail && <MonDetail mon={detail} onClose={() => setDetail(null)} onChanged={onChanged} onEdit={() => (setEditing(detail), setDetail(null))} onSend={() => (setSending(detail), setDetail(null))} />}
      {editing && <EditorModal mon={editing === "create" ? null : editing} onClose={() => setEditing(null)} onChanged={onChanged} onSend={(m) => (setEditing(null), setSending(m))} />}
      {sending && <WithdrawFlow mon={sending} onClose={() => setSending(null)} onChanged={onChanged} />}
    </>
  );
  return { open: setDetail, create: () => setEditing("create"), modals };
}

// PokeHome: deposited Pokémon in virtual PC boxes, oldest first so a box
// never reshuffles when something new arrives.
export function PokeHome() {
  const home = useData(() => api.get<{ pokemon: Mon[] }>("/api/pokehome").then((r) => (r.pokemon ?? []).slice().sort((a, b) => +new Date(a.home_deposited_at ?? 0) - +new Date(b.home_deposited_at ?? 0))), []);
  const [box, setBox] = useState(0);
  const [depositing, setDepositing] = useState(false);
  const { open, create, modals } = useMonModals(home.reload);
  const mons = home.data ?? [];
  const boxes = Math.max(1, Math.ceil(mons.length / BOX_SIZE));
  const b = Math.min(box, boxes - 1);
  const inBox = mons.slice(b * BOX_SIZE, b * BOX_SIZE + BOX_SIZE);

  return (
    <>
      <Hero
        eyebrow="Pokémon tools"
        title="PokeHOME"
        actions={
          <>
            <button className="btn btn-primary" onClick={() => setDepositing(true)}>
              Deposit from a save
            </button>
            <button className="btn" onClick={create}>
              Create Pokémon
            </button>
          </>
        }
      >
        Long-term storage. Deposit Pokémon from your saves, edit them, and send them into any save.
      </Hero>
      {home.error ? (
        <ErrorNote error={home.error} onRetry={home.reload} />
      ) : home.loading && !home.data ? (
        <Spinner />
      ) : mons.length === 0 ? (
        <Empty>
          <strong className="text-text">Nothing deposited yet</strong>
          <p className="m-0 text-sm">Deposit Pokémon from an uploaded save, or create one.</p>
        </Empty>
      ) : (
        <div className="mx-auto max-w-2xl">
          <p className="text-center text-sm text-muted">
            {mons.length} Pokémon deposited, across {boxes} box{boxes === 1 ? "" : "es"}.
          </p>
          <div className="crt p-4">
            <div className="relative z-10 mb-3 flex items-center justify-between">
              <button className="btn btn-sm" disabled={b === 0} onClick={() => setBox(b - 1)} aria-label="Previous box">
                ◀
              </button>
              <span className="pixel text-[0.7rem] text-cyan">
                BOX {b + 1} <span className="text-muted">· {b + 1}/{boxes}</span>
              </span>
              <button className="btn btn-sm" disabled={b >= boxes - 1} onClick={() => setBox(b + 1)} aria-label="Next box">
                ▶
              </button>
            </div>
            <div className="relative z-10 grid grid-cols-6 gap-1.5">
              {Array.from({ length: BOX_SIZE }, (_, i) => inBox[i]).map((m, i) => (
                <PcSlot key={m?.id ?? `empty-${i}`} mon={m} title={m ? `${m.species_name}, ${describeMon(m)}` : undefined} onClick={m ? () => open(m) : undefined} />
              ))}
            </div>
          </div>
        </div>
      )}
      {depositing && <DepositFlow onClose={() => setDepositing(false)} onDeposited={home.reload} />}
      {modals}
    </>
  );
}

// DepositFlow: pick a save, tick the Pokémon to keep.
function DepositFlow({ onClose, onDeposited }: { onClose: () => void; onDeposited: () => void }) {
  const [saveId, setSaveId] = useState<string | null>(null);
  const [mons, setMons] = useState<Mon[] | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [out, setOut] = useState("");
  const pickSave = async (id: string) => {
    setSaveId(id);
    setMons(null);
    setPicked(new Set());
    const all = (await api.get<{ pokemon: Mon[] }>("/api/pokemon")).pokemon ?? [];
    setMons(all.filter((m) => m.origin_save_id === id && !m.home_deposited_at));
  };
  const deposit = async () => {
    if (!picked.size) return setOut("Select at least one Pokémon first.");
    setOut("Depositing…");
    let ok = 0;
    for (const id of picked) {
      try {
        await api.post(`/api/pokehome/${encodeURIComponent(id)}/deposit`);
        ok++;
      } catch (e) {
        return setOut((e as Error).message);
      }
    }
    setOut(`Deposited ${ok} Pokémon.`);
    onDeposited();
    setTimeout(onClose, 700);
  };
  return (
    <Modal open onClose={onClose} label="Deposit" wide>
      <span className="eyebrow">Deposit</span>
      {!saveId ? (
        <>
          <h2 className="pixel mb-4 mt-2 text-sm">Pick a save</h2>
          <SaveList badge onPick={(s) => pickSave(s.id)} />
        </>
      ) : (
        <>
          <div className="mb-4 mt-2 flex items-center justify-between gap-2">
            <h2 className="pixel m-0 text-sm">Choose Pokémon</h2>
            <button className="btn btn-sm" onClick={() => setSaveId(null)}>
              ‹ Saves
            </button>
          </div>
          {!mons ? (
            <Spinner />
          ) : mons.length === 0 ? (
            <p className="text-sm text-muted">Everything from this save is already deposited (or it had no Pokémon).</p>
          ) : (
            <div className="grid max-h-[50vh] gap-2 overflow-y-auto [grid-template-columns:repeat(auto-fill,minmax(96px,1fr))]">
              {mons.map((m) => (
                <MonTile
                  key={m.id}
                  mon={m}
                  checked={picked.has(m.id)}
                  onClick={() => {
                    const next = new Set(picked);
                    if (next.has(m.id)) next.delete(m.id);
                    else next.add(m.id);
                    setPicked(next);
                  }}
                />
              ))}
            </div>
          )}
          <div className="mt-4 flex items-center gap-3">
            <button className="btn btn-primary" onClick={deposit}>
              Deposit {picked.size || ""}
            </button>
            {out && <span className="text-sm text-muted">{out}</span>}
          </div>
        </>
      )}
    </Modal>
  );
}
