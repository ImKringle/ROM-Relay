import type { ReactNode } from "react";
import { dexNumber, sprite, tidy, TYPE_COLORS } from "../lib/pokemon";

// Small images hide themselves when the file is missing (a very new species
// has no sprite yet) rather than showing a broken-image icon.
const hide = (e: React.SyntheticEvent<HTMLImageElement>) => (e.currentTarget.style.visibility = "hidden");

export function Sprite({ dex, shiny, size = 72, dim, alt = "" }: { dex: number; shiny?: boolean; size?: number; dim?: boolean; alt?: string }) {
  return (
    <img
      src={sprite(dex, shiny)}
      alt={alt}
      width={size}
      height={size}
      loading="lazy"
      onError={hide}
      className={`mx-auto block [image-rendering:pixelated] ${dim ? "opacity-55 brightness-[.35] grayscale" : ""}`}
    />
  );
}

export function TypeIcon({ type, size = 20 }: { type: string; size?: number }) {
  return <img src={`/assets/typebadges/${type}.png`} alt="" width={size} height={size} onError={hide} />;
}

export function TypeChip({ type }: { type: string }) {
  return <img src={`/assets/typebadges/${type}.png`} alt={tidy(type)} width={64} height={28} onError={hide} className="inline-block" />;
}

export function ShinyIcon({ className = "size-3.5" }: { className?: string }) {
  return <img src="/assets/shiny/shiny.png" alt="Shiny" className={className} />;
}

export function GenderIcon({ which }: { which: "male" | "female" }) {
  return <img src={`/assets/gender/${which}.png`} alt={which === "male" ? "Male" : "Female"} width={16} height={16} className="inline-block" />;
}

// DexCell: one species tile in a grid.
export function DexCell({ dex, name, owned = true, shiny, number, title, onOpen }: { dex: number; name: string; owned?: boolean; shiny?: boolean; number?: number; title?: string; onOpen: () => void }) {
  return (
    <button
      type="button"
      title={title}
      onClick={onOpen}
      className="relative rounded border-2 border-line bg-panel px-1 pb-1 pt-1.5 text-center transition-colors hover:border-cyan"
    >
      <Sprite dex={dex} shiny={owned && shiny} dim={!owned} alt={name} />
      {owned && shiny && (
        <span className="absolute right-1.5 top-1">
          <ShinyIcon />
        </span>
      )}
      <div className="pixel text-[0.5rem] text-muted">{dexNumber(number ?? dex)}</div>
      <div className={`truncate text-[0.72rem] capitalize ${owned ? "" : "text-muted"}`}>{name}</div>
    </button>
  );
}

export function Pill({ active, onClick, title, children }: { active?: boolean; onClick: () => void; title?: string; children: ReactNode }) {
  return (
    <button type="button" className="chip" aria-pressed={!!active} title={title} onClick={onClick}>
      {children}
    </button>
  );
}

export function TypeTile({ type, mult, state }: { type: string; mult: number; state: string }) {
  return (
    <div
      className={`grid place-items-center rounded border-2 p-1 ${state === "quad" ? "border-danger" : "border-line"}`}
      style={{ background: `${TYPE_COLORS[type]}33` }}
      title={`${tidy(type)}: ×${mult}`}
    >
      <TypeIcon type={type} />
    </div>
  );
}
