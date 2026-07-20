// Uploading saves, and the Save Data list.
import { describeUpload, element, formatSize, toastOnce, tokenSuffix } from "./util.js";

// postSave sends a save to the one upload endpoint and reports the outcome.
//
// gameID is optional and only a fallback: the server reads the save to find its
// game. A game page passes its own id for saves nothing can read.
async function postSave(gameID, file, out) {
  const body = new FormData();
  if (gameID) body.append("game_id", gameID);
  body.append("save_file", file);
  out.textContent = "Uploading…";

  const response = await fetch("/api/saves/upload" + tokenSuffix(), { method: "POST", body });
  const text = await response.text();
  if (!response.ok) {
    out.textContent = text || `Upload failed (${response.status})`;
    return null;
  }
  let result = {};
  try {
    result = JSON.parse(text);
  } catch {
    // The save is stored either way; only the summary is unreadable.
  }
  out.textContent = describeUpload(result);
  return result;
}

// describeChosenFiles is the label shown beside the upload button once one or
// more files are picked.
function describeChosenFiles(files) {
  if (!files.length) return "No file chosen";
  if (files.length === 1) return files[0].name;
  return `${files.length} files chosen`;
}

// uploaded holds what to run after a successful upload. The panel is one shared
// control but several modules care about it, the Dex refreshes its grid, Save
// Data reloads its list, and they each ask for it independently.
const uploaded = [];

// wireUploadPanel drives the upload control, wherever it appears. There is no
// game picker: the save says which game it is and the server reads it.
//
// Each panel element binds exactly once however many callers ask (binding per
// call would upload the same save several times), but the check is per element:
// the game modal swaps in a fresh panel after page load, and calling this again
// wires it.
export function wireUploadPanel(onUploaded) {
  if (onUploaded) uploaded.push(onUploaded);
  const panel = document.querySelector("[data-upload-panel]:not([data-upload-bound])");
  if (!panel) return;
  panel.dataset.uploadBound = "true";
  const file = panel.querySelector("[data-upload-file]");
  const name = panel.querySelector("[data-upload-name]");
  const out = panel.querySelector("[data-upload-out]");

  // The input is visually hidden behind a real button, so the chosen filename(s)
  // have to be echoed or nothing would confirm what was picked.
  file.addEventListener("change", () => {
    name.textContent = describeChosenFiles(file.files);
    out.textContent = "";
  });

  panel.querySelector("[data-upload-send]").addEventListener("click", async () => {
    if (!file.files.length) {
      out.textContent = "Choose a save file first.";
      return;
    }
    // Uploaded one request per file: the server's pipeline (parse, dedup, file
    // under a game, update the Dex) is written and tested around one save at a
    // time, and a partial failure this way still stores everything before it.
    const files = Array.from(file.files);
    let succeeded = 0;
    for (const [index, chosen] of files.entries()) {
      if (files.length > 1) out.textContent = `Uploading ${index + 1} of ${files.length}: ${chosen.name}…`;
      const result = await postSave(panel.dataset.uploadGameId, chosen, out);
      if (result) {
        succeeded++;
        // Read at click time, so a caller that registered after this bound still runs.
        for (const callback of uploaded) callback(result);
      }
    }
    if (files.length > 1) out.textContent = `Uploaded ${succeeded} of ${files.length} save${files.length > 1 ? "s" : ""}.`;
    if (succeeded) {
      file.value = "";
      name.textContent = "No file chosen";
    }
  });
}

// wireSavesList renders the Save Data page: every stored version, grouped by
// game, newest first.
export function wireSavesList() {
  const list = document.querySelector("#saves-list");
  if (!list) return;

  async function load() {
    toastOnce(
      "save-vault-intro",
      "Your uploaded saves, newest first. Every upload is kept as a new version, so a bad one never replaces your progress.",
    );
    const response = await fetch("/api/saves" + tokenSuffix());
    if (!response.ok) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", await response.text()));
      return;
    }
    const { saves } = await response.json();
    if (!saves || !saves.length) {
      // A full-page state, not a small dashed box: an empty vault is the
      // whole story for this page, not a footnote next to other saves.
      list.className = "grid";
      const empty = element("div", "col-span-full grid min-h-[40vh] place-items-center gap-2 p-10 text-center text-[var(--color-muted)]");
      empty.append(
        element("strong", "text-lg text-[var(--color-text)]", "No saves stored yet"),
        element("p", "m-0 max-w-sm text-sm", "Open a game in the Library and choose Upload save, or use the panel above."),
      );
      list.replaceChildren(empty);
      return;
    }
    list.className = "grid max-w-[1800px] items-start gap-4 grid-cols-[repeat(auto-fill,minmax(320px,1fr))]";

    const byGame = new Map();
    for (const save of saves) {
      const key = save.game_label || "Unknown game";
      if (!byGame.has(key)) byGame.set(key, []);
      byGame.get(key).push(save);
    }

    list.replaceChildren();
    for (const [game, versions] of byGame) {
      const card = element("div", "card p-4");
      // The newest version's game facts stand for the group: same game, same
      // platform and region.
      const info = versions[0].game_info || {};
      const head = element("div", "flex items-start gap-3.5");
      const heading = element("div", "min-w-0 flex-1");
      heading.append(element("h2", "m-0 text-base", game));
      const facts = element("div", "mt-1 flex flex-wrap gap-1.5");
      if (info.platform) facts.append(element("span", "badge", info.platform));
      if (info.region) facts.append(element("span", "badge", info.region + " region"));
      if (info.generation) facts.append(element("span", "badge", "Generation " + info.generation));
      heading.append(facts);
      heading.append(
        element(
          "p",
          "mb-0 mt-1 text-xs text-[var(--color-muted)]",
          `${versions.length} version${versions.length > 1 ? "s" : ""} stored`,
        ),
      );
      head.append(heading);
      card.append(head);
      versions.forEach((save, index) => card.append(versionRow(save, index === 0, load)));
      list.append(card);
    }
  }

  function versionRow(save, latest, reload) {
    const when = new Date(save.uploaded_at).toLocaleString();
    const row = element(
      "div",
      "mt-2 flex flex-wrap items-center gap-3 rounded-xl border p-2.5 " +
        (latest ? "border-[var(--color-accent)]" : "border-[var(--color-line)]"),
    );

    const meta = element("div", "min-w-0 flex-1");
    const name = element("div", "flex flex-wrap items-center gap-1.5 text-sm font-medium");
    name.append(save.file_name);
    if (latest) name.append(element("span", "badge badge--accent", "Latest"));
    if (save.parsed) {
      name.append(element("span", "badge", `${save.parsed_game || "Pokémon"} · ${save.pokemon_count || 0} Pokémon`));
    }
    meta.append(name, element("div", "text-xs text-[var(--color-muted)]", `${when} · ${formatSize(save.size_bytes)}`));

    // PKHeX cannot always tell a Gen 3 FireRed/LeafGreen (or Ruby/Sapphire)
    // save apart from its bytes alone, the cartridge version isn't stored in
    // SRAM, so its own guess sometimes lands on the wrong half of the pair.
    // Only offered when the detected game actually has that ambiguity
    // (game_info.alternates is empty otherwise).
    const alternates = save.game_info?.alternates;
    if (alternates?.length) {
      const correction = element("label", "mt-1 flex items-center gap-1.5 text-xs text-[var(--color-muted)]");
      correction.append("This is actually…");
      const select = document.createElement("select");
      select.className = "field w-auto py-1 text-xs";
      select.append(new Option("", "", true, true));
      for (const alt of alternates) select.append(new Option(alt.name, alt.code));
      select.addEventListener("change", async () => {
        if (!select.value) return;
        const res = await fetch(`/api/saves/${encodeURIComponent(save.id)}/game-override` + tokenSuffix(), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ game: select.value }),
        });
        if (res.ok) reload();
        else alert(await res.text());
      });
      correction.append(select);
      meta.append(correction);
    }

    const download = element("a", "button", "Download");
    download.href = `/saves/${encodeURIComponent(save.id)}/download` + tokenSuffix();

    const remove = element("button", "button button--danger", "Delete");
    remove.type = "button";
    remove.addEventListener("click", async () => {
      // A save may be the only copy of someone's progress, so deleting it asks
      // first and names what is going.
      if (!confirm(`Delete ${save.file_name} (${when})? This cannot be undone.`)) return;
      const deleted = await fetch(`/api/saves/${encodeURIComponent(save.id)}` + tokenSuffix(), { method: "DELETE" });
      if (deleted.ok) reload();
      else alert(await deleted.text());
    });

    row.append(meta, download, remove);
    return row;
  }

  wireUploadPanel(() => load());
  load();
}
