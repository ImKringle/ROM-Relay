// ROM Library: browse the full catalog and delete a file. The list comes from
// the same /api/admin/games endpoint the Patch Silo's pickers already use.
import { element, formatSize } from "./util.js";

export async function wireRoms() {
  const page = document.querySelector("[data-admin-roms]");
  if (!page) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const list = document.querySelector("#roms-list");
  const filter = document.querySelector("#roms-filter");

  const response = await fetch("/api/admin/games");
  if (!response.ok) {
    list.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", await response.text()));
    return;
  }
  const { games } = await response.json();

  // One flat row per artifact, not per game: a delete acts on a file, and a
  // game can have more than one (e.g. an unencrypted and an encrypted CIA).
  const rows = [];
  for (const game of games || []) {
    for (const artifact of game.artifacts || []) {
      rows.push({ game, artifact });
    }
  }

  function matches(row, term) {
    if (!term) return true;
    const haystack = `${row.game.title} ${row.artifact.name} ${row.game.platform}`.toLowerCase();
    return haystack.includes(term);
  }

  function render() {
    const term = filter.value.trim().toLowerCase();
    const visible = rows.filter((row) => matches(row, term));
    if (visible.length === 0) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", "No files match."));
      return;
    }
    list.replaceChildren(...visible.map(rowNode));
  }

  function rowNode({ game, artifact }) {
    const card = element("div", "surface flex flex-wrap items-center gap-4 p-3.5");

    const meta = element("div", "min-w-0 flex-1");
    const name = element("div", "flex flex-wrap items-center gap-2");
    name.append(element("strong", "text-sm", game.title));
    name.append(element("span", "badge", game.platform.toUpperCase()));
    if (artifact.patched) name.append(element("span", "badge badge--accent", "Patched"));
    if (artifact.external) name.append(element("span", "badge badge--accent", "External"));
    meta.append(
      name,
      element("div", "mt-1 text-xs text-[var(--color-muted)]", `${artifact.name} · ${formatSize(artifact.size)}`),
    );

    const remove = element("button", "button button--danger", "Delete");
    remove.type = "button";
    remove.addEventListener("click", async () => {
      if (!confirm(`Delete ${artifact.name}? This removes the file from disk and cannot be undone.`)) return;
      const result = await fetch(`/api/admin/roms/${encodeURIComponent(artifact.id)}`, {
        method: "DELETE",
        headers: { "X-CSRF-Token": csrf },
      });
      if (!result.ok) {
        alert((await result.text()) || `That did not work (${result.status}).`);
        return;
      }
      rows.splice(rows.indexOf(rows.find((row) => row.artifact.id === artifact.id)), 1);
      render();
    });

    card.append(meta, remove);
    return card;
  }

  filter.addEventListener("input", render);
  render();
}
