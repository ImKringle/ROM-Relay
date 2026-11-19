// Shared Pokémon display helpers.

// Same-origin sprite route (internal/dex.SpriteHandler), keyed by National dex number.
export function sprite(dex: number, shiny?: boolean) {
  return `/sprites/pokemon/${shiny ? "shiny/" : ""}${dex}.png`;
}

export const dexNumber = (n: number) => "#" + String(n).padStart(3, "0");
export const tidy = (v?: string) => (v || "").replaceAll("-", " ");
export const titleCase = (v: string) => v.replace(/\w\S*/g, (w) => w[0].toUpperCase() + w.slice(1));
export const money = (n?: number) => "₽" + (n || 0).toLocaleString();
export function minutes(total?: number) {
  const h = Math.floor((total || 0) / 60);
  const m = (total || 0) % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}
export const ROMAN = ["", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X"];

export const ALL_TYPES = ["normal", "fire", "water", "electric", "grass", "ice", "fighting", "poison", "ground", "flying", "psychic", "bug", "rock", "ghost", "dragon", "dark", "steel", "fairy"];

// Type identity colours: franchise-wide, fixed across the UI.
export const TYPE_COLORS: Record<string, string> = {
  normal: "#9a9370",
  fire: "#ee6b3c",
  water: "#4f9fe0",
  electric: "#f2c53d",
  grass: "#6bbf4f",
  ice: "#7fd8d0",
  fighting: "#c0524a",
  poison: "#a259c4",
  ground: "#cf9a4a",
  flying: "#93a9f2",
  psychic: "#ef6ea0",
  bug: "#a3c23e",
  rock: "#b7a468",
  ghost: "#6e5a9e",
  dragon: "#6c5ce0",
  dark: "#5a4a42",
  steel: "#9aa8b8",
  fairy: "#ee99d6",
};
