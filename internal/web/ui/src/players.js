// Player Data: pick a user, see their saves and Pokédex/Trainer data.
import { element, formatSize, spriteURL } from "./util.js";

export async function wirePlayers() {
  const page = document.querySelector("[data-admin-players]");
  if (!page) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const picker = document.querySelector("#player-picker");
  const dexSection = document.querySelector("#player-dex");
  const dexSummary = document.querySelector("#player-dex-summary");
  const clearTrainersButton = document.querySelector("#player-clear-trainers");
  const pokehomeSection = document.querySelector("#player-pokehome");
  const pokehomeList = document.querySelector("#player-pokehome-list");
  const savesSection = document.querySelector("#player-saves");
  const savesList = document.querySelector("#player-saves-list");
  const selectedTrainers = new Set();

  const response = await fetch("/api/admin/users");
  const { users } = response.ok ? await response.json() : { users: [] };
  for (const user of users || []) picker.append(new Option(user.username, user.id));

  async function send(url, { headers, ...options } = {}) {
    const result = await fetch(url, { ...options, headers: { "X-CSRF-Token": csrf, ...headers } });
    if (!result.ok) {
      alert((await result.text()) || `That did not work (${result.status}).`);
      return null;
    }
    return result;
  }

  function formatMinutes(minutes) {
    const hours = Math.floor(minutes / 60);
    return `${hours}h ${minutes % 60}m`;
  }

  function trainerRow(profile) {
    const id = `${profile.game}:${profile.tid}:${profile.sid}`;
    const row = element("label", "flex items-start gap-2.5 rounded-xl border border-[var(--color-line)] p-3");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "mt-0.5";
    checkbox.checked = selectedTrainers.has(id);
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) selectedTrainers.add(id);
      else selectedTrainers.delete(id);
      clearTrainersButton.disabled = selectedTrainers.size === 0;
    });
    const body = element("div", "min-w-0 flex-1");
    body.append(element("strong", "text-sm", `${profile.game} · ${profile.ot} (${profile.tid}/${profile.sid})`));
    const detail = [`Played ${formatMinutes(profile.played_minutes || 0)}`];
    if (profile.hof_clears !== null && profile.hof_clears !== undefined) {
      detail.push(`${profile.hof_clears} Hall of Fame clear${profile.hof_clears === 1 ? "" : "s"}`);
    }
    body.append(element("div", "mt-1 text-xs text-[var(--color-muted)]", detail.join(" · ")));
    row.append(checkbox, body);
    return row;
  }

  function pokehomeRow(userId, mon) {
    const row = element("div", "surface flex flex-wrap items-center gap-3 p-3");
    const sprite = document.createElement("img");
    sprite.className = "h-10 w-10 shrink-0 [image-rendering:pixelated]";
    sprite.alt = "";
    sprite.src = spriteURL(mon.species, mon.shiny);
    const meta = element("div", "min-w-0 flex-1");
    meta.append(element("strong", "text-sm", mon.nickname && mon.nickname !== mon.species_name ? `${mon.nickname} (${mon.species_name})` : mon.species_name));
    const detail = [`Lv. ${mon.level}`];
    if (mon.held_item) detail.push(`holding ${mon.held_item}`);
    if (mon.home_deposited_at) detail.push(`deposited ${new Date(mon.home_deposited_at).toLocaleDateString()}`);
    meta.append(element("div", "mt-1 text-xs text-[var(--color-muted)]", detail.join(" · ")));

    const remove = element("button", "button button--danger", "Delete");
    remove.type = "button";
    remove.addEventListener("click", async () => {
      if (!confirm(`Delete ${mon.species_name} from PokeHome? This cannot be undone.`)) return;
      if (await send(`/api/admin/users/${encodeURIComponent(userId)}/pokehome/${encodeURIComponent(mon.id)}`, { method: "DELETE" })) {
        load(userId);
      }
    });
    row.append(sprite, meta, remove);
    return row;
  }

  function saveRow(userId, save) {
    const row = element("div", "surface flex flex-wrap items-center gap-3 p-3");
    const meta = element("div", "min-w-0 flex-1");
    meta.append(element("strong", "text-sm", save.file_name));
    meta.append(
      element(
        "div",
        "mt-1 text-xs text-[var(--color-muted)]",
        `${formatSize(save.size_bytes)} · uploaded ${new Date(save.uploaded_at).toLocaleDateString()}` +
          (save.parsed ? ` · ${save.pokemon_count || 0} Pokémon` : ""),
      ),
    );

    const actions = element("div", "flex items-center gap-2");
    const download = element("a", "button", "Download");
    download.href = `/admin/users/${encodeURIComponent(userId)}/saves/${encodeURIComponent(save.id)}/download`;
    const remove = element("button", "button button--danger", "Delete");
    remove.type = "button";
    remove.addEventListener("click", async () => {
      if (!confirm(`Delete ${save.file_name}? This cannot be undone.`)) return;
      if (await send(`/api/admin/users/${encodeURIComponent(userId)}/saves/${encodeURIComponent(save.id)}`, { method: "DELETE" })) {
        load(userId);
      }
    });
    actions.append(download, remove);
    row.append(meta, actions);
    return row;
  }

  async function load(userId) {
    if (!userId) {
      dexSection.classList.add("hidden");
      pokehomeSection.classList.add("hidden");
      savesSection.classList.add("hidden");
      return;
    }
    dexSection.classList.remove("hidden");
    pokehomeSection.classList.remove("hidden");
    savesSection.classList.remove("hidden");
    selectedTrainers.clear();
    clearTrainersButton.disabled = true;

    const dexResponse = await fetch(`/api/admin/users/${encodeURIComponent(userId)}/dex`);
    if (dexResponse.ok) {
      const { trainers, pokemon } = await dexResponse.json();
      const summary = [element("p", "m-0 text-sm text-[var(--color-muted)]", `${pokemon} Pokémon stored.`)];
      summary.push(...(trainers || []).map(trainerRow));
      dexSummary.replaceChildren(...summary);
    }

    const pokehomeResponse = await fetch(`/api/admin/users/${encodeURIComponent(userId)}/pokehome`);
    if (pokehomeResponse.ok) {
      const { pokemon: deposited } = await pokehomeResponse.json();
      pokehomeList.replaceChildren(
        ...((deposited || []).length
          ? deposited.map((mon) => pokehomeRow(userId, mon))
          : [element("p", "text-sm text-[var(--color-muted)]", "Nothing deposited.")]),
      );
    }

    const savesResponse = await fetch(`/api/admin/users/${encodeURIComponent(userId)}/saves`);
    if (savesResponse.ok) {
      const { saves } = await savesResponse.json();
      savesList.replaceChildren(
        ...((saves || []).length
          ? saves.map((save) => saveRow(userId, save))
          : [element("p", "text-sm text-[var(--color-muted)]", "No saves stored.")]),
      );
    }
  }

  picker.addEventListener("change", () => load(picker.value));

  clearTrainersButton.addEventListener("click", async () => {
    const userId = picker.value;
    if (!userId || selectedTrainers.size === 0) return;
    const username = picker.selectedOptions[0]?.textContent || "this user";
    const n = selectedTrainers.size;
    if (!confirm(`Clear dex data for ${n} selected playthrough${n === 1 ? "" : "s"} of ${username}? Their Hall of Fame and dex progress are removed. Saves and stored Pokémon are kept.`)) return;
    if (
      await send(`/api/admin/users/${encodeURIComponent(userId)}/clear-trainers`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ trainer_ids: [...selectedTrainers] }),
      })
    ) {
      load(userId);
    }
  });
}
