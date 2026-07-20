// The public patch workspace: pick a server recipe or upload your own patch,
// then watch the job through to a download. Ported from the old patch.js; the
// endpoints, CSRF header, and payloads are unchanged.
import { tokenSuffix } from "./util.js";

export async function wirePatchWorkspace() {
  const form = document.querySelector("#public-patch-form");
  if (!form) return;
  const recipes = document.querySelector("#patch-recipe");
  const out = document.querySelector("#patch-result");
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";

  // Picking a game already in the library patches a copy of it instead of
  // asking for a re-upload. A modal of cover-art cards (the same pattern the
  // 3DS Batch Installer uses) replaces the old bare text+<datalist> combo,
  // which rendered as the browser's own unstyled, unbounded dropdown with no
  // art to recognise a title by.
  const help = document.querySelector("#public-artifact-help");
  const openButton = document.querySelector("#public-artifact-open");
  const chosenLabel = document.querySelector("#public-artifact-chosen");
  const modalScrim = document.querySelector("[data-patch-artifact-modal]");
  const modalList = document.querySelector("#patch-artifact-list");
  const modalFilter = document.querySelector("#patch-artifact-filter");
  const games = await fetch("/api/games" + tokenSuffix())
    .then((r) => (r.ok ? r.json() : { games: [] }))
    .then((data) => data.games || [])
    .catch(() => []);
  const choices = [];
  for (const game of games) {
    for (const artifact of game.artifacts || []) {
      const label = `${(artifact.platform || game.platform || "").toUpperCase()} · ${game.title} · ${artifact.name}`;
      choices.push({
        label,
        artifact,
        title: game.title,
        cover: game.artwork_url ? `/media/${game.id}/cover` + tokenSuffix() : "",
      });
    }
  }

  function renderModalList(query) {
    const q = query.trim().toLowerCase();
    const matches = q ? choices.filter((c) => c.label.toLowerCase().includes(q)) : choices;
    modalList.replaceChildren();
    if (!matches.length) {
      const empty = document.createElement("p");
      empty.className = "col-span-full text-sm text-[var(--color-muted)]";
      empty.textContent = choices.length ? "No matches." : "Nothing in the library yet.";
      modalList.append(empty);
      return;
    }
    for (const choice of matches) {
      const card = document.createElement("button");
      card.type = "button";
      card.className = "pick";
      const art = document.createElement("span");
      art.className = "grid aspect-[3/4] w-full place-items-center bg-[var(--color-well)] p-1.5";
      const image = document.createElement("img");
      image.loading = "lazy";
      image.alt = "";
      image.className = "max-h-full max-w-full object-contain";
      if (choice.cover) {
        image.src = choice.cover;
        image.addEventListener("error", () => {
          image.src = "/assets/logo/brand.png";
          image.className += " opacity-40 p-3";
        });
      } else {
        image.src = "/assets/logo/brand.png";
        image.className += " opacity-40 p-3";
      }
      art.append(image);
      const caption = document.createElement("span");
      caption.className = "grid gap-0.5 p-2";
      const strong = document.createElement("strong");
      strong.className = "text-xs leading-snug";
      strong.textContent = choice.title;
      const small = document.createElement("span");
      small.className = "truncate text-[.65rem] text-[var(--color-faint)]";
      small.textContent = choice.artifact.name;
      caption.append(strong, small);
      card.append(art, caption);
      card.addEventListener("click", () => chooseArtifact(choice));
      modalList.append(card);
    }
  }

  function chooseArtifact(choice) {
    form.artifact_id.value = choice.artifact.id;
    form.artifact_label.value = choice.label;
    chosenLabel.textContent = choice.label;
    chosenLabel.classList.remove("text-[var(--color-muted)]");
    help.textContent = `Will patch a copy of ${choice.artifact.name}, your original stays untouched.`;
    delete modalScrim.dataset.open;
    syncROMExclusivity();
  }

  openButton.addEventListener("click", () => {
    modalFilter.value = "";
    renderModalList("");
    modalScrim.dataset.open = "true";
    modalFilter.focus();
  });
  modalFilter.addEventListener("input", () => renderModalList(modalFilter.value));

  // Step 1's two options are alternatives, not both-at-once: picking one
  // disables the other so a stale file/selection can't silently ride along
  // with the active choice on submit.
  const romInput = form.rom;
  function syncROMExclusivity() {
    openButton.disabled = romInput.files.length > 0;
    romInput.disabled = !!form.artifact_id.value;
  }
  romInput.addEventListener("change", syncROMExclusivity);

  const response = await fetch("/api/patch-files" + tokenSuffix());
  if (response.ok) {
    const { patch_recipes: available } = await response.json();
    for (const recipe of available || []) {
      const option = document.createElement("option");
      option.value = recipe.id;
      option.textContent = `${recipe.platform.toUpperCase()} · ${recipe.title} · ${recipe.extension.toUpperCase()}`;
      option.dataset.output = recipe.output_filename || "";
      recipes.append(option);
    }
  }

  // Step 2's pair works the same way: a chosen recipe disables the patch-file
  // upload and vice versa.
  const patchFileInput = form.patch;
  function syncPatchExclusivity() {
    recipes.disabled = patchFileInput.files.length > 0;
    patchFileInput.disabled = !!recipes.value;
  }
  recipes.addEventListener("change", () => {
    const chosen = recipes.selectedOptions[0];
    // A recipe usually knows what its output should be called; offer that name
    // rather than making the visitor invent one, but never overwrite their choice.
    if (chosen?.dataset.output && !form.output_filename.value) {
      form.output_filename.value = chosen.dataset.output;
    }
    syncPatchExclusivity();
  });
  patchFileInput.addEventListener("change", syncPatchExclusivity);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!form.artifact_id.value && !form.rom.files.length) {
      out.textContent = "Choose a ROM from the library or upload one.";
      return;
    }
    out.textContent = "Uploading and validating…";
    const job = await fetch("/api/patch-jobs" + tokenSuffix(), {
      method: "POST",
      headers: { "X-CSRF-Token": csrf },
      body: new FormData(form),
    });
    if (!job.ok) {
      out.textContent = await job.text();
      return;
    }
    poll((await job.json()).id);
  });

  // Patching runs server-side and can take a while on a large ROM, so the page
  // reports status until there is something to download.
  async function poll(id) {
    const response = await fetch(`/api/patch-jobs/${encodeURIComponent(id)}` + tokenSuffix());
    const job = await response.json();
    out.textContent = "Status: " + job.status + (job.error ? "\n" + job.error : "");

    if (job.status === "completed" && job.result_artifact_id) {
      const link = document.createElement("a");
      link.className = "text-[var(--color-accent)]";
      link.href = "/d/" + job.result_artifact_id + tokenSuffix();
      link.textContent = "Download patched ROM";
      out.append(document.createTextNode("\n"), link);
      return;
    }
    if (job.status === "queued" || job.status === "running") setTimeout(() => poll(id), 1000);
  }
}
