// The admin page: patch creation, metadata editing, retention, 3DS conversion,
// and remote search. Ported from the old adminJSScript; the endpoints, CSRF
// header, and payloads are unchanged.
//
// Every section lives on one page and every section needs the catalog, so the
// games are fetched once here and shared, rather than each form asking again.
import { element } from "./util.js";

// status writes a short outcome line into a result box. It is the one place the
// old script built markup with innerHTML; badges are real nodes now, so a
// filename or an error text from the server can never be read as HTML.
function status(box, text, kind = "") {
  box.replaceChildren(element("span", "badge badge--accent", kind || "status"), document.createTextNode(" " + text));
}

export async function wireAdmin() {
  const page = document.querySelector("[data-admin]");
  if (!page) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const headers = { "X-CSRF-Token": csrf };

  // The admin page is reachable by admin token alone, with no session, so its
  // catalog read uses the admin endpoint rather than the public one.
  const response = await fetch("/api/admin/games");
  const games = response.ok ? (await response.json()).games || [] : [];

  wirePatchForm(headers, games);
  wireRetain(headers);
  wirePrune(headers);
  wireMetadata(headers, games);
  wireConversion(headers, games);
  wireRemote(headers);
}

// wireShutdown gates the Admin hub's "Shut down ROM Relay" button behind a
// confirm() popup. It only ever cancels the submit, never intercepts it with
// fetch, so the plain form POST underneath still works if this bundle failed
// to load, just without the "are you sure" prompt.
export function wireShutdown() {
  const form = document.querySelector("[data-shutdown-form]");
  if (!form) return;
  form.addEventListener("submit", (event) => {
    if (!confirm("Shut down ROM Relay? This stops the server for everyone until it's started again.")) {
      event.preventDefault();
    }
  });
}

// wirePatchForm drives Create a patched ROM: pick a base ROM by label, pick or
// upload a patch, then watch the job to completion.
function wirePatchForm(headers, games) {
  const form = document.querySelector("#patch-form");
  if (!form) return;
  const out = document.querySelector("#result");
  const recipes = document.querySelector("#patch-recipe");
  const picker = document.querySelector("#artifact-picker");
  const help = document.querySelector("#artifact-help");
  const list = document.querySelector("#catalog-artifacts");

  // The picker shows a readable label but the server needs an artifact ID, so
  // labels are mapped back here. A typed label that matches nothing clears the
  // hidden ID, which is what stops a near-miss from patching the wrong ROM.
  const choices = [];
  for (const game of games) {
    for (const artifact of game.artifacts || []) {
      const label = `${game.platform.toUpperCase()} · ${game.title} · ${artifact.name}`;
      choices.push({ label, artifact });
      list.append(new Option(label));
    }
  }

  picker.addEventListener("input", () => {
    const hit = choices.find((choice) => choice.label === picker.value);
    form.artifact_id.value = hit?.artifact.id || "";
    help.textContent = hit
      ? `Selected ${hit.artifact.name} (${hit.artifact.extension.toUpperCase()})`
      : "Choose an exact suggestion from the list.";
  });

  fetch("/api/admin/patch-files")
    .then((r) => r.json())
    .then(({ patch_recipes: available }) => {
      for (const recipe of available || []) {
        const option = new Option(
          `${recipe.platform.toUpperCase()} · ${recipe.title} · ${recipe.id}`,
          recipe.id,
        );
        option.dataset.output = recipe.output_filename || "";
        option.dataset.title = recipe.title || "";
        recipes.append(option);
      }
    })
    .catch((error) => status(out, "Could not load recipes: " + error, "failed"));

  // A recipe knows what its output should be called and what it is; offer both
  // rather than making the operator retype them, but never overwrite a choice
  // already made.
  recipes.addEventListener("change", () => {
    const chosen = recipes.selectedOptions[0];
    if (chosen?.dataset.output && !form.output_filename.value) form.output_filename.value = chosen.dataset.output;
    if (chosen?.dataset.title && !form.title.value) form.title.value = chosen.dataset.title;
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!form.artifact_id.value) {
      status(out, "Choose an exact base-ROM suggestion first.", "failed");
      return;
    }
    status(out, "Uploading and validating…", "queued");
    const created = await fetch("/api/admin/patch-jobs", { method: "POST", headers, body: new FormData(form) });
    if (!created.ok) {
      status(out, await created.text(), "failed");
      return;
    }
    poll((await created.json()).id);
  });

  async function poll(id) {
    const response = await fetch("/api/admin/patch-jobs/" + encodeURIComponent(id));
    const job = await response.json();
    status(out, `Job ${job.id} · ${job.status}` + (job.error ? " · " + job.error : ""), job.status);
    if (job.status === "queued" || job.status === "running") setTimeout(() => poll(id), 800);
  }
}

// wireRetain removes the expiry from a job the public workspace produced.
function wireRetain(headers) {
  const form = document.querySelector("#retain-job");
  if (!form) return;
  const out = document.querySelector("#retain-result");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const id = form.job_id.value.trim();
    const response = await fetch("/api/admin/patch-jobs/" + encodeURIComponent(id) + "/retain", {
      method: "POST",
      headers,
    });
    out.textContent = response.ok ? "Permanent retention enabled for " + id : await response.text();
  });
}

// wirePrune removes stored Pokémon whose source save is gone, for every user.
function wirePrune(headers) {
  const form = document.querySelector("#dex-prune");
  if (!form) return;
  const out = document.querySelector("#dex-prune-result");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    out.textContent = "Pruning…";
    const response = await fetch("/api/admin/dex/prune", { method: "POST", headers });
    if (!response.ok) {
      out.textContent = await response.text();
      return;
    }
    const { removed } = await response.json();
    out.textContent = removed === 0 ? "Nothing to prune: every stored Pokémon still has its save." : `Removed ${removed} orphaned Pokémon.`;
  });
}

// wireMetadata drives the metadata editor and its preview.
function wireMetadata(headers, games) {
  const form = document.querySelector("#metadata-form");
  if (!form) return;
  const picker = form.game_id;
  const out = document.querySelector("#metadata-result");
  const preview = document.querySelector("#metadata-preview");
  const fields = ["title", "developer", "publisher", "description", "release_date", "genre", "region", "players", "rating"];

  for (const game of games) {
    picker.append(new Option(`${game.platform.toUpperCase()} · ${game.title}${game.rom_patch ? " · PATCHED" : ""}`, game.id));
  }

  function fill(game) {
    for (const name of fields) form.elements[name].value = game[name] || "";
    form.remove_cover.checked = false;
    preview.hidden = false;
    document.querySelector("#metadata-preview-title").textContent = game.title;
    document.querySelector("#metadata-preview-source").textContent =
      "Metadata source: " + (game.metadata_source || "local");

    const badges = [element("span", "badge", game.platform.toUpperCase())];
    if (game.rom_patch) badges.push(element("span", "badge badge--accent", "Patched"));
    if (game.encryption) badges.push(element("span", "badge badge--accent", game.encryption + " CIA"));
    document.querySelector("#metadata-preview-badges").replaceChildren(...badges);

    // The cover is the fastest way to tell two similarly named games apart, so
    // the preview swaps in the real art whenever the game has any.
    const art = preview.querySelector("[data-preview-art]");
    const next = game.artwork_url
      ? Object.assign(document.createElement("img"), {
          src: "/media/" + encodeURIComponent(game.id) + "/cover",
          alt: "Current cover for " + game.title,
          className: "aspect-[3/4] w-full rounded-[9px] object-cover",
        })
      : element("div", "grid aspect-[3/4] w-full place-items-center rounded-[9px] bg-[var(--color-surface-3)] text-xs text-[var(--color-faint)]", "No art");
    next.dataset.previewArt = "";
    art.replaceWith(next);
  }

  picker.addEventListener("change", () => {
    const game = games.find((candidate) => candidate.id === picker.value);
    if (game) fill(game);
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!picker.value) return;
    status(out, "Saving manual override…", "working");
    const body = new FormData(form);
    body.delete("game_id");
    const response = await fetch("/api/admin/games/" + encodeURIComponent(picker.value) + "/metadata", {
      method: "POST",
      headers,
      body,
    });
    if (!response.ok) {
      status(out, await response.text(), "failed");
      return;
    }
    // Refill from the saved record, not the submitted form: what came back is
    // what the catalog now holds, including anything the server normalised.
    const saved = await response.json();
    const index = games.findIndex((candidate) => candidate.id === saved.id);
    if (index >= 0) games[index] = saved;
    fill(saved);
    status(out, "Saved. Providers can no longer overwrite this entry.", "completed");
  });
}

// wireConversion queues a .3ds/.cci to CIA conversion. Progress is reported by
// the activity icon in the top nav, not here.
function wireConversion(headers, games) {
  const form = document.querySelector("#conversion-form");
  if (!form) return;
  const out = document.querySelector("#conversion-result");
  const picker = form.artifact_id;

  for (const game of games) {
    for (const artifact of game.artifacts || []) {
      if ([".3ds", ".cci"].includes((artifact.extension || "").toLowerCase())) {
        picker.append(new Option(`${game.title} · ${artifact.name}`, artifact.id));
      }
    }
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    status(out, "Starting conversion…", "working");
    const response = await fetch("/api/admin/convert/" + encodeURIComponent(picker.value), { method: "POST", headers });
    status(
      out,
      response.ok
        ? "Conversion queued. Progress appears in Active Jobs and the top-nav activity icon."
        : await response.text(),
      response.ok ? "queued" : "failed",
    );
  });
}

// wireRemote searches the configured SFTP source and fetches a chosen result.
function wireRemote(headers) {
  const search = document.querySelector("#remote-search-form");
  if (!search) return;
  const fetchForm = document.querySelector("#remote-fetch-form");
  const out = document.querySelector("#remote-result");

  search.addEventListener("submit", async (event) => {
    event.preventDefault();
    status(out, "Searching configured SFTP…", "working");
    const response = await fetch("/api/admin/remote-search?" + new URLSearchParams(new FormData(search)));
    out.textContent = await response.text();
  });

  fetchForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    status(out, "Downloading and validating…", "working");
    const response = await fetch("/api/admin/remote-jobs", { method: "POST", headers, body: new FormData(fetchForm) });
    out.textContent = await response.text();
  });
}
