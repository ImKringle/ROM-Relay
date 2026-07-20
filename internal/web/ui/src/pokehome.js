// PokeHome: the PC box browser, the withdrawal destination picker, and the
// Pokémon editor. Three surfaces, one box-grid component (see .pcbox/.pcslot
// in app.css) and one data model, a deposited Pokémon is the same object
// whether it is being browsed, sent somewhere, or edited.
import { element, itemIconURL, noIconFallback, spriteURL, tokenSuffix } from "./util.js";
import { wireUploadPanel } from "./saves.js";

// Real games' boxes are 6 columns x 5 rows. PokeHome's own storage has no
// real box/slot positions (deposits are a flat list), this is the size a
// virtual box is paginated into, purely for the browsing view.
const BOX_SIZE = 30;

const STATS = [
  ["hp", "HP"], ["atk", "Atk"], ["def", "Def"], ["spa", "SpA"], ["spd", "SpD"], ["spe", "Spe"],
];

// genderBadge: a small corner icon for a mon's actual gender, omitted for
// genderless (mon.gender is "" or "-") since there's nothing to badge.
export function genderBadge(mon, className) {
  if (mon.gender !== "M" && mon.gender !== "F") return null;
  const icon = document.createElement("img");
  icon.className = className;
  icon.src = mon.gender === "M" ? "/assets/gender/male.png" : "/assets/gender/female.png";
  icon.alt = mon.gender === "M" ? "Male" : "Female";
  return icon;
}

export function shinyBadge(className) {
  const icon = document.createElement("img");
  icon.className = className;
  icon.src = "/assets/shiny/shiny.png";
  icon.alt = "Shiny";
  return icon;
}

// typeBadge: the same painted type-chart badge the Pokédex species panel
// uses (internal/web/ui/dist/typebadges), colour and label both come from
// the art, sized down for the editor's tighter header row.
function typeBadge(type) {
  const img = document.createElement("img");
  img.className = "editor-type-badge";
  img.src = `/assets/typebadges/${type}.png`;
  img.alt = type;
  img.width = 64;
  img.height = 28;
  return img;
}

// ---- Reference data: species/abilities/items/moves/balls/natures --------
//
// One fetch, cached for the page's lifetime, these lists are the sidecar's
// own PKHeX name tables (see /api/reference), so a selection made from one
// of them is guaranteed to round-trip through an edit or create call rather
// than fail as "unknown ability".

let referencePromise = null;
function loadReference() {
  if (!referencePromise) referencePromise = api("/api/reference");
  return referencePromise;
}

// combobox turns a plain text input into a searchable dropdown: type to
// filter, click/Enter to choose. `options` is [{value, label}]; choosing one
// sets the input's display text to its label and stashes the real value in
// dataset.value (read that back, not input.value, for anything that isn't
// itself the free-text label, a ball or move id, not an ability name).
//
// The list is appended to <body>, positioned with `position: fixed` computed
// from the input's own rect, rather than living next to the input in
// `.combobox-wrap`, every combobox here lives inside `.modal`, which is
// `overflow-y: auto` (it's the thing that scrolls when a form is taller than
// the screen), and an absolutely-positioned child near the bottom of that
// box renders outside its clipped area and simply never appears. Anchoring
// to the viewport instead of the nearest scrollable ancestor is the same fix
// class as the topnav's earlier overflow-clip bug.
export function combobox(input, options) {
  const wrap = element("div", "combobox-wrap");
  input.replaceWith(wrap);
  wrap.append(input);
  input.autocomplete = "off";

  const list = element("div", "combobox-list combobox-list--floating");
  list.hidden = true;
  document.body.append(list);

  let filtered = [];
  let activeIndex = -1;

  // Flips the list above the field when it would otherwise run off the
  // bottom of the viewport and there's more room above than below - the
  // "dropdown clipped at the bottom of a modal" case.
  function reposition() {
    const r = input.getBoundingClientRect();
    const spaceBelow = window.innerHeight - r.bottom;
    const spaceAbove = r.top;
    const flipUp = list.offsetHeight > spaceBelow - 8 && spaceAbove > spaceBelow;
    list.style.left = `${r.left}px`;
    list.style.width = `${r.width}px`;
    if (flipUp) {
      list.style.top = "";
      list.style.bottom = `${window.innerHeight - r.top + 4}px`;
    } else {
      list.style.bottom = "";
      list.style.top = `${r.bottom + 4}px`;
    }
  }

  // renderList takes the filter query explicitly rather than always reading
  // input.value: after choose()/setValue() the input already displays the
  // chosen label, and filtering *that* against itself on the very next focus
  // would show just the one row that already matches, see the focus
  // listener below, which opens with an empty query instead.
  function renderList(query) {
    const q = query.trim().toLowerCase();
    filtered = (q ? options.filter((o) => o.label.toLowerCase().includes(q)) : options).slice(0, 40);
    activeIndex = -1;
    list.replaceChildren();
    if (!filtered.length) {
      list.append(element("div", "combobox-empty", q ? "No matches." : "Type to search…"));
    } else {
      for (const opt of filtered) {
        const row = element("div", "combobox-option");
        if (opt.icon) {
          const img = document.createElement("img");
          img.className = "combobox-option-icon";
          img.src = opt.icon;
          img.alt = "";
          img.loading = "lazy";
          img.onerror = () => noIconFallback(img);
          row.append(img);
        }
        row.append(document.createTextNode(opt.label));
        row.addEventListener("mousedown", (event) => {
          event.preventDefault();
          choose(opt);
        });
        list.append(row);
      }
    }
    list.hidden = false;
    reposition();
  }

  // render is the "user is typing" path: filter by whatever is currently in
  // the field. See renderList's comment for why focus doesn't use this.
  function render() {
    renderList(input.value);
  }

  function choose(opt) {
    input.value = opt.label;
    input.dataset.value = opt.value;
    list.hidden = true;
    // Setting .value/.dataset directly does not fire a native "input" event,
    // so every listener keyed off typing (refreshSprite/updateSpeciesIcon,
    // updateHeldItemIcon, ...) silently never ran for a dropdown pick, only
    // for whatever the user typed afterward. Dispatching one here means a
    // caller's existing "input" listener is the only wiring it ever needs,
    // for typing or picking alike.
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function highlight() {
    Array.from(list.children).forEach((row, i) => row.classList.toggle("combobox-option--active", i === activeIndex));
    list.children[activeIndex]?.scrollIntoView({ block: "nearest" });
  }

  // Focusing a field that already has a chosen value should browse the full
  // list, not self-filter down to the one row already matching what's
  // displayed, that's the "ball dropdown only shows the set option" bug.
  // Real filtering only kicks in once the "input" event fires below, i.e.
  // once the person actually types.
  input.addEventListener("focus", () => renderList(""));
  input.addEventListener("input", render);
  input.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      activeIndex = Math.min(activeIndex + 1, filtered.length - 1);
      highlight();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      activeIndex = Math.max(activeIndex - 1, 0);
      highlight();
    } else if (event.key === "Enter" && activeIndex >= 0 && filtered[activeIndex]) {
      event.preventDefault();
      choose(filtered[activeIndex]);
    } else if (event.key === "Escape") {
      list.hidden = true;
    }
  });
  document.addEventListener("click", (event) => {
    if (!wrap.contains(event.target) && !list.contains(event.target)) list.hidden = true;
  });
  // Capture-phase: a modal's own scroll (or any ancestor's) fires a "scroll"
  // event that only reaches a capture listener, never one added on window in
  // the bubble phase, scroll events don't bubble.
  document.addEventListener("scroll", () => {
    if (!list.hidden) reposition();
  }, true);
  window.addEventListener("resize", () => {
    if (!list.hidden) reposition();
  });

  return {
    setOptions(next) {
      options = next;
    },
    // setValue displays a known value/label pair (e.g. this Pokémon's current
    // ball) without the user having picked it from the list.
    setValue(value, label) {
      input.value = label ?? "";
      input.dataset.value = value ?? "";
    },
  };
}

export async function api(path, options) {
  const response = await fetch(path + tokenSuffix(), options);
  const text = await response.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // A non-JSON error body (a bare http.Error string) is still the message.
  }
  if (!response.ok) {
    const message = (body && body.error) || text || `Request failed (${response.status})`;
    const err = new Error(message);
    // A legality rejection's per-line report (see the sidecar's
    // LegalityViolations and writeSidecarError) rides along on the thrown
    // error so a caller like the Creator/Editor can list it, not just show
    // the one-line summary.
    if (body && Array.isArray(body.report)) err.report = body.report;
    throw err;
  }
  return body;
}

// isNicknamed reports whether a Pokémon's nickname is worth showing
// separately from its species name. An un-nicknamed Pokémon's saved
// "nickname" is literally its species name in caps (a Game Boy/DS-era
// convention that survives all the way to modern saves), so a plain string
// comparison reads every ordinary Exeggcute as if it had been renamed
// "EXEGGCUTE".
// renderErrorReport shows a caught error in `target`: a plain message, or,
// for a legality rejection (err.report, see api()'s handling of the
// sidecar's LegalityViolations output), the per-line violations as a real
// list instead of one run-on sentence.
function renderErrorReport(target, err) {
  if (err.report && err.report.length) {
    target.replaceChildren(element("p", "m-0 mb-1 font-semibold text-[var(--color-danger)]", err.message));
    const list = document.createElement("ul");
    list.className = "m-0 list-disc pl-5 text-[var(--color-danger)]";
    for (const line of err.report) list.append(element("li", "", line));
    target.append(list);
  } else {
    target.textContent = err.message;
  }
}

export function isNicknamed(mon) {
  return mon.nickname && mon.nickname.toUpperCase() !== (mon.species_name || "").toUpperCase();
}

export function describeMon(mon) {
  const bits = [`Lv. ${mon.level}`];
  if (mon.nature) bits.push(mon.nature);
  if (mon.shiny) bits.push("Shiny");
  return bits.join(" · ");
}

// openMonDetail shows one Pokémon's detail modal, species, level, badges,
// with Edit and Send-to-a-save actions. Shared by PokeHome's own box browser
// and the standalone Editor page's save-Pokémon list: same modal
// (#slot-modal, see partials/pokemon-editor-modals.html), same two follow-on
// flows, different only in where the mon list itself came from.
export function openMonDetail(mon, editor, withdraw, onChanged) {
  const slotScrim = document.querySelector("[data-slot-scrim]");
  if (!slotScrim) return;
  document.querySelector("#slot-sprite").src = spriteURL(mon.species, mon.shiny);
  document.querySelector("#slot-name").textContent = isNicknamed(mon)
    ? `${mon.nickname} (${mon.species_name})`
    : mon.species_name;
  document.querySelector("#slot-meta").textContent = describeMon(mon);
  const badges = document.querySelector("#slot-badges");
  badges.replaceChildren();
  if (mon.origin_game) badges.append(element("span", "badge", mon.origin_game));
  if (mon.ability) badges.append(element("span", "badge", mon.ability));
  if (mon.held_item) badges.append(element("span", "badge", mon.held_item));
  document.querySelector("#slot-deposited").textContent = mon.home_deposited_at
    ? `Deposited ${new Date(mon.home_deposited_at).toLocaleString()}${
        mon.home_last_withdrawn_at ? ` · last sent out ${new Date(mon.home_last_withdrawn_at).toLocaleString()}` : ""
      }`
    : "";
  slotScrim.dataset.open = "true";

  document.querySelector("#slot-withdraw-open").onclick = () => {
    slotScrim.dataset.open = "false";
    withdraw.open(mon, onChanged);
  };
  document.querySelector("#slot-edit-open").onclick = () => {
    slotScrim.dataset.open = "false";
    editor.open(mon, onChanged);
  };
  document.querySelector("#slot-delete").onclick = async () => {
    const label = isNicknamed(mon) ? `${mon.nickname} (${mon.species_name})` : mon.species_name;
    if (!confirm(`Delete ${label}? This removes it from storage entirely and cannot be undone.`)) return;
    try {
      await api(`/api/pokemon/${encodeURIComponent(mon.id)}`, { method: "DELETE" });
      slotScrim.dataset.open = "false";
      if (onChanged) onChanged();
    } catch (err) {
      alert(err.message);
    }
  };
}

// slotCell renders one PC slot: empty, occupied (browsable), or occupied
// (picker mode, inert) / empty (picker mode, pickable). One function for
// every state .pcslot defines, so the browsing grid and the withdrawal grid
// never draw a slot two different ways.
function slotCell(mon, opts) {
  const picker = opts && opts.picker;
  if (!mon) {
    const cell = element("div", "pcslot" + (picker ? " pcslot--pickable" : " pcslot--empty"));
    if (picker) {
      cell.tabIndex = 0;
      cell.title = "Empty, send this Pokémon here";
      cell.addEventListener("click", () => opts.onPick());
      cell.addEventListener("keydown", (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          opts.onPick();
        }
      });
    }
    return cell;
  }
  // An occupied slot in picker mode used to be fully inert (opts.onPick did
  // not exist for it at all), clicking did nothing but show a tooltip. It's
  // now clickable too, when the caller supplies onOccupiedClick, offering to
  // evict the current occupant into PokeHome rather than just refusing.
  const clickable = !picker || (picker && opts.onOccupiedClick);
  const cell = element("div", "pcslot pcslot--occupied" + (picker && !opts.onOccupiedClick ? " pcslot--inert" : ""));
  const image = document.createElement("img");
  image.className = "pcslot-sprite";
  image.alt = mon.species_name || "";
  image.loading = "lazy";
  image.src = spriteURL(mon.species, mon.shiny);
  cell.append(image);
  cell.append(element("span", "pcslot-level", "Lv" + mon.level));
  if (mon.shiny) cell.append(shinyBadge("pcslot-shiny"));
  const gBadge = genderBadge(mon, "pcslot-gender");
  if (gBadge) cell.append(gBadge);
  if (clickable) {
    const handler = () => (picker ? opts.onOccupiedClick(mon) : opts.onOpen(mon));
    cell.title = picker
      ? `${mon.species_name}, occupied. Click to swap it to PokeHome and place yours here.`
      : `${mon.species_name}${isNicknamed(mon) ? ` "${mon.nickname}"` : ""}, ${describeMon(mon)}`;
    cell.tabIndex = 0;
    cell.addEventListener("click", handler);
    cell.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        handler();
      }
    });
  } else {
    cell.title = `${mon.species_name}, occupied`;
  }
  return cell;
}

// ---- PokeHome's own storage: browse deposited Pokémon as PC boxes --------

function wirePokehomeBrowser(editor, withdraw) {
  const root = document.querySelector("#pokehome-box");
  if (!root) return;

  const stats = document.querySelector("#pokehome-stats");
  const grid = document.querySelector("#pokehome-grid");
  const nameEl = document.querySelector("#pokehome-box-name");
  const pageEl = document.querySelector("#pokehome-box-page");
  const empty = document.querySelector("#pokehome-empty");
  const prev = document.querySelector("#pokehome-prev");
  const next = document.querySelector("#pokehome-next");

  let deposited = [];
  let boxIndex = 0;

  function boxCount() {
    return Math.max(1, Math.ceil(deposited.length / BOX_SIZE));
  }

  function render() {
    const count = boxCount();
    boxIndex = Math.max(0, Math.min(boxIndex, count - 1));
    const start = boxIndex * BOX_SIZE;
    const inBox = deposited.slice(start, start + BOX_SIZE);
    nameEl.textContent = `BOX ${boxIndex + 1}`;
    pageEl.textContent = `${boxIndex + 1} of ${count}`;
    prev.disabled = boxIndex === 0;
    next.disabled = boxIndex >= count - 1;
    grid.replaceChildren();
    for (let i = 0; i < BOX_SIZE; i++) {
      grid.append(slotCell(inBox[i], { onOpen: (mon) => openMonDetail(mon, editor, withdraw, load) }));
    }
    root.hidden = deposited.length === 0;
    empty.hidden = deposited.length !== 0;
    stats.textContent = deposited.length
      ? `${deposited.length} Pokémon deposited, across ${count} box${count === 1 ? "" : "es"}.`
      : "";
  }

  async function load() {
    stats.textContent = "Loading…";
    try {
      const result = await api("/api/pokehome");
      // Oldest deposit first, so a box a Pokémon already lives in never shifts
      // when a new one is deposited, new arrivals only ever extend the last
      // box, they never push earlier ones around.
      deposited = (result.pokemon || []).slice().sort(
        (a, b) => new Date(a.home_deposited_at) - new Date(b.home_deposited_at),
      );
    } catch (err) {
      stats.textContent = err.message;
      deposited = [];
    }
    render();
  }

  prev.addEventListener("click", () => {
    boxIndex--;
    render();
  });
  next.addEventListener("click", () => {
    boxIndex++;
    render();
  });

  wireDepositFlow(load);

  const createOpener = document.querySelector("[data-create-open]");
  if (createOpener) {
    createOpener.addEventListener("click", () => editor.openCreate(load));
  }

  load();
}

// ---- Deposit: pick/upload a save, then check off which Pokémon to keep ---

function wireDepositFlow(onDeposited) {
  const opener = document.querySelector("[data-deposit-open]");
  const scrim = document.querySelector("[data-deposit-scrim]");
  if (!opener || !scrim) return;

  const stepSave = document.querySelector("#deposit-step-save");
  const stepMons = document.querySelector("#deposit-step-mons");
  const saveList = document.querySelector("#deposit-save-list");
  const monGrid = document.querySelector("#deposit-mon-grid");
  const monEmpty = document.querySelector("#deposit-mon-empty");
  const out = document.querySelector("#deposit-out");
  const confirmButton = document.querySelector("#deposit-confirm");
  const backButton = document.querySelector("#deposit-back");

  let selected = new Set();

  async function showSaveStep() {
    stepMons.hidden = true;
    stepSave.hidden = false;
    out.textContent = "";
    saveList.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", "Loading your saves…"));
    try {
      const { saves } = await api("/api/saves");
      saveList.replaceChildren();
      if (!saves || !saves.length) {
        saveList.append(element("p", "text-sm text-[var(--color-muted)]", "No saves yet, upload one below."));
        return;
      }
      for (const save of saves) {
        const row = element("button", "destrow");
        row.type = "button";
        row.append(element("span", "flex-1 font-semibold", save.game_label || save.file_name));
        if (save.parsed) row.append(element("span", "badge", `${save.pokemon_count || 0} Pokémon`));
        row.addEventListener("click", () => showMonStep(save.id));
        saveList.append(row);
      }
    } catch (err) {
      saveList.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", err.message));
    }
  }

  async function showMonStep(saveID) {
    stepSave.hidden = true;
    stepMons.hidden = false;
    out.textContent = "";
    selected = new Set();
    monGrid.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-muted)]", "Loading…"));
    let owned;
    try {
      owned = (await api("/api/pokemon")).pokemon || [];
    } catch (err) {
      monGrid.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-danger)]", err.message));
      return;
    }
    const candidates = owned.filter((m) => m.origin_save_id === saveID && !m.home_deposited_at);
    monGrid.replaceChildren();
    monEmpty.hidden = candidates.length !== 0;
    for (const mon of candidates) {
      const tile = element("label", "depositslot");
      tile.title = `${mon.species_name}, ${describeMon(mon)}${mon.held_item ? `, holding ${mon.held_item}` : ""}`;
      const image = document.createElement("img");
      image.className = "depositslot-sprite";
      image.alt = mon.species_name;
      image.src = spriteURL(mon.species, mon.shiny);
      tile.append(image);
      tile.append(element("span", "depositslot-name", isNicknamed(mon) ? mon.nickname : mon.species_name));
      tile.append(element("span", "depositslot-lv", "Lv" + mon.level));
      if (mon.shiny) tile.append(shinyBadge("depositslot-shiny"));
      const gBadge = genderBadge(mon, "depositslot-gender");
      if (gBadge) tile.append(gBadge);
      if (mon.held_item) {
        const item = document.createElement("img");
        item.className = "depositslot-item";
        item.alt = mon.held_item;
        item.src = itemIconURL(mon.held_item);
        item.loading = "lazy";
        item.onerror = () => noIconFallback(item);
        tile.append(item);
      }
      const check = document.createElement("input");
      check.type = "checkbox";
      check.className = "sr-only";
      check.addEventListener("change", () => {
        if (check.checked) selected.add(mon.id);
        else selected.delete(mon.id);
      });
      tile.prepend(check);
      monGrid.append(tile);
    }
  }

  confirmButton.addEventListener("click", async () => {
    if (!selected.size) {
      out.textContent = "Select at least one Pokémon first.";
      return;
    }
    out.textContent = "Depositing…";
    let ok = 0;
    for (const id of selected) {
      try {
        await api(`/api/pokehome/${encodeURIComponent(id)}/deposit`, { method: "POST" });
        ok++;
      } catch (err) {
        out.textContent = err.message;
        return;
      }
    }
    out.textContent = `Deposited ${ok} Pokémon.`;
    onDeposited();
    setTimeout(() => {
      scrim.dataset.open = "false";
    }, 700);
  });

  backButton.addEventListener("click", showSaveStep);
  opener.addEventListener("click", () => {
    scrim.dataset.open = "true";
    showSaveStep();
  });

  wireUploadPanel((result) => {
    // The shared upload-panel callback list fires for every panel on the
    // page (see saves.js), only react when this flow's own step is the one
    // showing, or an upload through the withdraw modal would also jump this
    // one to its mon-selection step.
    if (scrim.dataset.open === "true" && !stepSave.hidden) showMonStep(result.save_id);
  });
}

// ---- Withdraw: pick a destination save, then its box grid ----------------

export function wireWithdrawFlow() {
  const scrim = document.querySelector("[data-withdraw-scrim]");
  if (!scrim) return { open: () => {} };

  const stepSave = document.querySelector("#withdraw-step-save");
  const stepBoxes = document.querySelector("#withdraw-step-boxes");
  const saveList = document.querySelector("#withdraw-save-list");
  const grid = document.querySelector("#withdraw-grid");
  const nameEl = document.querySelector("#withdraw-box-name");
  const pageEl = document.querySelector("#withdraw-box-page");
  const prev = document.querySelector("#withdraw-prev");
  const next = document.querySelector("#withdraw-next");
  const backButton = document.querySelector("#withdraw-back");
  const out = document.querySelector("#withdraw-out");

  let mon = null;
  let onChanged = null;
  let layout = null; // { box_count, box_slot_count, occupied }
  let boxIndex = 0;
  let destSaveID = null;
  let destLabel = "";

  function occupantsForBox(index) {
    const map = new Map();
    for (const [slotKey, occ] of Object.entries(layout.occupied || {})) {
      const [, box, slot] = slotKey.split(":").map((v, i) => (i === 0 ? v : Number(v)));
      if (box === index + 1) map.set(slot, occ);
    }
    return map;
  }

  function renderBoxes() {
    const inBox = occupantsForBox(boxIndex);
    nameEl.textContent = `BOX ${boxIndex + 1}`;
    pageEl.textContent = `${boxIndex + 1} of ${layout.box_count}`;
    prev.disabled = boxIndex === 0;
    next.disabled = boxIndex >= layout.box_count - 1;
    grid.replaceChildren();
    for (let slot = 1; slot <= layout.box_slot_count; slot++) {
      grid.append(
        slotCell(inBox.get(slot) || null, {
          picker: true,
          onPick: () => confirmWithdraw(boxIndex + 1, slot),
          onOccupiedClick: (occupant) => confirmWithdraw(boxIndex + 1, slot, occupant),
        }),
      );
    }
  }

  async function showBoxes(saveID, label) {
    destSaveID = saveID;
    destLabel = label;
    stepSave.hidden = true;
    stepBoxes.hidden = false;
    boxIndex = 0;
    out.textContent = "Loading boxes…";
    try {
      layout = await api(`/api/pokehome/boxes?save_id=${encodeURIComponent(saveID)}`);
      out.textContent = "";
      renderBoxes();
    } catch (err) {
      out.textContent = err.message;
    }
  }

  // refreshOccupancy re-fetches the destination's box layout after a
  // withdrawal, without resetting the box the picker is looking at or
  // touching `out`, a plain showBoxes() call would stomp the "Sent. ..."
  // download link confirmWithdraw just wrote there with its own "Loading
  // boxes…" text, wiping it out before anyone could read it.
  async function refreshOccupancy() {
    try {
      layout = await api(`/api/pokehome/boxes?save_id=${encodeURIComponent(destSaveID)}`);
      renderBoxes();
    } catch {
      // The withdrawal itself already succeeded; a stale grid until the next
      // manual action is a smaller problem than losing the success message.
    }
  }

  async function showSaveStep() {
    stepBoxes.hidden = true;
    stepSave.hidden = false;
    out.textContent = "";
    saveList.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", "Loading your saves…"));
    try {
      const { saves } = await api("/api/saves");
      saveList.replaceChildren();
      if (!saves || !saves.length) {
        saveList.append(element("p", "text-sm text-[var(--color-muted)]", "No saves yet, upload one below."));
        return;
      }
      for (const save of saves) {
        const row = element("button", "destrow");
        row.type = "button";
        row.append(element("span", "flex-1 font-semibold", save.game_label || save.file_name));
        row.addEventListener("click", () => showBoxes(save.id, save.game_label || save.file_name));
        saveList.append(row);
      }
    } catch (err) {
      saveList.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", err.message));
    }
  }

  async function confirmWithdraw(box, slot, occupant) {
    const message = occupant
      ? `Box ${box} slot ${slot} already has ${occupant.species_name}. Move it to PokeHome and put ${mon.species_name} there instead? This creates a new save version.`
      : `Send ${mon.species_name} to ${destLabel}, box ${box} slot ${slot}? This creates a new save version.`;
    if (!confirm(message)) return;
    out.textContent = occupant ? "Moving the occupant to PokeHome and sending…" : "Sending…";
    try {
      const result = await api(`/api/pokehome/${encodeURIComponent(mon.id)}/withdraw`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ save_id: destSaveID, box, slot, evict_to_home: !!occupant }),
      });
      // A tiny underlined text link here was easy to miss entirely, worse on
      // mobile, this is the one moment a person actually needs to grab the
      // result, so it gets two real, thumb-sized buttons instead.
      out.replaceChildren(element("p", "mb-2", "Sent."));
      const actions = element("div", "flex flex-wrap gap-2");
      const download = element("a", "button button--primary justify-center flex-1", "Download save");
      download.href = `/saves/${encodeURIComponent(result.save_id)}/download` + tokenSuffix();
      const viewInVault = element("a", "button justify-center flex-1", "View in Save Vault");
      viewInVault.href = "/saves" + tokenSuffix();
      actions.append(download, viewInVault);
      out.append(actions);
      if (onChanged) onChanged();
      // A withdrawal creates a new save version rather than editing destSaveID
      // in place (saves are immutable, see saves.go). A second withdrawal in
      // this same session has to build on that new version, not the one the
      // user originally picked, or it would silently branch off stale bytes
      // and the slot that was just filled would still show empty here.
      destSaveID = result.save_id;
      await refreshOccupancy();
    } catch (err) {
      out.textContent = err.message;
    }
  }

  prev.addEventListener("click", () => {
    boxIndex--;
    renderBoxes();
  });
  next.addEventListener("click", () => {
    boxIndex++;
    renderBoxes();
  });
  backButton.addEventListener("click", showSaveStep);

  wireUploadPanel((result) => {
    if (scrim.dataset.open === "true" && !stepSave.hidden) showBoxes(result.save_id, result.game || result.file_name);
  });

  return {
    open(openedMon, changed) {
      mon = openedMon;
      onChanged = changed;
      scrim.dataset.open = "true";
      showSaveStep();
    },
  };
}

// ---- Editor: PKHeX field edits --------------------------------------------

export function wireEditor(withdraw) {
  const scrim = document.querySelector("[data-editor-scrim]");
  if (!scrim) return { open: () => {}, openCreate: () => {} };

  const formBody = document.querySelector("#editor-form-body");
  const postcreate = document.querySelector("#editor-postcreate");
  const postcreateName = document.querySelector("#editor-postcreate-name");
  const postcreateDeposit = document.querySelector("#editor-postcreate-deposit");
  const postcreateDownload = document.querySelector("#editor-postcreate-download");
  const postcreateInject = document.querySelector("#editor-postcreate-inject");
  const postcreateDone = document.querySelector("#editor-postcreate-done");
  const postcreateOut = document.querySelector("#editor-postcreate-out");

  const title = document.querySelector("#editor-title");
  const intro = document.querySelector("#editor-intro");
  const typesBox = document.querySelector("#editor-types");
  const abilityHint = document.querySelector("#editor-ability-hint");
  const sprite = document.querySelector("#editor-sprite");
  const spriteWrap = document.querySelector("#editor-sprite-wrap");
  const newFields = document.querySelector("#editor-new-fields");
  const speciesInput = document.querySelector("#editor-species");
  const speciesIcon = document.querySelector("#editor-species-icon");
  const generationSelect = document.querySelector("#editor-generation");

  const ivsBox = document.querySelector("#editor-ivs");
  const evsBox = document.querySelector("#editor-evs");
  const ivInputs = {};
  const evInputs = {};
  for (const [key, label] of STATS) {
    ivInputs[key] = statField(ivsBox, key, label, 0, 31);
    evInputs[key] = statField(evsBox, key, label, 0, 252);
  }

  const movesBox = document.querySelector("#editor-moves");
  const moveInputs = [];

  const fields = {
    nickname: document.querySelector("#editor-nickname"),
    ot: document.querySelector("#editor-ot"),
    level: document.querySelector("#editor-level"),
    form: document.querySelector("#editor-form"),
    nature: document.querySelector("#editor-nature"),
    gender: document.querySelector("#editor-gender"),
    ability: document.querySelector("#editor-ability"),
    heldItem: document.querySelector("#editor-held-item"),
    heldItemIcon: document.querySelector("#editor-held-item-icon"),
    ball: document.querySelector("#editor-ball"),
    ballIcon: document.querySelector("#editor-ball-icon"),
    shiny: document.querySelector("#editor-shiny"),
  };
  const out = document.querySelector("#editor-out");
  const saveButton = document.querySelector("#editor-save");

  let mon = null;
  let mode = "edit"; // "edit" | "create"
  let onChanged = null;
  let original = {};
  let ref = null; // resolved /api/reference payload, once loaded

  clampOnBlur(fields.level, 1, 100);
  for (const [key] of STATS) {
    clampOnBlur(ivInputs[key], 0, 31);
    clampOnBlur(evInputs[key], 0, 252);
  }

  const speciesBox = combobox(speciesInput, []);
  const abilityBox = combobox(fields.ability, []);
  const itemBox = combobox(fields.heldItem, []);
  const ballBox = combobox(fields.ball, []);
  fields.heldItem.addEventListener("input", () => updateHeldItemIcon(fields.heldItem.value));
  fields.ball.addEventListener("input", () => updateBallIcon(fields.ball.dataset.value));

  async function ensureReference() {
    if (ref) return ref;
    ref = await loadReference();
    speciesBox.setOptions(ref.species.map((s) => ({ value: s.dex, label: `${s.name} (#${s.dex})`, icon: spriteURL(s.dex) })));
    abilityBox.setOptions(ref.abilities.map((a) => ({ value: a.name, label: a.name })));
    itemBox.setOptions(ref.items.map((i) => ({ value: i.name, label: i.name, icon: itemIconURL(i.name) })));
    ballBox.setOptions(ref.balls.map((b) => ({ value: b.id, label: `${b.name} (${b.id})`, icon: itemIconURL(b.name) })));
    populateNatures(ref.natures);
    return ref;
  }

  function populateNatures(natures) {
    const current = fields.nature.value;
    fields.nature.replaceChildren();
    for (const n of natures) {
      fields.nature.append(new Option(n.effect === "Neutral" ? n.name : `${n.name}, ${n.effect}`, n.name));
    }
    if (current) fields.nature.value = current;
  }

  function updateHeldItemIcon(name) {
    if (!name) {
      fields.heldItemIcon.hidden = true;
      return;
    }
    fields.heldItemIcon.src = itemIconURL(name);
    fields.heldItemIcon.alt = name;
    fields.heldItemIcon.hidden = false;
    fields.heldItemIcon.onerror = () => noIconFallback(fields.heldItemIcon);
  }

  // updateBallIcon mirrors updateHeldItemIcon, looked up by ball ID rather
  // than name (the field's underlying value, per the combobox's
  // value/label split) - the ball sprite set lives alongside the held-item
  // one (see itemIconURL), Poké Balls are just items PokeAPI's own sprite set
  // already covers.
  function updateBallIcon(ballId) {
    const ball = ref && ref.balls.find((b) => b.id === Number(ballId));
    if (!ball) {
      fields.ballIcon.hidden = true;
      return;
    }
    fields.ballIcon.src = itemIconURL(ball.name);
    fields.ballIcon.alt = ball.name;
    fields.ballIcon.hidden = false;
    fields.ballIcon.onerror = () => noIconFallback(fields.ballIcon);
  }

  // updateSpeciesIcon mirrors updateHeldItemIcon for the species field itself,
  // the big header sprite (refreshSprite) is out of view once the form is
  // scrolled down to Species, so the field needs its own small icon too.
  function updateSpeciesIcon(dex) {
    if (!dex) {
      speciesIcon.hidden = true;
      return;
    }
    speciesIcon.src = spriteURL(dex, fields.shiny.checked);
    speciesIcon.hidden = false;
    speciesIcon.onerror = () => noIconFallback(speciesIcon);
  }

  function refreshSprite(dex) {
    if (dex) {
      sprite.src = spriteURL(dex, fields.shiny.checked);
      spriteWrap.style.visibility = "visible";
    } else {
      spriteWrap.style.visibility = "hidden";
    }
    updateSpeciesIcon(dex);
  }
  fields.shiny.addEventListener("change", () => refreshSprite(mode === "create" ? Number(speciesInput.dataset.value || 0) : mon && mon.species));

  // refreshTypes mirrors the Pokédex species panel's type row, fetched from
  // the same /api/dex/species endpoint by national dex number. Pokédex
  // enrichment is optional (config.ini's [metadata.pokeapi]), so a missing
  // species just leaves the row empty rather than erroring the editor. Also
  // fills the ability hint alongside the types, from the same fetch, one
  // request already has both, no reason for a second round trip.
  let typesRequest = 0;
  async function refreshTypes(dex) {
    const token = ++typesRequest;
    typesBox.replaceChildren();
    abilityHint.textContent = "";
    // The dropdown only ever offers what's actually legal for the selected
    // species (see below); with no species chosen yet, that's nothing.
    abilityBox.setOptions([]);
    if (!dex) return;
    try {
      const { species } = await api(`/api/dex/species/${dex}`);
      if (token !== typesRequest) return;
      typesBox.replaceChildren(...(species.types || "").split("/").filter(Boolean).map(typeBadge));
      const legal = (species.abilities || "").split("/").filter(Boolean);
      const abilityOptions = legal.map((a) => ({ value: a, label: a }));
      if (species.hidden_ability) abilityOptions.push({ value: species.hidden_ability, label: `${species.hidden_ability} (Hidden)` });
      abilityBox.setOptions(abilityOptions);
      abilityHint.textContent = abilityOptions.length ? `Legal for this species: ${abilityOptions.map((o) => o.label).join(", ")}` : "";
      // A newly blanked Pokémon defaults to Ability 0, which the legality
      // gate rejects for almost every species, Create would 409 on its very
      // first try unless the person happened to already know to fill this
      // in themselves. Only in create mode, and only while the field is
      // still untouched, so picking a species doesn't clobber a choice
      // someone already made.
      if (mode === "create" && !fields.ability.value && legal.length) {
        abilityBox.setValue(legal[0], legal[0]);
      }
    } catch {
      // Left empty, see comment above.
    }
  }

  function statField(container, key, label, min, max) {
    const wrap = element("div", "statfield");
    wrap.append(element("label", "", label));
    const input = document.createElement("input");
    input.className = "field";
    input.type = "number";
    input.min = String(min);
    input.max = String(max);
    wrap.append(input);
    container.append(wrap);
    return input;
  }

  // clampOnBlur keeps a number field inside [min,max] once the person leaves
  // it, rather than fighting every keystroke, matching the real cap (31 IV,
  // 252 EV, 100 level) instead of just hinting at it with an HTML max that
  // typing can freely exceed.
  function clampOnBlur(input, min, max) {
    input.addEventListener("change", () => {
      if (input.value === "") return;
      input.value = String(Math.min(max, Math.max(min, Math.round(Number(input.value)) || 0)));
    });
  }

  function buildMoveInputs(moves) {
    movesBox.replaceChildren();
    moveInputs.length = 0;
    for (let i = 0; i < 4; i++) {
      const move = (moves || [])[i];
      const wrap = element("div", "statfield");
      wrap.append(element("label", "", `Move ${i + 1}`));
      const input = document.createElement("input");
      input.className = "field";
      input.type = "text";
      input.placeholder = "—";
      wrap.append(input);
      movesBox.append(wrap);
      const box = combobox(input, ref ? [{ value: 0, label: "— (empty)" }, ...ref.moves.map((m) => ({ value: m.id, label: m.name }))] : []);
      if (move) box.setValue(move.id, move.name);
      else input.dataset.value = "0";
      moveInputs.push(input);
    }
  }

  async function open(openedMon, changed) {
    await ensureReference();
    mon = openedMon;
    mode = "edit";
    onChanged = changed;
    out.textContent = "";
    formBody.hidden = false;
    postcreate.hidden = true;
    title.textContent = "Edit Pokémon";
    intro.textContent = "Changes are written back byte-for-byte through the same PKHeX library the Save Vault uses to read your saves. Leave a field as it is to keep it unchanged.";
    saveButton.textContent = "Save changes";
    newFields.hidden = true;
    refreshSprite(mon.species);
    refreshTypes(mon.species);

    fields.nickname.value = mon.nickname || "";
    fields.ot.value = mon.origin_ot || "";
    fields.level.value = mon.level || 1;
    fields.form.value = mon.form || 0;
    fields.nature.value = mon.nature || "Hardy";
    fields.gender.value = mon.gender || "-";
    fields.ability.value = mon.ability || "";
    fields.heldItem.value = mon.held_item || "";
    updateHeldItemIcon(mon.held_item);
    const ball = mon.ball || 4;
    const ballName = ref.balls.find((b) => b.id === ball);
    ballBox.setValue(ball, ballName ? `${ballName.name} (${ball})` : String(ball));
    updateBallIcon(ball);
    fields.shiny.checked = !!mon.shiny;
    for (const [key] of STATS) {
      ivInputs[key].value = (mon.ivs && mon.ivs[key]) || 0;
      evInputs[key].value = (mon.evs && mon.evs[key]) || 0;
    }
    buildMoveInputs(mon.moves);
    original = { ...mon };
    scrim.dataset.open = "true";
  }

  async function openCreate(changed) {
    await ensureReference();
    mon = null;
    mode = "create";
    onChanged = changed;
    out.textContent = "";
    formBody.hidden = false;
    postcreate.hidden = true;
    title.textContent = "Create Pokémon";
    intro.textContent = "Build a Pokémon from scratch through the same PKHeX library the Save Vault uses. Species and level are required, everything else is optional.";
    saveButton.textContent = "Create Pokémon";
    newFields.hidden = false;
    speciesBox.setValue("", "");
    generationSelect.value = "9";
    refreshSprite(null);
    refreshTypes(null);

    fields.nickname.value = "";
    fields.ot.value = "";
    fields.level.value = 5;
    fields.form.value = 0;
    fields.nature.value = "Hardy";
    fields.gender.value = "-";
    fields.ability.value = "";
    fields.heldItem.value = "";
    updateHeldItemIcon("");
    const poke = ref.balls.find((b) => b.id === 4);
    ballBox.setValue(4, poke ? `${poke.name} (4)` : "Poké Ball (4)");
    updateBallIcon(4);
    fields.shiny.checked = false;
    for (const [key] of STATS) {
      ivInputs[key].value = 0;
      evInputs[key].value = 0;
    }
    buildMoveInputs([]);
    scrim.dataset.open = "true";
  }

  speciesInput.addEventListener("input", () => {
    // The combobox choose() already set dataset.value; a species picked from
    // the list is the only thing this field accepts, so the sprite only
    // updates when that has actually happened (dataset.value survives a
    // fresh keystroke until the next choice, but a preview beats none).
    refreshSprite(Number(speciesInput.dataset.value || 0));
    refreshTypes(Number(speciesInput.dataset.value || 0));
  });

  saveButton.addEventListener("click", async () => {
    if (mode === "create") {
      await createMon();
    } else {
      await saveEdit();
    }
  });

  async function saveEdit() {
    const body = { generation: mon.generation };
    if (fields.nickname.value !== (original.nickname || "")) body.nickname = fields.nickname.value;
    if (fields.ot.value !== (original.origin_ot || "")) body.original_trainer_name = fields.ot.value;
    if (Number(fields.level.value) !== original.level) body.level = Number(fields.level.value);
    if (Number(fields.form.value) !== (original.form || 0)) body.form = Number(fields.form.value);
    if (fields.nature.value !== original.nature) body.nature = fields.nature.value;
    if (fields.gender.value !== original.gender) body.gender = fields.gender.value;
    if (fields.ability.value !== original.ability) body.ability = fields.ability.value;
    if (fields.heldItem.value !== (original.held_item || "")) body.held_item = fields.heldItem.value;
    const ball = Number(fields.ball.dataset.value || 0);
    if (ball && ball !== original.ball) body.ball = ball;
    if (fields.shiny.checked !== !!original.shiny) body.shiny = fields.shiny.checked;
    Object.assign(body, statDiffs(), moveDiff());

    out.textContent = "Saving…";
    try {
      await api(`/api/pokemon/${encodeURIComponent(mon.id)}/edit`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      out.textContent = "Saved.";
      if (onChanged) onChanged();
      setTimeout(() => {
        scrim.dataset.open = "false";
      }, 500);
    } catch (err) {
      renderErrorReport(out, err);
    }
  }

  async function createMon() {
    const species = Number(speciesInput.dataset.value || 0);
    if (!species) {
      out.textContent = "Pick a species first.";
      return;
    }
    const body = {
      generation: Number(generationSelect.value), species, level: Number(fields.level.value) || 5,
    };
    if (fields.nickname.value) body.nickname = fields.nickname.value;
    if (fields.ot.value) body.original_trainer_name = fields.ot.value;
    if (Number(fields.form.value)) body.form = Number(fields.form.value);
    if (fields.nature.value) body.nature = fields.nature.value;
    if (fields.gender.value) body.gender = fields.gender.value;
    if (fields.ability.value) body.ability = fields.ability.value;
    if (fields.heldItem.value) body.held_item = fields.heldItem.value;
    const ball = Number(fields.ball.dataset.value || 0);
    if (ball) body.ball = ball;
    if (fields.shiny.checked) body.shiny = true;
    Object.assign(body, statTotals(), moveList());

    out.textContent = "Creating…";
    try {
      const created = await api("/api/pokemon", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      // What happens to a newly-created Pokémon (deposit, download, send to a
      // save) used to be an automatic, silent PokeHome deposit, every option
      // is opt-in instead, offered here as a follow-up choice.
      mon = created;
      out.textContent = "";
      formBody.hidden = true;
      postcreate.hidden = false;
      postcreateOut.textContent = "";
      postcreateDeposit.checked = false;
      postcreateDownload.checked = false;
      postcreateInject.checked = false;
      postcreateName.textContent = created.nickname || created.species_name;
    } catch (err) {
      renderErrorReport(out, err);
    }
  }

  postcreateDone.addEventListener("click", async () => {
    const created = mon;
    try {
      if (postcreateDeposit.checked) {
        postcreateOut.textContent = "Depositing…";
        await api(`/api/pokehome/${encodeURIComponent(created.id)}/deposit`, { method: "POST" });
      }
      if (postcreateDownload.checked) {
        const link = document.createElement("a");
        link.href = `/api/pokemon/${encodeURIComponent(created.id)}/download` + tokenSuffix();
        link.download = "";
        document.body.append(link);
        link.click();
        link.remove();
      }
      if (onChanged) onChanged();
      if (postcreateInject.checked) {
        scrim.dataset.open = "false";
        withdraw.open(created, onChanged);
        return;
      }
      postcreateOut.textContent = "Done.";
      setTimeout(() => {
        scrim.dataset.open = "false";
      }, 500);
    } catch (err) {
      postcreateOut.textContent = err.message;
    }
  });

  function statDiffs() {
    const out2 = {};
    const ivs = {};
    let ivsChanged = false;
    for (const [key] of STATS) {
      const v = Number(ivInputs[key].value);
      if (v !== ((original.ivs && original.ivs[key]) || 0)) {
        ivs[key] = v;
        ivsChanged = true;
      }
    }
    if (ivsChanged) out2.ivs = ivs;
    const evs = {};
    let evsChanged = false;
    for (const [key] of STATS) {
      const v = Number(evInputs[key].value);
      if (v !== ((original.evs && original.evs[key]) || 0)) {
        evs[key] = v;
        evsChanged = true;
      }
    }
    if (evsChanged) out2.evs = evs;
    return out2;
  }

  function moveDiff() {
    const originalMoves = (original.moves || []).map((m) => m.id);
    const newMoves = moveInputs.map((i) => Number(i.dataset.value || 0));
    return newMoves.some((id, i) => id !== (originalMoves[i] || 0)) ? { moves: newMoves } : {};
  }

  function statTotals() {
    const out2 = {};
    const ivs = {};
    let any = false;
    for (const [key] of STATS) {
      const v = Number(ivInputs[key].value) || 0;
      if (v) {
        ivs[key] = v;
        any = true;
      }
    }
    if (any) out2.ivs = ivs;
    const evs = {};
    any = false;
    for (const [key] of STATS) {
      const v = Number(evInputs[key].value) || 0;
      if (v) {
        evs[key] = v;
        any = true;
      }
    }
    if (any) out2.evs = evs;
    return out2;
  }

  function moveList() {
    const moves = moveInputs.map((i) => Number(i.dataset.value || 0)).filter((id) => id);
    return moves.length ? { moves } : {};
  }

  return { open, openCreate };
}

// Bails out before wiring anything on a page that isn't PokeHome, most
// importantly before wireWithdrawFlow()/wireEditor() below, which attach to
// the shared editor/withdraw/slot modals (pokemon-editor-modals.html) that
// the standalone Editor page also includes. Without this guard, app.js
// calling both wirePokehome() and wireStandaloneEditor() unconditionally on
// every page meant /editor got two independent sets of listeners on the
// same modal buttons, a stray PokeHome-oriented click handler firing
// alongside the real one, throwing on its own null state.
export function wirePokehome() {
  if (!document.querySelector("#pokehome-box")) return;
  const withdraw = wireWithdrawFlow();
  const editor = wireEditor(withdraw);
  wirePokehomeBrowser(editor, withdraw);
}
