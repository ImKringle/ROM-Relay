// The ROM Randomizer tool: pick a library ROM or upload one, configure the
// full Universal Pokemon Randomizer ZX option set (rendered from
// randomizer-schema.js), submit, and poll the job through to a download.
// Gen 1-5 cartridge only in this version (see the randomizer sidecar).
import { tokenSuffix } from "./util.js";
import { OPTION_SCHEMA } from "./randomizer-schema.js";

function fieldInputName(key) {
  return "opt_" + key;
}

// buildOptionsForm renders every category as a collapsible section, and every
// field as its own labeled control. Dependent fields (the "when" property)
// start disabled/hidden until their governing field matches, kept in sync by
// a single change listener per governing field rather than one per dependent.
function buildOptionsForm(container) {
  const dependents = new Map(); // governing key -> [{el, cfg}]

  for (const category of OPTION_SCHEMA) {
    const details = document.createElement("details");
    details.className = "rounded-[15px] border border-[var(--color-line)] p-4";
    const summary = document.createElement("summary");
    summary.className = "cursor-pointer text-lg font-semibold";
    summary.textContent = category.title;
    details.append(summary);
    const grid = document.createElement("div");
    grid.className = "mt-3 grid gap-3 sm:grid-cols-2";
    details.append(grid);

    if (category.tweaks) {
      for (const tweak of category.tweaks) {
        grid.append(checkboxField(fieldInputName(tweak.key), tweak.label, false));
      }
    } else {
      for (const field of category.fields) {
        const wrap = renderField(field);
        grid.append(wrap);
        if (field.when) {
          const list = dependents.get(field.when.key) || [];
          list.push({ el: wrap, cfg: field.when });
          dependents.set(field.when.key, list);
        }
      }
    }
    container.append(details);
  }

  // Wire dependent-field visibility once every field exists.
  for (const [governingKey, entries] of dependents) {
    const governingInput = container.querySelector(`[name="${fieldInputName(governingKey)}"]`);
    if (!governingInput) continue;
    const sync = () => {
      const current = governingInput.type === "checkbox" ? governingInput.checked : governingInput.value;
      for (const { el, cfg } of entries) {
        const active = current === cfg.equals;
        el.classList.toggle("hidden", !active);
        el.querySelectorAll("input,select").forEach((i) => (i.disabled = !active));
      }
    };
    governingInput.addEventListener("change", sync);
    sync();
  }
}

function renderField(field) {
  const label = document.createElement("label");
  label.className = "grid gap-1.5";
  const span = document.createElement("span");
  span.className = "text-sm font-semibold";
  span.textContent = field.label;

  if (field.type === "bool") {
    const row = document.createElement("label");
    row.className = "flex items-center gap-2 text-sm";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.className = "size-4";
    input.name = fieldInputName(field.key);
    input.checked = !!field.default;
    row.append(input, document.createTextNode(field.label));
    return row;
  }
  if (field.type === "enum") {
    const select = document.createElement("select");
    select.className = "field";
    select.name = fieldInputName(field.key);
    for (const value of field.options) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = value.replaceAll("_", " ");
      if (value === field.default) option.selected = true;
      select.append(option);
    }
    label.append(span, select);
    return label;
  }
  // int
  const input = document.createElement("input");
  input.type = "number";
  input.className = "field";
  input.name = fieldInputName(field.key);
  input.value = String(field.default);
  if (field.min !== undefined) input.min = String(field.min);
  if (field.max !== undefined) input.max = String(field.max);
  label.append(span, input);
  return label;
}

function checkboxField(name, labelText) {
  const row = document.createElement("label");
  row.className = "flex items-center gap-2 text-sm";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.className = "size-4";
  input.name = name;
  row.append(input, document.createTextNode(labelText));
  return row;
}

// serializeSettings reads the rendered form back into the flat JSON object
// the sidecar expects (see Shim.java's buildSettings): custom starters and
// misc tweaks are collected into arrays, everything else maps straight
// through by key. Dependent fields are always included; the sidecar only
// reads them when their governing option applies.
function serializeSettings(container) {
  const out = {};
  const customStarters = [];
  const miscTweaks = [];
  for (const category of OPTION_SCHEMA) {
    if (category.tweaks) {
      for (const tweak of category.tweaks) {
        const input = container.querySelector(`[name="${fieldInputName(tweak.key)}"]`);
        if (input?.checked) miscTweaks.push(tweak.key);
      }
      continue;
    }
    for (const field of category.fields) {
      const input = container.querySelector(`[name="${fieldInputName(field.key)}"]`);
      if (!input) continue;
      if (field.key.startsWith("custom_starter_")) {
        customStarters[Number(field.key.slice(-1)) - 1] = Number(input.value);
        continue;
      }
      if (field.type === "bool") out[field.key] = input.checked;
      else if (field.type === "int") out[field.key] = Number(input.value);
      else out[field.key] = input.value;
    }
  }
  if (customStarters.length) out.custom_starters = customStarters;
  out.misc_tweaks = miscTweaks;
  return out;
}

export async function wireRandomizer() {
  const form = document.querySelector("#randomizer-form");
  if (!form) return;
  try {
    await wireRandomizerForm(form);
  } catch (e) {
    // A silent failure here means "Browse library" does nothing with zero
    // console output, the hardest kind of bug to report; surface it loudly.
    console.error("randomizer form failed to initialize:", e);
  }
}

// withExtension replaces (or appends) name's extension with ext, so the
// output filename always matches the source ROM's actual extension even if
// the user typed something else or copied the placeholder's example.
function withExtension(name, ext) {
  const trimmed = (name || "").trim();
  if (!trimmed || !ext) return trimmed;
  const dot = trimmed.lastIndexOf(".");
  const stem = dot > 0 ? trimmed.slice(0, dot) : trimmed;
  return stem + ext;
}

async function wireRandomizerForm(form) {
  const optionsRoot = document.querySelector("#randomizer-options");
  buildOptionsForm(optionsRoot);

  const out = document.querySelector("#randomizer-result");
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";

  // Library picker: same cover-art card modal the ROM Patcher and 3DS Batch
  // Installer use, single-select.
  const help = document.querySelector("#randomizer-artifact-help");
  const openButton = document.querySelector("#randomizer-artifact-open");
  const chosenLabel = document.querySelector("#randomizer-artifact-chosen");
  const modalScrim = document.querySelector("[data-randomizer-artifact-modal]");
  const modalList = document.querySelector("#randomizer-artifact-list");
  const modalFilter = document.querySelector("#randomizer-artifact-filter");
  // Randomizing only makes sense for cartridge platforms the sidecar
  // supports; the 3DS/other-console entries in the library would just fail.
  const SUPPORTED_PLATFORMS = new Set(["gb", "gbc", "gba", "nds"]);
  const games = await fetch("/api/games" + tokenSuffix())
    .then((r) => (r.ok ? r.json() : { games: [] }))
    .then((data) => data.games || [])
    .catch(() => []);
  const choices = [];
  for (const game of games) {
    for (const artifact of game.artifacts || []) {
      const platform = (artifact.platform || game.platform || "").toLowerCase();
      if (!SUPPORTED_PLATFORMS.has(platform)) continue;
      choices.push({
        label: `${platform.toUpperCase()} · ${game.title} · ${artifact.name}`,
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
      empty.textContent = choices.length ? "No matches." : "No Game Boy/Color/Advance or DS titles in the library yet.";
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
    chosenLabel.textContent = choice.label;
    chosenLabel.classList.remove("text-[var(--color-muted)]");
    help.textContent = `Will randomize a copy of ${choice.artifact.name}, your original stays untouched.`;
    delete modalScrim.dataset.open;
    syncROMExclusivity();
    if (!form.output_filename.value.trim()) {
      form.output_filename.value = withExtension(`${choice.title} (Randomized)`, choice.artifact.extension);
    }
    syncCapabilities();
  }

  openButton.addEventListener("click", () => {
    modalFilter.value = "";
    renderModalList("");
    modalScrim.dataset.open = "true";
    modalFilter.focus();
  });
  modalFilter.addEventListener("input", () => renderModalList(modalFilter.value));

  const romInput = form.rom;
  function syncROMExclusivity() {
    openButton.disabled = romInput.files.length > 0;
    romInput.disabled = !!form.artifact_id.value;
  }
  romInput.addEventListener("change", () => {
    syncROMExclusivity();
    const file = romInput.files[0];
    if (file && !form.output_filename.value.trim()) {
      const ext = "." + (file.name.split(".").pop() || "").toLowerCase();
      form.output_filename.value = withExtension(file.name.replace(/\.[^.]+$/, "") + " (Randomized)", ext);
    }
    syncCapabilities();
  });

  // currentSourceExtension resolves the actual extension of whichever source
  // (library pick or upload) is currently chosen, so the output filename can
  // always be corrected to match it regardless of what the user typed.
  function currentSourceExtension() {
    if (form.artifact_id.value) {
      const choice = choices.find((c) => c.artifact.id === form.artifact_id.value);
      return choice?.artifact.extension || "";
    }
    const file = romInput.files[0];
    return file ? "." + (file.name.split(".").pop() || "").toLowerCase() : "";
  }

  // syncCapabilities asks the sidecar which misc tweaks the chosen ROM's real
  // detected generation/handler actually supports (this varies by generation
  // and even by specific game revision, not just platform), and dims/disables
  // the ones that don't apply. Best-effort: any failure just leaves every
  // tweak enabled rather than blocking the tool.
  let capabilitiesToken = 0;
  async function syncCapabilities() {
    const token = ++capabilitiesToken;
    if (!form.artifact_id.value && !romInput.files.length) {
      applyMiscTweakAvailability(null);
      return;
    }
    const body = new FormData();
    if (form.artifact_id.value) body.set("artifact_id", form.artifact_id.value);
    else body.set("rom", romInput.files[0]);
    let caps = null;
    try {
      const response = await fetch("/api/randomize-capabilities" + tokenSuffix(), {
        method: "POST",
        headers: { "X-CSRF-Token": csrf },
        body,
      });
      if (response.ok) caps = await response.json();
    } catch {
      caps = null;
    }
    if (token === capabilitiesToken) applyMiscTweakAvailability(caps);
  }

  function applyMiscTweakAvailability(caps) {
    const allowed = caps?.misc_tweaks ? new Set(caps.misc_tweaks) : null;
    const misc = OPTION_SCHEMA.find((category) => category.id === "misc");
    for (const tweak of misc.tweaks) {
      const input = optionsRoot.querySelector(`[name="${fieldInputName(tweak.key)}"]`);
      if (!input) continue;
      const supported = !allowed || allowed.has(tweak.key);
      input.disabled = !supported;
      if (!supported) input.checked = false;
      const row = input.closest("label");
      row?.classList.toggle("opacity-40", !supported);
      row?.setAttribute("title", supported ? "" : "Not supported on this ROM.");
    }
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!form.artifact_id.value && !form.rom.files.length) {
      out.textContent = "Choose a ROM from the library or upload one.";
      return;
    }
    const ext = currentSourceExtension();
    if (ext) form.output_filename.value = withExtension(form.output_filename.value, ext);
    out.textContent = "Uploading and randomizing… this can take a while, especially on Gen 5.";
    const body = new FormData(form);
    body.set("settings", JSON.stringify(serializeSettings(optionsRoot)));
    const job = await fetch("/api/randomize-jobs" + tokenSuffix(), {
      method: "POST",
      headers: { "X-CSRF-Token": csrf },
      body,
    });
    if (!job.ok) {
      out.textContent = await job.text();
      return;
    }
    poll((await job.json()).id);
  });

  async function poll(id) {
    const response = await fetch(`/api/randomize-jobs/${encodeURIComponent(id)}` + tokenSuffix());
    const job = await response.json();
    out.textContent = "Status: " + job.status + (job.error ? "\n" + job.error : "");

    if (job.status === "completed" && job.result_artifact_id) {
      const link = document.createElement("a");
      link.className = "text-[var(--color-accent)]";
      link.href = "/d/" + job.result_artifact_id + tokenSuffix();
      link.textContent = "Download randomized ROM";
      out.append(document.createTextNode("\n"), link);
      return;
    }
    if (job.status === "queued" || job.status === "running") setTimeout(() => poll(id), 1500);
  }
}
