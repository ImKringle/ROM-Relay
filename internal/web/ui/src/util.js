// Small helpers shared by the page modules.

// tokenSuffix carries the viewer token, when one is in play, onto requests this
// script makes. Handheld-friendly deployments authenticate by token in the URL,
// so a fetch that drops it would 401 on an otherwise working page.
export function tokenSuffix() {
  const token = new URLSearchParams(location.search).get("token");
  return token ? "?token=" + encodeURIComponent(token) : "";
}

// element builds a node with its class and text set, which is most of what the
// list rendering does. textContent, never innerHTML: these strings include
// user-supplied filenames and Pokémon nicknames.
export function element(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}

// spriteURL is the same-origin sprite route (internal/dex.SpriteHandler),
// pixel art keyed by National dex number. Shared by the Pokédex grid and
// PokeHome's box slots, one Pokémon, one icon convention everywhere it
// appears.
export function spriteURL(dex, shiny) {
  return `/sprites/pokemon/${shiny ? "shiny/" : ""}${dex}.png` + tokenSuffix();
}

// itemSlug turns a PKHeX display name ("King's Rock", "Poké Ball") into the
// PokéAPI icon filename convention: lowercase, apostrophes/accents dropped
// rather than dashed (PokéAPI's own item slugs do this, "kings-rock", not
// "king-s-rock" - and "poke-ball", not "pok-ball"), everything else
// non-alphanumeric collapsed to one dash.
export function itemSlug(name) {
  return name
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "") // strip combining accents (e-acute -> e), not the base letter
    .toLowerCase()
    .replace(/['’.]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

// itemIconURL is the vendored item-sprite route (internal/dex.SpriteHandler
// serves internal/dex/assets/sprites/items alongside .../pokemon). Not every
// held item has an icon in the vendored set, pair with noIconFallback so a
// miss shows a clear "no icon" placeholder instead of a broken-image glyph
// (or, worse, just vanishing).
export function itemIconURL(name) {
  return `/sprites/items/${itemSlug(name)}.png` + tokenSuffix();
}

// NO_ICON_SRC is a small inline "no icon" placeholder (a dashed box with a
// slash), no extra asset file needed, and it renders even if every other
// asset route is unreachable.
export const NO_ICON_SRC =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'>" +
      "<rect x='1.5' y='1.5' width='21' height='21' rx='5' fill='none' stroke='#8892a6' stroke-width='1.5' stroke-dasharray='3 2'/>" +
      "<path d='M6 6l12 12' stroke='#8892a6' stroke-width='1.5' stroke-linecap='round'/>" +
      "</svg>",
  );

// noIconFallback is the standard onerror handler for an itemIconURL <img>: a
// missing sprite gets the placeholder once, not an infinite error loop if the
// placeholder itself somehow 404s.
export function noIconFallback(img) {
  img.onerror = null;
  img.src = NO_ICON_SRC;
  img.title = img.alt ? `${img.alt} (no icon available)` : "No icon available";
}

// toast shows a small dismissable notice anchored to the viewport, for a
// one-off explanation that doesn't need to occupy permanent page layout
// (a persistent paragraph under every page's H1 gets skimmed past once and
// then just wastes space forever). Auto-dismisses after `duration` ms unless
// `sticky` is set; the close button always works regardless.
export function toast(message, opts) {
  const el = element("div", "toast", message);
  const close = document.createElement("button");
  close.type = "button";
  close.className = "toast-close";
  close.setAttribute("aria-label", "Dismiss");
  close.textContent = "×";
  el.append(close);
  document.body.append(el);
  requestAnimationFrame(() => el.classList.add("toast--visible"));
  let dismissed = false;
  const dismiss = () => {
    if (dismissed) return;
    dismissed = true;
    el.classList.remove("toast--visible");
    setTimeout(() => el.remove(), 200);
  };
  close.addEventListener("click", dismiss);
  if (!opts || !opts.sticky) setTimeout(dismiss, (opts && opts.duration) || 7000);
  return dismiss;
}

// toastOnce shows a toast at most once ever per browser (tracked in
// localStorage by `key`), for a first-visit explanation that would just be
// noise on every subsequent page load.
export function toastOnce(key, message, opts) {
  const flag = `romrelay:toast-seen:${key}`;
  try {
    if (localStorage.getItem(flag)) return;
    localStorage.setItem(flag, "1");
  } catch {
    // Storage unavailable (private browsing, quota), show it anyway rather
    // than silently never explaining anything.
  }
  toast(message, opts);
}

export function formatSize(bytes) {
  return bytes >= 1048576
    ? (bytes / 1048576).toFixed(1) + " MB"
    : Math.max(1, Math.round(bytes / 1024)) + " KB";
}

// describeUpload turns an upload response into the one line a person needs.
//
// A re-upload of identical bytes is deduplicated, but it can still read the save
// for the first time, when the parser was unreachable on the original upload,
// and that fills in the Dex. Saying only "already stored" there would hide the
// thing the person came to do.
export function describeUpload(result) {
  if (result.stored === false) {
    const same = "Already stored, identical to your latest save.";
    if (result.parsed && result.inserted > 0) {
      return `${same} Dex updated: ${result.inserted} new from ${result.pokemon_found || 0} read.`;
    }
    return same;
  }
  if (result.parsed) {
    return `Stored. ${result.pokemon_found || 0} Pokémon read, ${result.inserted || 0} new in your Dex.`;
  }
  return result.detail || "Stored.";
}
