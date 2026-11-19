import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { api, ApiError } from "../../lib/api";
import { sprite } from "../../lib/pokemon";
import { ShinyIcon, TypeChip } from "../../components/PokemonBits";
import { ConfirmButton } from "../../components/ConfirmButton";
import { Modal, Spinner } from "../../components/ui";

// Shared Pokémon model and surfaces for PokeHome and the Editor: one mon
// object whether it is browsed, sent to a save, or edited.

export type Stats = Partial<Record<"hp" | "atk" | "def" | "spa" | "spd" | "spe", number>>;
export type Mon = {
  id: string;
  species: number;
  species_name: string;
  nickname?: string;
  level: number;
  form?: number;
  nature?: string;
  gender?: string;
  ability?: string;
  held_item?: string;
  ball?: number;
  shiny?: boolean;
  generation?: number;
  origin_game?: string;
  origin_ot?: string;
  origin_save_id?: string;
  origin_slot?: string;
  ivs?: Stats;
  evs?: Stats;
  moves?: { id: number; name: string }[];
  home_deposited_at?: string;
  home_last_withdrawn_at?: string;
};
export type Save = { id: string; file_name: string; game_label?: string; parsed?: boolean; pokemon_count?: number };
type Reference = {
  species: { dex: number; name: string }[];
  abilities: { name: string }[];
  items: { id: number; name: string }[];
  moves: { id: number; name: string }[];
  balls: { id: number; name: string }[];
  natures: { name: string; effect: string }[];
};

export const STATS = [
  ["hp", "HP"],
  ["atk", "Atk"],
  ["def", "Def"],
  ["spa", "SpA"],
  ["spd", "SpD"],
  ["spe", "Spe"],
] as const;

// Reference tables (the sidecar's own PKHeX names), fetched once per page.
let refPromise: Promise<Reference> | null = null;
export const loadReference = () => (refPromise ??= api.get<Reference>("/api/reference"));

export const isNicknamed = (m: Mon) => !!m.nickname && m.nickname.toUpperCase() !== (m.species_name || "").toUpperCase();
export const monLabel = (m: Mon) => (isNicknamed(m) ? `${m.nickname} (${m.species_name})` : m.species_name);
export const describeMon = (m: Mon) => [`Lv. ${m.level}`, m.nature, m.shiny && "Shiny"].filter(Boolean).join(" · ");

// PokéAPI item icon naming: accents and apostrophes dropped, rest dashed.
export function itemIcon(name: string) {
  const slug = name
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/['’.]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return `/sprites/items/${slug}.png`;
}
const noIcon = (e: React.SyntheticEvent<HTMLImageElement>) => (e.currentTarget.style.visibility = "hidden");

export function GenderMark({ gender, className = "size-3.5" }: { gender?: string; className?: string }) {
  if (gender !== "M" && gender !== "F") return null;
  return <img src={gender === "M" ? "/assets/gender/male.png" : "/assets/gender/female.png"} alt={gender === "M" ? "Male" : "Female"} className={className} />;
}

// PcSlot: one box slot, empty or occupied, browsing or picking.
export function PcSlot({ mon, onClick, title, pickable }: { mon?: Mon | null; onClick?: () => void; title?: string; pickable?: boolean }) {
  return (
    <button
      type="button"
      disabled={!onClick}
      title={title}
      onClick={onClick}
      className={`relative grid aspect-square place-items-center rounded border-2 ${mon ? "border-line bg-panel-2 hover:border-cyan" : pickable ? "border-dashed border-cyan-deep bg-void hover:border-cyan" : "border-line/50 bg-void/60"} disabled:cursor-default`}
    >
      {mon && (
        <>
          <img src={sprite(mon.species, mon.shiny)} alt={mon.species_name} loading="lazy" className="size-[80%] [image-rendering:pixelated]" />
          <span className="pixel absolute bottom-0.5 right-1 text-[0.45rem] text-muted">Lv{mon.level}</span>
          {mon.shiny && (
            <span className="absolute left-0.5 top-0.5">
              <ShinyIcon className="size-3" />
            </span>
          )}
          <span className="absolute right-0.5 top-0.5">
            <GenderMark gender={mon.gender} className="size-3" />
          </span>
        </>
      )}
    </button>
  );
}

export function MonTile({ mon, onClick, checked }: { mon: Mon; onClick?: () => void; checked?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={`${mon.species_name}, ${describeMon(mon)}${mon.held_item ? `, holding ${mon.held_item}` : ""}`}
      className={`relative grid justify-items-center gap-0.5 rounded border-2 p-2 ${checked ? "border-cyan bg-cyan/10" : "border-line bg-panel hover:border-cyan-deep"}`}
    >
      <img src={sprite(mon.species, mon.shiny)} alt="" className="size-14 [image-rendering:pixelated]" loading="lazy" />
      <span className="w-full truncate text-center text-xs font-semibold">{isNicknamed(mon) ? mon.nickname : mon.species_name}</span>
      <span className="pixel text-[0.45rem] text-muted">Lv{mon.level}</span>
      {mon.shiny && (
        <span className="absolute left-1 top-1">
          <ShinyIcon className="size-3" />
        </span>
      )}
      <span className="absolute right-1 top-1">
        <GenderMark gender={mon.gender} className="size-3" />
      </span>
      {mon.held_item && <img src={itemIcon(mon.held_item)} alt={mon.held_item} onError={noIcon} className="absolute bottom-1 left-1 size-5" />}
      {checked !== undefined && <span className="absolute bottom-1 right-1 text-cyan">{checked ? "✓" : ""}</span>}
    </button>
  );
}

// ---- Combobox --------------------------------------------------------------

export type Option = { value: string | number; label: string; icon?: string };

// Combobox: type to filter, pick with click or Enter. The list is portalled
// to <body> with fixed positioning so a scrolling modal never clips it.
export function Combobox({ value, label, options, onChange, placeholder, id }: { value: string | number; label: string; options: Option[]; onChange: (o: Option) => void; placeholder?: string; id?: string }) {
  const input = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState<string | null>(null);
  const [active, setActive] = useState(-1);
  const [pos, setPos] = useState<{ left: number; width: number; top?: number; bottom?: number }>({ left: 0, width: 0 });
  const q = (query ?? "").trim().toLowerCase();
  const filtered = (q ? options.filter((o) => o.label.toLowerCase().includes(q)) : options).slice(0, 40);

  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const r = input.current!.getBoundingClientRect();
      const below = window.innerHeight - r.bottom;
      setPos(below < 260 && r.top > below ? { left: r.left, width: r.width, bottom: window.innerHeight - r.top + 4 } : { left: r.left, width: r.width, top: r.bottom + 4 });
    };
    place();
    document.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      document.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open]);

  const choose = (o: Option) => {
    onChange(o);
    setQuery(null);
    setOpen(false);
  };
  return (
    <>
      <input
        ref={input}
        id={id}
        className="field"
        autoComplete="off"
        placeholder={placeholder}
        value={query ?? label}
        onFocus={() => (setOpen(true), setQuery(""))}
        onBlur={() => setTimeout(() => (setOpen(false), setQuery(null)), 120)}
        onChange={(e) => (setQuery(e.target.value), setActive(-1), setOpen(true))}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") (e.preventDefault(), setActive((a) => Math.min(a + 1, filtered.length - 1)));
          else if (e.key === "ArrowUp") (e.preventDefault(), setActive((a) => Math.max(a - 1, 0)));
          else if (e.key === "Enter" && filtered[active]) (e.preventDefault(), choose(filtered[active]));
          else if (e.key === "Escape") setOpen(false);
        }}
        data-value={value}
      />
      {open &&
        createPortal(
          <div className="panel panel-hi fixed z-[60] max-h-64 overflow-y-auto p-1" style={pos}>
            {filtered.length === 0 ? (
              <div className="p-2 text-sm text-muted">{q ? "No matches." : "Type to search…"}</div>
            ) : (
              filtered.map((o, i) => (
                <div
                  key={`${o.value}`}
                  onMouseDown={(e) => (e.preventDefault(), choose(o))}
                  className={`flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm ${i === active ? "bg-cyan/20 text-cyan" : "hover:bg-panel-3"}`}
                >
                  {o.icon && <img src={o.icon} alt="" loading="lazy" onError={noIcon} className="size-6 [image-rendering:pixelated]" />}
                  {o.label}
                </div>
              ))
            )}
          </div>,
          document.body,
        )}
    </>
  );
}

// ---- Detail ------------------------------------------------------------------

export function MonDetail({ mon, onClose, onEdit, onSend, onChanged }: { mon: Mon; onClose: () => void; onEdit: () => void; onSend: () => void; onChanged: () => void }) {
  return (
    <Modal open onClose={onClose} label="Pokémon">
      <div className="flex items-center gap-4">
        <img src={sprite(mon.species, mon.shiny)} alt="" className="size-24 rounded border-2 border-line bg-void [image-rendering:pixelated]" />
        <div className="min-w-0">
          <h2 className="pixel m-0 text-sm capitalize">{monLabel(mon)}</h2>
          <p className="m-0 mt-2 text-sm text-muted">{describeMon(mon)}</p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {mon.origin_game && <span className="badge badge-cyan">{mon.origin_game}</span>}
            {mon.ability && <span className="badge">{mon.ability}</span>}
            {mon.held_item && <span className="badge">{mon.held_item}</span>}
          </div>
        </div>
      </div>
      {mon.home_deposited_at && (
        <p className="mb-0 mt-3 text-xs text-faint">
          Deposited {new Date(mon.home_deposited_at).toLocaleString()}
          {mon.home_last_withdrawn_at && ` · last sent out ${new Date(mon.home_last_withdrawn_at).toLocaleString()}`}
        </p>
      )}
      <div className="mt-5 flex flex-wrap gap-2">
        <button className="btn btn-primary" onClick={onSend}>
          Send to a save
        </button>
        <button className="btn" onClick={onEdit}>
          Edit
        </button>
        <span className="ml-auto">
          <ConfirmButton
            small={false}
            label="Delete"
            confirm="Delete for good?"
            onConfirm={async () => {
              await api.del(`/api/pokemon/${encodeURIComponent(mon.id)}`);
              onClose();
              onChanged();
            }}
          />
        </span>
      </div>
    </Modal>
  );
}

// ---- Save list -------------------------------------------------------------

export function SaveList({ onPick, badge }: { onPick: (s: Save) => void; badge?: boolean }) {
  const [saves, setSaves] = useState<Save[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    api.get<{ saves: Save[] }>("/api/saves").then((r) => setSaves(r.saves ?? []), (e) => setErr(e.message));
  }, []);
  if (err) return <p className="text-sm text-danger">{err}</p>;
  if (!saves) return <Spinner label="Loading saves" />;
  if (!saves.length) return <p className="text-sm text-muted">No saves yet. Upload one on the Save Data page.</p>;
  return (
    <div className="grid gap-1.5">
      {saves.map((s) => (
        <button key={s.id} className="flex items-center justify-between gap-2 rounded border-2 border-line bg-panel p-3 text-left hover:border-cyan" onClick={() => onPick(s)}>
          <span className="font-semibold [overflow-wrap:anywhere]">{s.game_label || s.file_name}</span>
          {badge && s.parsed && <span className="badge">{s.pokemon_count ?? 0} Pokémon</span>}
        </button>
      ))}
    </div>
  );
}

// ---- Withdraw: pick a destination save, then a box slot ---------------------

type Layout = { box_count: number; box_slot_count: number; occupied?: Record<string, Mon> };

export function WithdrawFlow({ mon, onClose, onChanged }: { mon: Mon; onClose: () => void; onChanged: () => void }) {
  const [dest, setDest] = useState<{ id: string; label: string } | null>(null);
  const [layout, setLayout] = useState<Layout | null>(null);
  const [box, setBox] = useState(0);
  const [pending, setPending] = useState<{ box: number; slot: number; occupant?: Mon } | null>(null);
  const [out, setOut] = useState<ReactNode>(null);
  const loadBoxes = async (id: string) => setLayout(await api.get<Layout>(`/api/pokehome/boxes?save_id=${encodeURIComponent(id)}`));
  const occupants = new Map<number, Mon>();
  for (const [key, occ] of Object.entries(layout?.occupied ?? {})) {
    const [, b, s] = key.split(":");
    if (Number(b) === box + 1) occupants.set(Number(s), occ);
  }
  const send = async () => {
    if (!pending || !dest) return;
    setOut(pending.occupant ? "Moving the occupant to PokeHome and sending…" : "Sending…");
    try {
      const r = await api.post<{ save_id: string }>(`/api/pokehome/${encodeURIComponent(mon.id)}/withdraw`, { save_id: dest.id, box: pending.box, slot: pending.slot, evict_to_home: !!pending.occupant });
      setOut(
        <div className="grid gap-2">
          <span className="text-leaf">Sent.</span>
          <div className="flex flex-wrap gap-2">
            <a className="btn btn-primary flex-1" href={`/saves/${encodeURIComponent(r.save_id)}/download`}>
              Download save
            </a>
            <a className="btn flex-1" href="/saves">
              View in Save Data
            </a>
          </div>
        </div>,
      );
      // Saves are immutable: a withdrawal makes a new version, and a second
      // one this session must build on it, not on the version first picked.
      setDest({ ...dest, id: r.save_id });
      setPending(null);
      onChanged();
      await loadBoxes(r.save_id);
    } catch (e) {
      setOut(<span className="text-danger">{(e as Error).message}</span>);
    }
  };
  return (
    <Modal open onClose={onClose} label="Send to a save" wide>
      <span className="eyebrow">Send {mon.species_name}</span>
      {!dest ? (
        <>
          <h2 className="pixel mb-4 mt-2 text-sm">Pick a save</h2>
          <SaveList
            onPick={async (s) => {
              setDest({ id: s.id, label: s.game_label || s.file_name });
              setBox(0);
              setOut("Loading boxes…");
              try {
                await loadBoxes(s.id);
                setOut(null);
              } catch (e) {
                setOut(<span className="text-danger">{(e as Error).message}</span>);
              }
            }}
          />
        </>
      ) : (
        <>
          <div className="mb-3 mt-2 flex flex-wrap items-center justify-between gap-2">
            <button className="btn btn-sm" onClick={() => (setDest(null), setLayout(null), setOut(null))}>
              ‹ Saves
            </button>
            <span className="text-sm text-muted">{dest.label}</span>
          </div>
          {layout && (
            <div className="mx-auto max-w-md">
              <div className="mb-2 flex items-center justify-between">
                <button className="btn btn-sm" disabled={box === 0} onClick={() => setBox(box - 1)}>
                  ◀
                </button>
                <span className="pixel text-[0.6rem] text-cyan">
                  BOX {box + 1} · {box + 1}/{layout.box_count}
                </span>
                <button className="btn btn-sm" disabled={box >= layout.box_count - 1} onClick={() => setBox(box + 1)}>
                  ▶
                </button>
              </div>
              <div className="grid grid-cols-6 gap-1.5 rounded border-2 border-line-hi bg-void p-2">
                {Array.from({ length: layout.box_slot_count }, (_, i) => i + 1).map((slot) => {
                  const occ = occupants.get(slot);
                  return (
                    <PcSlot
                      key={slot}
                      mon={occ}
                      pickable
                      title={occ ? `${occ.species_name}: click to swap it to PokeHome and place yours here` : "Empty: send here"}
                      onClick={() => setPending({ box: box + 1, slot, occupant: occ })}
                    />
                  );
                })}
              </div>
            </div>
          )}
          {pending && (
            <div className="mt-4 grid gap-2 rounded border-2 border-gold/50 p-3 text-sm">
              {pending.occupant
                ? `Box ${pending.box} slot ${pending.slot} has ${pending.occupant.species_name}. Move it to PokeHome and put ${mon.species_name} there? This creates a new save version.`
                : `Send ${mon.species_name} to box ${pending.box} slot ${pending.slot}? This creates a new save version.`}
              <div className="flex gap-2">
                <button className="btn btn-primary btn-sm" onClick={send}>
                  Send
                </button>
                <button className="btn btn-sm" onClick={() => setPending(null)}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </>
      )}
      {out && <div className="mt-3 text-sm">{out}</div>}
    </Modal>
  );
}

// ---- Editor: edit an owned Pokémon, or create one ----------------------------

type Form = {
  species: number;
  speciesLabel: string;
  generation: number;
  nickname: string;
  ot: string;
  level: number;
  form: number;
  nature: string;
  gender: string;
  ability: string;
  heldItem: string;
  ball: number;
  ballLabel: string;
  shiny: boolean;
  ivs: Record<string, number>;
  evs: Record<string, number>;
  moves: { id: number; name: string }[];
};

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(v) || 0));

export function EditorModal({ mon, onClose, onChanged, onSend }: { mon: Mon | null; onClose: () => void; onChanged: () => void; onSend: (m: Mon) => void }) {
  const create = !mon;
  const [ref, setRef] = useState<Reference | null>(null);
  const [f, setF] = useState<Form | null>(null);
  const [types, setTypes] = useState<string[]>([]);
  const [legal, setLegal] = useState<Option[]>([]);
  const [error, setError] = useState<{ message: string; report?: string[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Mon | null>(null);

  useEffect(() => {
    loadReference().then((r) => {
      setRef(r);
      const ball = mon?.ball || 4;
      const ballName = r.balls.find((b) => b.id === ball)?.name ?? "Poké Ball";
      const stat = (s?: Stats) => Object.fromEntries(STATS.map(([k]) => [k, s?.[k] ?? 0]));
      setF({
        species: mon?.species ?? 0,
        speciesLabel: mon ? `${mon.species_name} (#${mon.species})` : "",
        generation: mon?.generation ?? 9,
        nickname: mon?.nickname ?? "",
        ot: mon?.origin_ot ?? "",
        level: mon?.level ?? 5,
        form: mon?.form ?? 0,
        nature: mon?.nature ?? "Hardy",
        gender: mon?.gender || "-",
        ability: mon?.ability ?? "",
        heldItem: mon?.held_item ?? "",
        ball,
        ballLabel: `${ballName} (${ball})`,
        shiny: !!mon?.shiny,
        ivs: stat(mon?.ivs),
        evs: stat(mon?.evs),
        moves: [0, 1, 2, 3].map((i) => mon?.moves?.[i] ?? { id: 0, name: "" }),
      });
    });
  }, [mon]);

  // Types and the legal abilities for the chosen species, from the Pokédex
  // reference (optional: missing data just leaves them empty).
  useEffect(() => {
    if (!f?.species) return setTypes([]), setLegal([]);
    let stale = false;
    api
      .get<{ species: { types?: string; abilities?: string; hidden_ability?: string } }>(`/api/dex/species/${f.species}`)
      .then(({ species: s }) => {
        if (stale) return;
        setTypes((s.types ?? "").split("/").filter(Boolean));
        const opts: Option[] = (s.abilities ?? "").split("/").filter(Boolean).map((a) => ({ value: a, label: a }));
        if (s.hidden_ability) opts.push({ value: s.hidden_ability, label: `${s.hidden_ability} (Hidden)` });
        setLegal(opts);
        // A blank ability fails legality for almost every species on create.
        if (create && opts[0]) setF((x) => (x && !x.ability ? { ...x, ability: String(opts[0].value) } : x));
      })
      .catch(() => (setTypes([]), setLegal([])));
    return () => {
      stale = true;
    };
  }, [f?.species, create]);

  if (!ref || !f) return (
    <Modal open onClose={onClose} label="Editor">
      <Spinner />
    </Modal>
  );
  const set = (patch: Partial<Form>) => setF({ ...f, ...patch });

  const submit = async () => {
    setError(null);
    setBusy(true);
    try {
      if (create) {
        if (!f.species) throw new Error("Pick a species first.");
        const body: Record<string, unknown> = { generation: f.generation, species: f.species, level: f.level || 5 };
        if (f.nickname) body.nickname = f.nickname;
        if (f.ot) body.original_trainer_name = f.ot;
        if (f.form) body.form = f.form;
        if (f.nature) body.nature = f.nature;
        if (f.gender) body.gender = f.gender;
        if (f.ability) body.ability = f.ability;
        if (f.heldItem) body.held_item = f.heldItem;
        if (f.ball) body.ball = f.ball;
        if (f.shiny) body.shiny = true;
        const ivs = Object.fromEntries(Object.entries(f.ivs).filter(([, v]) => v));
        const evs = Object.fromEntries(Object.entries(f.evs).filter(([, v]) => v));
        if (Object.keys(ivs).length) body.ivs = ivs;
        if (Object.keys(evs).length) body.evs = evs;
        const moves = f.moves.map((m) => m.id).filter(Boolean);
        if (moves.length) body.moves = moves;
        setCreated(await api.post<Mon>("/api/pokemon", body));
      } else {
        // Only what changed is sent, so untouched fields stay byte-identical.
        const o = mon!;
        const body: Record<string, unknown> = { generation: o.generation };
        if (f.nickname !== (o.nickname ?? "")) body.nickname = f.nickname;
        if (f.ot !== (o.origin_ot ?? "")) body.original_trainer_name = f.ot;
        if (f.level !== o.level) body.level = f.level;
        if (f.form !== (o.form ?? 0)) body.form = f.form;
        if (f.nature !== o.nature) body.nature = f.nature;
        if (f.gender !== o.gender) body.gender = f.gender;
        if (f.ability !== o.ability) body.ability = f.ability;
        if (f.heldItem !== (o.held_item ?? "")) body.held_item = f.heldItem;
        if (f.ball && f.ball !== o.ball) body.ball = f.ball;
        if (f.shiny !== !!o.shiny) body.shiny = f.shiny;
        for (const kind of ["ivs", "evs"] as const) {
          const diff = Object.fromEntries(STATS.filter(([k]) => f[kind][k] !== (o[kind]?.[k] ?? 0)).map(([k]) => [k, f[kind][k]]));
          if (Object.keys(diff).length) body[kind] = diff;
        }
        const newMoves = f.moves.map((m) => m.id);
        if (newMoves.some((id, i) => id !== (o.moves?.[i]?.id ?? 0))) body.moves = newMoves;
        await api.post(`/api/pokemon/${encodeURIComponent(o.id)}/edit`, body);
        onChanged();
        onClose();
      }
    } catch (e) {
      setError({ message: (e as Error).message, report: e instanceof ApiError ? e.report : undefined });
    } finally {
      setBusy(false);
    }
  };

  if (created) return <PostCreate mon={created} onClose={onClose} onChanged={onChanged} onSend={onSend} />;

  const moveOptions: Option[] = [{ value: 0, label: "— (empty)" }, ...ref.moves.map((m) => ({ value: m.id, label: m.name }))];
  return (
    <Modal open onClose={onClose} label={create ? "Create Pokémon" : "Edit Pokémon"} wide>
      <div className="mb-4 flex flex-wrap items-center gap-4">
        <div className="grid size-24 place-items-center rounded border-2 border-line bg-void">{f.species ? <img src={sprite(f.species, f.shiny)} alt="" className="size-20 [image-rendering:pixelated]" /> : null}</div>
        <div>
          <span className="eyebrow">{create ? "Create" : "Edit"}</span>
          <h2 className="pixel m-0 mt-2 text-sm">{create ? "Create Pokémon" : "Edit Pokémon"}</h2>
          <div className="mt-2 flex gap-1.5">
            {types.map((t) => (
              <TypeChip key={t} type={t} />
            ))}
          </div>
          <p className="m-0 mt-2 max-w-xl text-xs text-muted">
            {create ? "Built through the same PKHeX library that reads your saves. Species and level are required." : "Written back byte-for-byte through PKHeX. Untouched fields stay as they are."}
          </p>
        </div>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {create && (
          <>
            <Field label="Species">
              <Combobox
                value={f.species}
                label={f.speciesLabel}
                placeholder="Search species…"
                options={ref.species.map((s) => ({ value: s.dex, label: `${s.name} (#${s.dex})`, icon: sprite(s.dex) }))}
                onChange={(o) => set({ species: Number(o.value), speciesLabel: o.label })}
              />
            </Field>
            <Field label="Generation">
              <select className="field" value={f.generation} onChange={(e) => set({ generation: Number(e.target.value) })}>
                {[1, 2, 3, 4, 5, 6, 7, 8, 9].map((g) => (
                  <option key={g} value={g}>
                    Generation {g}
                  </option>
                ))}
              </select>
            </Field>
          </>
        )}
        <Field label="Nickname">
          <input className="field" value={f.nickname} onChange={(e) => set({ nickname: e.target.value })} />
        </Field>
        <Field label="Original trainer">
          <input className="field" value={f.ot} onChange={(e) => set({ ot: e.target.value })} />
        </Field>
        <Field label="Level">
          <input className="field" type="number" min={1} max={100} value={f.level} onChange={(e) => set({ level: clamp(Number(e.target.value), 1, 100) })} />
        </Field>
        <Field label="Form">
          <input className="field" type="number" min={0} value={f.form} onChange={(e) => set({ form: Math.max(0, Number(e.target.value) || 0) })} />
        </Field>
        <Field label="Nature">
          <select className="field" value={f.nature} onChange={(e) => set({ nature: e.target.value })}>
            {ref.natures.map((n) => (
              <option key={n.name} value={n.name}>
                {n.effect === "Neutral" ? n.name : `${n.name}, ${n.effect}`}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Gender">
          <select className="field" value={f.gender} onChange={(e) => set({ gender: e.target.value })}>
            <option value="M">Male</option>
            <option value="F">Female</option>
            <option value="-">Genderless</option>
          </select>
        </Field>
        <Field label="Ability" hint={legal.length ? `Legal: ${legal.map((o) => o.label).join(", ")}` : undefined}>
          <Combobox value={f.ability} label={f.ability} options={legal.length ? legal : ref.abilities.map((a) => ({ value: a.name, label: a.name }))} onChange={(o) => set({ ability: String(o.value) })} />
        </Field>
        <Field label="Held item">
          <Combobox value={f.heldItem} label={f.heldItem} placeholder="None" options={ref.items.map((i) => ({ value: i.name, label: i.name, icon: itemIcon(i.name) }))} onChange={(o) => set({ heldItem: String(o.value) })} />
        </Field>
        <Field label="Ball">
          <Combobox value={f.ball} label={f.ballLabel} options={ref.balls.map((b) => ({ value: b.id, label: `${b.name} (${b.id})`, icon: itemIcon(b.name) }))} onChange={(o) => set({ ball: Number(o.value), ballLabel: o.label })} />
        </Field>
        <label className="flex items-center gap-2 self-end pb-2 text-sm">
          <input type="checkbox" className="size-4 accent-cyan" checked={f.shiny} onChange={(e) => set({ shiny: e.target.checked })} />
          Shiny
        </label>
      </div>
      <div className="mt-4 grid gap-4 md:grid-cols-2">
        {(["ivs", "evs"] as const).map((kind) => (
          <fieldset key={kind} className="rounded border-2 border-line p-3">
            <legend className="eyebrow px-1">{kind === "ivs" ? "IVs (0-31)" : "EVs (0-252)"}</legend>
            <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
              {STATS.map(([k, l]) => (
                <label key={k} className="grid gap-1 text-xs">
                  {l}
                  <input
                    className="field px-1.5 py-1 text-sm"
                    type="number"
                    min={0}
                    max={kind === "ivs" ? 31 : 252}
                    value={f[kind][k]}
                    onChange={(e) => set({ [kind]: { ...f[kind], [k]: clamp(Number(e.target.value), 0, kind === "ivs" ? 31 : 252) } } as Partial<Form>)}
                  />
                </label>
              ))}
            </div>
          </fieldset>
        ))}
      </div>
      <fieldset className="mt-4 grid gap-2 rounded border-2 border-line p-3 sm:grid-cols-2">
        <legend className="eyebrow px-1">Moves</legend>
        {f.moves.map((m, i) => (
          <Field key={i} label={`Move ${i + 1}`}>
            <Combobox value={m.id} label={m.name} placeholder="—" options={moveOptions} onChange={(o) => set({ moves: f.moves.map((x, j) => (j === i ? { id: Number(o.value), name: Number(o.value) ? o.label : "" } : x)) })} />
          </Field>
        ))}
      </fieldset>
      {error && (
        <div className="mt-3 text-sm text-danger" role="alert">
          <p className="m-0 font-semibold">{error.message}</p>
          {error.report && (
            <ul className="m-0 mt-1 list-disc pl-5">
              {error.report.map((l) => (
                <li key={l}>{l}</li>
              ))}
            </ul>
          )}
        </div>
      )}
      <div className="mt-4 flex gap-2">
        <button className="btn btn-primary" disabled={busy} onClick={submit}>
          {create ? "Create Pokémon" : "Save changes"}
        </button>
        <button className="btn" onClick={onClose}>
          Cancel
        </button>
      </div>
    </Modal>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="grid content-start gap-1 text-sm">
      {label}
      {children}
      {hint && <span className="text-xs text-faint">{hint}</span>}
    </label>
  );
}

// PostCreate: a new Pokémon goes nowhere unless asked; every follow-up is opt-in.
function PostCreate({ mon, onClose, onChanged, onSend }: { mon: Mon; onClose: () => void; onChanged: () => void; onSend: (m: Mon) => void }) {
  const [out, setOut] = useState("");
  const deposit = async () => {
    setOut("Depositing…");
    try {
      await api.post(`/api/pokehome/${encodeURIComponent(mon.id)}/deposit`);
      setOut("Deposited in PokeHome.");
      onChanged();
    } catch (e) {
      setOut((e as Error).message);
    }
  };
  return (
    <Modal open onClose={() => (onChanged(), onClose())} label="Created">
      <span className="eyebrow">Created</span>
      <h2 className="pixel mb-4 mt-2 text-sm">{mon.nickname || mon.species_name}</h2>
      <p className="text-sm text-muted">What should happen to it?</p>
      <div className="grid gap-2">
        <button className="btn justify-start" onClick={deposit}>
          Deposit in PokeHome
        </button>
        <a className="btn justify-start" href={`/api/pokemon/${encodeURIComponent(mon.id)}/download`} download>
          Download the Pokémon file
        </a>
        <button className="btn justify-start" onClick={() => onSend(mon)}>
          Send it into a save…
        </button>
        <button className="btn btn-primary" onClick={() => (onChanged(), onClose())}>
          Done
        </button>
      </div>
      {out && <p className="mb-0 mt-3 text-sm">{out}</p>}
    </Modal>
  );
}

// ---- Bag: edit a save's item pouches ------------------------------------------

type Pouch = { type: string; capacity: number; max_count: number; items: { index: number; name?: string; count: number }[] };

export function BagModal({ saveId, onClose }: { saveId: string; onClose: () => void }) {
  const [pouches, setPouches] = useState<Pouch[] | null>(null);
  const [items, setItems] = useState<Option[]>([]);
  const [out, setOut] = useState("");
  const [adding, setAdding] = useState<Record<string, { id: number; name: string; qty: number }>>({});
  useEffect(() => {
    api.get<{ pouches: Pouch[] }>(`/api/saves/${encodeURIComponent(saveId)}/bag`).then(
      (r) => setPouches(r.pouches ?? []),
      (e) => setOut(e.message),
    );
    loadReference().then((r) => setItems(r.items.map((i) => ({ value: i.id, label: i.name, icon: itemIcon(i.name) }))));
  }, [saveId]);
  const update = (pi: number, fn: (p: Pouch) => Pouch) => setPouches((ps) => ps!.map((p, i) => (i === pi ? fn(p) : p)));
  const save = async () => {
    setOut("Saving…");
    try {
      const r = await api.post<{ pouches?: Pouch[] }>(`/api/saves/${encodeURIComponent(saveId)}/bag`, {
        pouches: pouches!.map((p) => ({ type: p.type, items: p.items.filter((i) => i.count > 0).map((i) => ({ index: i.index, count: i.count })) })),
      });
      if (r.pouches) setPouches(r.pouches);
      setOut("Saved as a new save version; see it in Save Data.");
    } catch (e) {
      setOut((e as Error).message);
    }
  };
  return (
    <Modal open onClose={onClose} label="Bag" wide>
      <span className="eyebrow">Bag</span>
      <h2 className="pixel mb-4 mt-2 text-sm">Items</h2>
      {!pouches ? (
        out ? <p className="text-sm text-danger">{out}</p> : <Spinner />
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {pouches.map((p, pi) => (
            <section key={p.type} className="panel p-3">
              <div className="flex items-center justify-between">
                <span className="eyebrow">{p.type}</span>
                <span className="text-xs text-faint">
                  {p.items.length}/{p.capacity} slots · max {p.max_count}
                </span>
              </div>
              <div className="mt-2 grid gap-1.5">
                {p.items.map((it) => (
                  <div key={it.index} className="flex items-center gap-2">
                    <img src={itemIcon(it.name ?? "")} alt="" onError={noIcon} className="size-6 [image-rendering:pixelated]" />
                    <span className="flex-1 text-sm">{it.name || `Item #${it.index}`}</span>
                    <input
                      className="field w-20 py-1"
                      type="number"
                      min={0}
                      max={p.max_count}
                      value={it.count}
                      onChange={(e) => update(pi, (x) => ({ ...x, items: x.items.map((y) => (y === it ? { ...y, count: clamp(Number(e.target.value), 0, x.max_count) } : y)) }))}
                    />
                    <button className="btn btn-sm" aria-label="Remove" onClick={() => update(pi, (x) => ({ ...x, items: x.items.filter((y) => y !== it) }))}>
                      ✕
                    </button>
                  </div>
                ))}
              </div>
              {p.items.length < p.capacity && (
                <div className="mt-2 flex items-center gap-2">
                  <div className="flex-1">
                    <Combobox value={adding[p.type]?.id ?? 0} label={adding[p.type]?.name ?? ""} placeholder="Add an item…" options={items} onChange={(o) => setAdding({ ...adding, [p.type]: { id: Number(o.value), name: o.label, qty: 1 } })} />
                  </div>
                  <button
                    className="btn btn-sm"
                    onClick={() => {
                      const a = adding[p.type];
                      if (!a?.id || p.items.some((i) => i.index === a.id)) return;
                      update(pi, (x) => ({ ...x, items: [...x.items, { index: a.id, name: a.name, count: 1 }] }));
                      setAdding({ ...adding, [p.type]: { id: 0, name: "", qty: 1 } });
                    }}
                  >
                    Add
                  </button>
                </div>
              )}
            </section>
          ))}
        </div>
      )}
      {pouches && (
        <div className="mt-4 flex items-center gap-3">
          <button className="btn btn-primary" onClick={save}>
            Save bag
          </button>
          {out && <span className="text-sm text-muted">{out}</span>}
        </div>
      )}
    </Modal>
  );
}
