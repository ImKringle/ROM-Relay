import { useEffect, useState } from "react";
import { api } from "../../lib/api";
import { Empty, Hero, Spinner } from "../../components/ui";
import { BagModal, MonTile, SaveList, type Mon } from "./mons";
import { useMonModals } from "./PokeHome";

// Editor: pick a save, edit any Pokémon read from it (grouped by where it
// lives: party, then each box), edit the bag, or create one from scratch.
export function Editor() {
  const [save, setSave] = useState<{ id: string; label: string } | null>(null);
  const [mons, setMons] = useState<Mon[] | null>(null);
  const [bag, setBag] = useState(false);
  const [listKey, setListKey] = useState(0);
  const refresh = async () => {
    if (!save) return;
    const all = (await api.get<{ pokemon: Mon[] }>("/api/pokemon")).pokemon ?? [];
    setMons(all.filter((m) => m.origin_save_id === save.id));
  };
  const { open, create, modals } = useMonModals(refresh);
  useEffect(() => {
    setMons(null);
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save?.id]);
  // A save deleted in another tab should not linger in the picker.
  useEffect(() => {
    const onVis = () => document.visibilityState === "visible" && !save && setListKey((k) => k + 1);
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [save]);

  const groups: [string, Mon[]][] = [];
  if (mons) {
    const party: [number, Mon][] = [];
    const boxes = new Map<number, [number, Mon][]>();
    const other: Mon[] = [];
    for (const m of mons) {
      const p = m.origin_slot?.match(/^party:(\d+)$/);
      const b = m.origin_slot?.match(/^box:(\d+):(\d+)$/);
      if (p) party.push([Number(p[1]), m]);
      else if (b) boxes.set(Number(b[1]), [...(boxes.get(Number(b[1])) ?? []), [Number(b[2]), m]]);
      else other.push(m);
    }
    if (party.length) groups.push(["Party", party.sort((a, b) => a[0] - b[0]).map((x) => x[1])]);
    [...boxes.keys()].sort((a, b) => a - b).forEach((k) => groups.push([`Box ${k}`, boxes.get(k)!.sort((a, b) => a[0] - b[0]).map((x) => x[1])]));
    if (other.length) groups.push([groups.length ? "Other" : "", other]);
  }

  return (
    <>
      <Hero
        eyebrow="Pokémon tools"
        title="Editor"
        actions={
          <button className="btn" onClick={create}>
            Create Pokémon
          </button>
        }
      >
        Edit the Pokémon and items in your saves through PKHeX. Every change is written as a new save version.
      </Hero>
      {!save ? (
        <section className="panel max-w-2xl p-5">
          <span className="eyebrow">Pick a save</span>
          <div className="mt-3">
            <SaveList key={listKey} badge onPick={(s) => setSave({ id: s.id, label: s.game_label || s.file_name })} />
          </div>
        </section>
      ) : (
        <section className="panel p-5">
          <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <button className="btn btn-sm" onClick={() => setSave(null)}>
                ‹ Saves
              </button>
              <strong>{save.label}</strong>
            </div>
            <button className="btn btn-sm" onClick={() => setBag(true)}>
              Bag
            </button>
          </div>
          {!mons ? (
            <Spinner />
          ) : mons.length === 0 ? (
            <Empty>No Pokémon were read from this save.</Empty>
          ) : (
            groups.map(([label, list]) => (
              <div key={label || "all"} className="mb-4">
                {label && <h3 className="eyebrow mb-2">{label}</h3>}
                <div className="grid gap-2 [grid-template-columns:repeat(auto-fill,minmax(96px,1fr))]">
                  {list.map((m) => (
                    <MonTile key={m.id} mon={m} onClick={() => open(m)} />
                  ))}
                </div>
              </div>
            ))
          )}
        </section>
      )}
      {bag && save && <BagModal saveId={save.id} onClose={() => setBag(false)} />}
      {modals}
    </>
  );
}
