// Standalone Editor page (Pokémon Tools > Editor): create a Pokémon from
// scratch, or pick a save and edit any of the Pokémon read out of it, then
// send the change straight back into a save, the same editor/withdraw
// modals PokeHome uses (see partials/pokemon-editor-modals.html), reused
// rather than rebuilt, since editing an owned Pokémon has never been
// PokeHome-specific (see editPokemonRequest's own comment on the Go side).
import { api, combobox, describeMon, genderBadge, isNicknamed, openMonDetail, shinyBadge, wireEditor, wireWithdrawFlow } from "./pokehome.js";
import { element, itemIconURL, noIconFallback, spriteURL } from "./util.js";
import { wireUploadPanel } from "./saves.js";

// wireBag wires the "Bag" button on the mon-picker step to a modal listing
// every pouch the sidecar reports for the current save, with an editable
// quantity per held item and an "add an item" row per pouch. getSaveID reads
// whichever save the standalone Editor currently has open, the same
// closure-over-a-let pattern showMonStep itself uses for currentSaveID.
function wireBag(getSaveID) {
  const openButton = document.querySelector("#tools-editor-bag-open");
  const scrim = document.querySelector("[data-bag-scrim]");
  const list = document.querySelector("#bag-pouches");
  const out = document.querySelector("#bag-out");
  const saveButton = document.querySelector("#bag-save");
  if (!openButton || !scrim) return;

  let pouches = [];
  let itemOptions = null;

  async function ensureItemOptions() {
    if (itemOptions) return itemOptions;
    const ref = await api("/api/reference");
    itemOptions = (ref.items || []).map((i) => ({ value: i.id, label: i.name, icon: itemIconURL(i.name) }));
    return itemOptions;
  }

  function renderPouch(pouch) {
    const section = element("div", "surface p-3");
    const head = element("div", "flex items-center justify-between gap-2");
    head.append(element("span", "eyebrow", pouch.type));
    head.append(element(
      "span", "text-xs text-[var(--color-faint)]",
      `${pouch.items.length}/${pouch.capacity} slots · max ${pouch.max_count} each`,
    ));
    section.append(head);

    const rows = element("div", "mt-2 grid gap-1.5");
    for (const item of pouch.items) {
      const row = element("div", "flex items-center gap-2");
      const icon = document.createElement("img");
      icon.className = "h-6 w-6 shrink-0 [image-rendering:pixelated]";
      icon.alt = "";
      icon.loading = "lazy";
      icon.src = itemIconURL(item.name || "");
      icon.onerror = () => noIconFallback(icon);
      row.append(icon);
      row.append(element("span", "flex-1 text-sm", item.name || `Item #${item.index}`));
      const qty = document.createElement("input");
      qty.type = "number";
      qty.className = "field w-24";
      qty.min = "0";
      qty.max = String(pouch.max_count);
      qty.value = String(item.count);
      qty.addEventListener("input", () => {
        item.count = Math.max(0, Math.min(pouch.max_count, Number(qty.value) || 0));
      });
      row.append(qty);
      const remove = element("button", "button button--small", "✕");
      remove.type = "button";
      remove.addEventListener("click", () => {
        pouch.items = pouch.items.filter((i) => i !== item);
        render();
      });
      row.append(remove);
      rows.append(row);
    }
    section.append(rows);

    if (pouch.items.length < pouch.capacity) {
      const addRow = element("div", "mt-2 flex items-center gap-2");
      const search = document.createElement("input");
      search.className = "field flex-1";
      search.type = "text";
      search.placeholder = "Add an item…";
      addRow.append(search);
      const qty = document.createElement("input");
      qty.type = "number";
      qty.className = "field w-20";
      qty.min = "1";
      qty.max = String(pouch.max_count);
      qty.value = "1";
      addRow.append(qty);
      const add = element("button", "button button--small", "Add");
      add.type = "button";
      add.addEventListener("click", async () => {
        const index = Number(search.dataset.value);
        if (!index || pouch.items.some((i) => i.index === index)) return;
        pouch.items.push({ index, name: search.value, count: Math.max(1, Math.min(pouch.max_count, Number(qty.value) || 1)) });
        render();
      });
      addRow.append(add);
      section.append(addRow);
      ensureItemOptions().then((options) => combobox(search, options));
    }

    return section;
  }

  function render() {
    list.replaceChildren(...pouches.map(renderPouch));
  }

  openButton.addEventListener("click", async () => {
    const saveID = getSaveID();
    if (!saveID) return;
    out.textContent = "";
    list.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", "Loading…"));
    scrim.dataset.open = "true";
    try {
      const data = await api(`/api/saves/${saveID}/bag`);
      pouches = data.pouches || [];
      render();
    } catch (err) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", err.message));
    }
  });

  saveButton?.addEventListener("click", async () => {
    const saveID = getSaveID();
    if (!saveID) return;
    out.textContent = "Saving…";
    try {
      const body = {
        pouches: pouches.map((p) => ({
          type: p.type,
          items: p.items.filter((i) => i.count > 0).map((i) => ({ index: i.index, count: i.count })),
        })),
      };
      const result = await api(`/api/saves/${saveID}/bag`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      pouches = result.pouches || pouches;
      render();
      out.textContent = "Saved as a new save version, see it in Save Vault.";
    } catch (err) {
      out.textContent = err.message;
    }
  });
}

export function wireStandaloneEditor() {
  const stepSave = document.querySelector("#tools-editor-step-save");
  if (!stepSave) return;

  const stepMons = document.querySelector("#tools-editor-step-mons");
  const saveList = document.querySelector("#tools-editor-save-list");
  const monGrid = document.querySelector("#tools-editor-mon-grid");
  const monEmpty = document.querySelector("#tools-editor-mon-empty");
  const saveLabel = document.querySelector("#tools-editor-save-label");
  const backButton = document.querySelector("#tools-editor-back");

  const withdraw = wireWithdrawFlow();
  const editor = wireEditor(withdraw);

  let currentSaveID = null;

  async function showSaveStep() {
    currentSaveID = null;
    stepMons.hidden = true;
    stepSave.hidden = false;
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
        row.addEventListener("click", () => showMonStep(save.id, save.game_label || save.file_name));
        saveList.append(row);
      }
    } catch (err) {
      saveList.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", err.message));
    }
  }

  async function showMonStep(saveID, label) {
    currentSaveID = saveID;
    stepSave.hidden = true;
    stepMons.hidden = false;
    saveLabel.textContent = label;
    monGrid.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-muted)]", "Loading…"));
    await refreshMons();
  }

  function monTile(mon) {
    const tile = element("button", "depositslot");
    tile.type = "button";
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
    tile.addEventListener("click", () => openMonDetail(mon, editor, withdraw, refreshMons));
    return tile;
  }

  function monSection(label, mons) {
    const section = element("div", "mb-4");
    if (label) section.append(element("h3", "eyebrow mb-2", label));
    const grid = element("div", "grid grid-cols-[repeat(auto-fill,minmax(96px,1fr))] gap-2");
    for (const mon of mons) grid.append(monTile(mon));
    section.append(grid);
    return section;
  }

  async function refreshMons() {
    if (!currentSaveID) return;
    let owned;
    try {
      owned = (await api("/api/pokemon")).pokemon || [];
    } catch (err) {
      monGrid.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", err.message));
      return;
    }
    const mons = owned.filter((m) => m.origin_save_id === currentSaveID);
    monEmpty.hidden = mons.length !== 0;

    // Grouped by where each Pokémon actually lives in the save, Party
    // (ordered 1-6), then each Box in number order, instead of one
    // undifferentiated grid. That flat grid was the QA report "the Editor is
    // only affecting a random Pokémon per save": nothing distinguished a
    // party member from a boxed one, or said which box.
    const party = [];
    const boxes = new Map(); // box number -> [{mon, order}]
    const other = [];
    for (const mon of mons) {
      const slot = mon.origin_slot || "";
      const partyMatch = slot.match(/^party:(\d+)$/);
      const boxMatch = slot.match(/^box:(\d+):(\d+)$/);
      if (partyMatch) {
        party.push({ mon, order: Number(partyMatch[1]) });
      } else if (boxMatch) {
        const box = Number(boxMatch[1]);
        if (!boxes.has(box)) boxes.set(box, []);
        boxes.get(box).push({ mon, order: Number(boxMatch[2]) });
      } else {
        other.push(mon); // no recorded slot, e.g. created from scratch
      }
    }
    party.sort((a, b) => a.order - b.order);
    for (const list of boxes.values()) list.sort((a, b) => a.order - b.order);

    monGrid.replaceChildren();
    if (party.length) monGrid.append(monSection("Party", party.map((p) => p.mon)));
    for (const box of [...boxes.keys()].sort((a, b) => a - b)) {
      monGrid.append(monSection(`Box ${box}`, boxes.get(box).map((p) => p.mon)));
    }
    if (other.length) monGrid.append(monSection(party.length || boxes.size ? "Other" : "", other));
  }

  wireBag(() => currentSaveID);

  backButton.addEventListener("click", showSaveStep);

  const createOpener = document.querySelector("[data-create-open]");
  if (createOpener) {
    // A Pokémon created here has no origin save, so it would never appear in
    // either step's list, refreshing is only meaningful once one exists,
    // which happens the moment editing a save's own Pokémon needs it.
    createOpener.addEventListener("click", () => editor.openCreate(refreshMons));
  }

  wireUploadPanel((result) => {
    if (!stepSave.hidden) showMonStep(result.save_id, result.game || result.file_name);
  });

  // A save deleted from Save Vault (or the admin Player Data page) in another
  // tab left this list stale until the person happened to click "‹ Back",
  // the QA report "deleted saves aren't cleared from the Editor". The list
  // step re-fetches fresh on every visit already; this just adds "the tab
  // came back into focus" as another trigger, without disturbing anyone
  // actually mid-edit on the mon-picker step.
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && !stepSave.hidden) showSaveStep();
  });

  showSaveStep();
}
