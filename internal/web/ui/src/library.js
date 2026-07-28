// Catalog and game-page behaviour: background work reporting, the game modal,
// and the FBI batch installer.
import { element, formatSize, tokenSuffix } from "./util.js";
import { wireUploadPanel } from "./saves.js";

// wireBackgroundWork drives the top-nav activity icon, reporting long-running
// server jobs (library hashing, metadata, 3DS conversion, downloads) no
// matter which page you're on, it used to be a per-page panel that only
// existed on the catalog and game pages, invisible everywhere else.
export function wireBackgroundWork() {
  const widget = document.querySelector("#activity-widget");
  const list = document.querySelector("#activity-list");
  const empty = document.querySelector("#activity-empty");
  if (!widget || !list) return;

  const render = (jobs) => {
    const rows = Object.entries(jobs).filter(([, job]) => job.active || job.error || job.recent);
    list.querySelectorAll(".topnav-activity-row").forEach((row) => row.remove());
    for (const [name, job] of rows) {
      const row = document.createElement("div");
      row.className = "topnav-activity-row";

      const head = document.createElement("div");
      head.className = "topnav-activity-row-head";
      const label = document.createElement("span");
      label.textContent = name.replaceAll("_", " ");
      head.append(label);
      if (job.total) {
        const percent = document.createElement("span");
        percent.textContent = Math.round((job.done * 100) / job.total) + "%";
        head.append(percent);
      }
      row.append(head);

      const msg = document.createElement("p");
      msg.className = "topnav-activity-row-msg";
      msg.textContent = job.message || "";
      row.append(msg);
      if (job.error) {
        const error = document.createElement("p");
        error.className = "topnav-activity-row-msg text-[var(--color-danger)]";
        error.textContent = job.error;
        row.append(error);
      }

      if (job.total) {
        const bar = document.createElement("div");
        bar.className = "kernelbar";
        const fill = document.createElement("i");
        fill.style.width = Math.round((job.done * 100) / job.total) + "%";
        bar.append(fill);
        row.append(bar);
      }
      list.append(row);
    }
    empty.hidden = rows.length > 0;
    widget.dataset.active = String(rows.some(([, job]) => job.active));
  };

  const poll = async () => {
    try {
      const response = await fetch("/healthz" + tokenSuffix());
      const health = await response.json();
      render(health.background_jobs || {});
    } catch {
      // A failed poll is not worth reporting: the next one is a second away.
    }
    setTimeout(poll, 1000);
  };
  poll();
}

// --- Modals ----------------------------------------------------------------
// One convention: a .modal-scrim opens with data-open="true"; its close button,
// a click on the scrim itself, and Escape all close it.

function openModal(scrim) {
  scrim.dataset.open = "true";
}
function closeModal(scrim) {
  delete scrim.dataset.open;
}

export function wireModals() {
  for (const scrim of document.querySelectorAll(".modal-scrim")) {
    scrim.addEventListener("click", (event) => {
      if (event.target === scrim) closeModal(scrim);
    });
    for (const close of scrim.querySelectorAll("[data-modal-close]")) {
      close.addEventListener("click", () => closeModal(scrim));
    }
  }
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    for (const scrim of document.querySelectorAll('.modal-scrim[data-open="true"]')) closeModal(scrim);
  });
}

// wireGameModal opens the game details over the library once htmx has swapped
// /games/{id}'s #game-view into the modal body. The card anchors stay real
// links, so direct visits and JS-off browsing land on the full page.
export function wireGameModal() {
  const scrim = document.querySelector("[data-game-modal]");
  if (!scrim) return;
  const body = scrim.querySelector("#game-modal-body");

  document.body.addEventListener("htmx:afterSwap", (event) => {
    if (event.target !== body) return;
    openModal(scrim);
    scrim.querySelector(".modal").scrollTop = 0;
    // The swapped-in view carries its own upload panel; wire the fresh element.
    wireUploadPanel();
  });
}

// --- Convert / Encrypt-Decrypt upload tools --------------------------------
// Both tools share one shape: upload, poll a job, then render a download
// link + QR the same way a game page's own "Direct link & QR" does. Crypt
// additionally handles the one case the server cannot resolve on its own, a
// file with a genuine mix of encrypted and decrypted content, by asking for
// a direction and resubmitting the same File object, never a second upload
// dialog.

function wireUploadOpen(buttonSelector, modalId) {
  const button = document.querySelector(buttonSelector);
  const scrim = document.querySelector(modalId);
  if (!button || !scrim) return;
  button.addEventListener("click", () => openModal(scrim));
}

// waitForUploadJob polls until a convert/crypt job leaves "running" and
// returns its final state (status/error/artifact_id/...) - the part
// pollUploadJob and the FBI batch's convert-then-build flow both need,
// without either caring how the other renders the result.
async function waitForUploadJob(kind, id) {
  for (;;) {
    const response = await fetch(`/api/upload/${kind}/${id}` + tokenSuffix());
    const job = await response.json();
    if (job.status !== "running") return job;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
}

// pollUploadJob polls until the job leaves "running", then renders either the
// error or a download link + QR image into out.
async function pollUploadJob(kind, id, out) {
  const job = await waitForUploadJob(kind, id);
  out.replaceChildren();
  if (job.status !== "completed") {
    out.append(element("p", "text-[var(--color-danger)]", job.error || "the job failed"));
    return;
  }
  if (job.direction) {
    out.append(element("p", "text-[var(--color-muted)]", `${job.direction === "decrypt" ? "Decrypted" : "Encrypted"}: ${job.filename}`));
  }
  const link = document.createElement("a");
  link.className = "button button--primary";
  link.href = job.download_url;
  link.textContent = "Download " + job.filename;
  out.append(link);
  const qr = document.createElement("img");
  qr.className = "mt-2 w-40 rounded-lg bg-white p-2";
  qr.alt = "Direct-download QR code";
  qr.loading = "lazy";
  qr.src = job.qr_url;
  out.append(qr);
}

// libraryArtifacts lists existing catalog artifacts matching one of extExts
// (and, if given, one platform), for the Convert/Crypt "From Library"
// picker, so those tools can run against a ROM already on the Relay without
// downloading and re-uploading it.
async function libraryArtifacts(extExts, platform) {
  const response = await fetch("/api/games" + tokenSuffix());
  if (!response.ok) return [];
  const { games } = await response.json();
  const out = [];
  for (const game of games || []) {
    for (const artifact of game.artifacts || []) {
      const ext = (artifact.extension || "").toLowerCase();
      if (!extExts.includes(ext)) continue;
      if (platform && (artifact.platform || game.platform) !== platform) continue;
      out.push({ id: artifact.id, label: `${game.title} · ${artifact.name}` });
    }
  }
  return out;
}

function fillLibraryOptions(select, items) {
  select.replaceChildren(
    ...items.map((item) => {
      const opt = document.createElement("option");
      opt.value = item.id;
      opt.textContent = item.label;
      return opt;
    }),
  );
}

// wireSourceToggle switches a Convert/Crypt form between an uploaded file and
// an existing Library artifact, lazily loading the artifact picker options
// the first time "From Library" is selected. platformSource, when given, is
// the crypt form's 3DS/NDS/All chip row (mixing every platform in one
// dropdown otherwise): its active chip narrows which platform the picker
// lists, and switching it reloads the list.
function wireSourceToggle(root, form, extExts, platformSource) {
  const buttons = root.querySelectorAll("[data-source-pick]");
  const upload = form.querySelector("[data-source-upload]");
  const library = form.querySelector("[data-source-library]");
  const currentPlatform = () => platformSource?.querySelector(".chip--active")?.dataset.cryptPlatform || "";
  const reload = async () => fillLibraryOptions(library, await libraryArtifacts(extExts, currentPlatform()));
  let loaded = false;
  for (const button of buttons) {
    button.addEventListener("click", async () => {
      buttons.forEach((b) => b.classList.toggle("chip--active", b === button));
      const isLibrary = button.dataset.sourcePick === "library";
      upload.hidden = isLibrary;
      upload.required = !isLibrary;
      upload.disabled = isLibrary;
      library.hidden = !isLibrary;
      library.required = isLibrary;
      library.disabled = !isLibrary;
      if (platformSource) platformSource.hidden = !isLibrary;
      if (isLibrary && !loaded) {
        loaded = true;
        await reload();
      }
    });
  }
  if (platformSource) {
    for (const chip of platformSource.querySelectorAll("[data-crypt-platform]")) {
      chip.addEventListener("click", async () => {
        platformSource.querySelectorAll("[data-crypt-platform]").forEach((c) => c.classList.toggle("chip--active", c === chip));
        await reload();
      });
    }
  }
}

export function wireUploadTools() {
  wireUploadOpen("[data-convert-open]", "#convert-modal");
  wireUploadOpen("[data-crypt-open]", "#crypt-modal");

  const convertForm = document.querySelector("[data-convert-form]");
  const convertSource = document.querySelector("[data-convert-source]");
  if (convertForm && convertSource) {
    wireSourceToggle(convertSource, convertForm, [".3ds", ".cci"]);
    convertForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const out = document.querySelector("[data-convert-out]");
      out.replaceChildren(element("p", "text-[var(--color-muted)]", "Converting…"));
      const library = convertForm.querySelector("[data-source-library]");
      let response;
      if (!library.hidden) {
        const params = new URLSearchParams();
        if (convertForm.retain.checked) params.set("retain", "true");
        response = await fetch(`/api/library/convert/${encodeURIComponent(library.value)}` + tokenSuffix(), { method: "POST", body: params });
      } else {
        response = await fetch("/api/upload/convert" + tokenSuffix(), { method: "POST", body: new FormData(convertForm) });
      }
      if (!response.ok) {
        out.replaceChildren(element("p", "text-[var(--color-danger)]", await response.text()));
        return;
      }
      const { id } = await response.json();
      await pollUploadJob("convert", id, out);
    });
  }

  const cryptForm = document.querySelector("[data-crypt-form]");
  const cryptSource = document.querySelector("[data-crypt-source]");
  if (cryptForm && cryptSource) {
    const cryptPlatformSource = cryptForm.querySelector("[data-crypt-platform-source]");
    wireSourceToggle(cryptSource, cryptForm, [".nds", ".srl", ".dsi", ".ids", ".3ds", ".cci", ".cia"], cryptPlatformSource);
    const directionBox = document.querySelector("[data-crypt-direction]");
    const out = document.querySelector("[data-crypt-out]");
    const library = cryptForm.querySelector("[data-source-library]");
    const submit = async (direction) => {
      directionBox.hidden = true;
      out.replaceChildren(element("p", "text-[var(--color-muted)]", "Working…"));
      let response;
      if (!library.hidden) {
        const params = new URLSearchParams();
        if (cryptForm.retain.checked) params.set("retain", "true");
        if (direction) params.set("direction", direction);
        response = await fetch(`/api/library/crypt/${encodeURIComponent(library.value)}` + tokenSuffix(), { method: "POST", body: params });
      } else {
        const body = new FormData(cryptForm);
        if (direction) body.set("direction", direction);
        response = await fetch("/api/upload/crypt" + tokenSuffix(), { method: "POST", body });
      }
      if (response.status === 409) {
        const conflict = await response.json();
        out.replaceChildren(element("p", "text-[var(--color-muted)]", conflict.message));
        directionBox.hidden = false;
        return;
      }
      if (!response.ok) {
        out.replaceChildren(element("p", "text-[var(--color-danger)]", await response.text()));
        return;
      }
      const { id } = await response.json();
      await pollUploadJob("crypt", id, out);
    };
    cryptForm.addEventListener("submit", (event) => {
      event.preventDefault();
      submit(null);
    });
    for (const pick of document.querySelectorAll("[data-crypt-direction-pick]")) {
      pick.addEventListener("click", () => submit(pick.dataset.cryptDirectionPick));
    }
  }
}

// wireLibraryUpload adds a ROM straight into the Library from the browser.
// Unlike Convert/Crypt, this isn't a polled background job: the server
// reconciles and hashes synchronously, so the response is the final result.
export function wireLibraryUpload() {
  wireUploadOpen("[data-upload-rom-open]", "#upload-rom-modal");
  const form = document.querySelector("[data-upload-rom-form]");
  if (!form) return;
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const out = document.querySelector("[data-upload-rom-out]");
    out.replaceChildren(element("p", "text-[var(--color-muted)]", "Uploading…"));
    const response = await fetch("/api/library/upload" + tokenSuffix(), { method: "POST", body: new FormData(form) });
    if (!response.ok) {
      out.replaceChildren(element("p", "text-[var(--color-danger)]", await response.text()));
      return;
    }
    const result = await response.json();
    out.replaceChildren(element("p", "text-[var(--color-muted)]", `Added ${result.name}. Reloading the library...`));
    setTimeout(() => location.reload(), 900);
  });
}

// --- 3DS Batch Installer -----------------------------------------------
// Select ROMs by their covers, filtered by platform (defaults to 3DS-only),
// and either show an ordered QR batch for FBI to scan, or push the
// selection to FBI's port 5000. Only encrypted 3DS CIAs are FBI-installable,
// other platforms are still selectable for reference, and those actions
// silently drop them, so a note is appended when that happens.
export async function wireFBIBatch() {
  const host = document.querySelector("[data-fbi]");
  if (!host) return;

  const response = await fetch("/api/games" + tokenSuffix());
  if (!response.ok) return;
  const { games } = await response.json();

  // One entry per artifact, carrying its game's name, cover, and platform:
  // the picker shows what you recognise (title + box art), not file names.
  // Release tags, "(USA)", "(Rev 2)", are stripped like the library cards do.
  const cleanTitle = (title) => title.replace(/[([][^)\]]*[)\]]/g, "").replace(/\s+/g, " ").trim() || title;
  const roms = [];
  for (const game of games || []) {
    for (const artifact of game.artifacts || []) {
      roms.push({
        id: artifact.id,
        title: cleanTitle(game.title),
        name: artifact.name,
        platform: artifact.platform || game.platform,
        extension: (artifact.extension || "").toLowerCase(),
        encryption: artifact.encryption || "",
        cia: (artifact.extension || "").toLowerCase() === ".cia" && artifact.encryption !== "unencrypted",
        cover: game.artwork_url ? `/media/${game.id}/cover` + tokenSuffix() : "",
      });
    }
  }
  if (!roms.length) return;
  const romsById = new Map(roms.map((rom) => [rom.id, rom]));

  const opener = document.querySelector("[data-fbi-open]");
  const openerBar = document.querySelector("[data-fbi-bar]");
  if (opener) {
    if (openerBar) openerBar.hidden = false;
    opener.addEventListener("click", () => openModal(host));
  }

  const list = host.querySelector("[data-fbi-list]");
  const output = host.querySelector("[data-fbi-output]");
  const counts = [...host.querySelectorAll("[data-fbi-count]")];
  const qrButtons = [...host.querySelectorAll("[data-fbi-qr]")];
  const allButtons = [...host.querySelectorAll("[data-fbi-all]")];

  for (const rom of roms) {
    const pick = element("label", "pick");
    pick.dataset.platform = rom.platform;

    const check = document.createElement("input");
    check.type = "checkbox";
    check.value = rom.id;
    check.className = "sr-only";

    const art = element("span", "grid aspect-[3/4] w-full place-items-center bg-[var(--color-well)] p-1.5");
    if (rom.cover) {
      const image = document.createElement("img");
      image.loading = "lazy";
      image.src = rom.cover;
      image.alt = "";
      image.className = "max-h-full max-w-full object-contain transition-opacity";
      image.addEventListener("error", () => image.remove());
      art.append(image);
    } else {
      const fallback = document.createElement("img");
      fallback.src = "/assets/logo/brand.png";
      fallback.alt = "";
      fallback.className = "max-h-10 max-w-10 object-contain opacity-40";
      art.append(fallback);
    }

    const caption = element("span", "grid gap-0.5 p-2");
    caption.append(element("strong", "text-xs leading-snug", rom.title));
    caption.append(element("span", "truncate text-[.65rem] text-[var(--color-faint)]", rom.name));

    // Same title, same box art: with several releases of one game the cards are
    // otherwise indistinguishable (e.g. an encrypted CIA, a decrypted CIA, and a
    // raw cart dump of the same Alpha Sapphire). Stamp format + encryption state
    // on the art itself so the one FBI can actually install stands out.
    const tags = element("span", "pick-tags");
    tags.append(element("span", "pick-tag", rom.extension.replace(/^\./, "").toUpperCase() || rom.platform.toUpperCase()));
    if (rom.extension === ".cia") {
      const ok = rom.encryption !== "unencrypted";
      const lock = element("span", `pick-tag pick-tag--${ok ? "ok" : "warn"}`, ok ? "🔒 Encrypted" : "🔓 Decrypted");
      lock.title = ok ? "Encrypted CIA, installable via FBI" : "Decrypted CIA, not installable via FBI";
      tags.append(lock);
    } else if (rom.platform === "3ds") {
      const warn = element("span", "pick-tag pick-tag--warn", "Not a CIA");
      warn.title = "Not an installable CIA, FBI can't install this file";
      tags.append(warn);
    }

    pick.append(check, art, tags, element("span", "pick-mark", "✓"), caption);
    pick.title = rom.name;
    list.append(pick);
  }

  // Platform filter: chips default to 3DS-only (the only platform FBI can
  // actually install), toggled on to widen what's shown. Hiding a card never
  // clears its checkbox, so a selection survives switching filters.
  const platformChips = [...host.querySelectorAll("[data-fbi-platform]")];
  const applyPlatformFilter = () => {
    const active = new Set(platformChips.filter((chip) => chip.classList.contains("chip--active")).map((chip) => chip.dataset.fbiPlatform));
    for (const pick of list.querySelectorAll(".pick")) pick.hidden = active.size > 0 && !active.has(pick.dataset.platform);
  };
  platformChips.forEach((chip) => {
    chip.addEventListener("click", () => {
      chip.classList.toggle("chip--active");
      applyPlatformFilter();
    });
  });
  applyPlatformFilter();

  const boxes = () => [...list.querySelectorAll("input")];
  const selected = () => boxes().filter((box) => box.checked).map((box) => box.value);
  const updateCount = () => {
    const n = selected().length;
    counts.forEach((el) => (el.textContent = n ? `${n} selected` : ""));
    qrButtons.forEach((btn) => (btn.hidden = n === 0));
  };
  list.addEventListener("change", updateCount);

  allButtons.forEach((btn) =>
    btn.addEventListener("click", () => {
      const visible = boxes().filter((box) => !box.closest(".pick").hidden);
      const allChecked = visible.every((box) => box.checked);
      visible.forEach((box) => (box.checked = !allChecked));
      updateCount();
    }),
  );

  // Non-CIA picks (other platforms) never make it into the FBI-compatible URL
  // list the server builds; say so instead of the batch silently shrinking.
  const skippedNote = (idsRequested, filesIncluded) => {
    const skipped = idsRequested - filesIncluded;
    return skipped > 0 ? `${skipped} of ${idsRequested} selected file${idsRequested === 1 ? "" : "s"} aren't FBI-installable CIAs and were left out.` : "";
  };

  // convertibleExtras finds selected files that aren't FBI-installable CIAs
  // yet but could be made into one with what's already built (item 1's
  // from-library Convert/Crypt): a raw .3ds/.cci cart dump, or an unencrypted
  // CIA. Anything else (another platform entirely) has no path to a CIA at
  // all and stays excluded regardless.
  const convertibleExtras = (ids) =>
    ids.filter((id) => {
      const rom = romsById.get(id);
      if (!rom || rom.cia) return false;
      return rom.extension === ".3ds" || rom.extension === ".cci" || (rom.extension === ".cia" && rom.encryption === "unencrypted");
    });

  // convertSelected runs each convertible file through Convert or Crypt
  // (encrypt direction), same as the Library page's own tools, and returns
  // the resulting CIA artifact IDs so the batch build can use them in place
  // of the originals. Throws on the first failure - a mixed batch quietly
  // sending only some of what was asked for is worse than stopping.
  async function convertSelected(ids) {
    const results = [];
    for (const id of ids) {
      const rom = romsById.get(id);
      const isConvert = rom.extension === ".3ds" || rom.extension === ".cci";
      const path = isConvert ? "convert" : "crypt";
      const body = isConvert ? new URLSearchParams() : new URLSearchParams({ direction: "encrypt" });
      const start = await fetch(`/api/library/${path}/${encodeURIComponent(id)}` + tokenSuffix(), { method: "POST", body });
      if (!start.ok) throw new Error(`${rom.name}: ${await start.text()}`);
      const { id: jobId } = await start.json();
      const job = await waitForUploadJob(path, jobId);
      if (job.status !== "completed") throw new Error(`${rom.name}: ${job.error || "conversion failed"}`);
      results.push(job.artifact_id);
    }
    return results;
  }

  qrButtons.forEach((btn) =>
    btn.addEventListener("click", async () => {
      let ids = selected();
      if (!ids.length) {
        output.textContent = "Select at least one ROM.";
        return;
      }

      const extras = convertibleExtras(ids);
      if (extras.length > 0) {
        const prompt = `${extras.length} of ${ids.length} selected file${ids.length === 1 ? "" : "s"} aren't FBI-installable CIAs and would be left out. Convert/decrypt them now, then build the QR?`;
        if (confirm(prompt)) {
          output.textContent = `Converting ${extras.length} file${extras.length === 1 ? "" : "s"}…`;
          try {
            const converted = await convertSelected(extras);
            ids = ids.filter((id) => !extras.includes(id)).concat(converted);
          } catch (err) {
            output.textContent = "Conversion failed: " + err.message;
            return;
          }
        }
      }

      output.textContent = "Building QR batch…";
      const token = new URLSearchParams(location.search).get("token");
      const amp = token ? "&token=" + encodeURIComponent(token) : "";
      const batch = await fetch("/api/fbi/batches?ids=" + encodeURIComponent(ids.join(",")) + amp);
      if (!batch.ok) {
        output.textContent = await batch.text();
        return;
      }
      const { qr_codes: codes, count: total, files } = await batch.json();
      output.replaceChildren();

      // Each code installs several ROMs at once (FBI reads a QR as a list of
      // URLs), so say so: "Scan 1 of 5" alone reads like one ROM per scan.
      const summary = element("p", "mb-2 w-full text-sm text-[var(--color-muted)]");
      const note = skippedNote(ids.length, files);
      summary.textContent =
        (files === 0
          ? "None of the selected files are FBI-installable encrypted CIAs, so there's no QR to scan."
          : total === 1
            ? `One QR code installs all ${files} selected CIA${files === 1 ? "" : "s"}. Scan it in FBI › Remote Install › Scan QR Code.`
            : `${files} CIAs across ${total} QR codes. Each code installs several at once. Scan them in order in FBI › Remote Install › Scan QR Code.`) +
        (note ? ` ${note}` : "");
      output.append(summary);

      codes.forEach((source, index) => {
        const figure = document.createElement("figure");
        const image = document.createElement("img");
        const caption = document.createElement("figcaption");
        image.src = source;
        image.alt = `FBI batch QR ${index + 1} of ${total}`;
        image.className = "w-52 rounded-lg bg-white p-2";
        caption.className = "text-center text-xs text-[var(--color-muted)]";
        caption.textContent = total === 1 ? `${files} CIAs` : `Scan ${index + 1} of ${total}`;
        figure.append(image, caption);
        output.append(figure);
      });
      // The picker grid + action bars above can push this well past the
      // modal's scroll viewport, so the QR renders "successfully" off-screen.
      output.scrollIntoView({ behavior: "smooth", block: "start" });
    }),
  );

  host.querySelector("[data-fbi-send]").addEventListener("submit", async (event) => {
    event.preventDefault();
    const ids = selected();
    if (!ids.length) {
      output.textContent = "Select at least one ROM.";
      return;
    }
    const body = new URLSearchParams({ target: event.target.target.value, artifact_ids: ids.join(",") });
    const sent = await fetch("/queue" + tokenSuffix(), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (!sent.ok) {
      output.textContent = await sent.text();
      return;
    }
    const { queued } = await sent.json();
    const note = skippedNote(ids.length, queued);
    output.textContent =
      queued === 0
        ? "None of the selected files are FBI-installable encrypted CIAs, so nothing was queued."
        : `${queued} CIA${queued === 1 ? "" : "s"} queued in FBI.` + (note ? ` ${note}` : "");
  });
}

// wireFTPSend drives the "Send via FTP" modal: an artifact picker like FBI's
// (every platform eligible, no CIA/lock restriction) plus a small remote
// directory browser for a homebrew FTP server (e.g. ftpd) on the user's own
// console. /api/ftp/browse and /api/ftp/send both treat `path` as opaque and
// just echo it back, so the current directory is state this function owns:
// "" is the login/root directory, descending appends "/name", going up
// strips back to the last "/".
export async function wireFTPSend() {
  const host = document.querySelector("[data-ftp]");
  if (!host) return;

  const response = await fetch("/api/games" + tokenSuffix());
  if (!response.ok) return;
  const { games } = await response.json();

  const cleanTitle = (title) => title.replace(/[([][^)\]]*[)\]]/g, "").replace(/\s+/g, " ").trim() || title;
  const files = [];
  for (const game of games || []) {
    for (const artifact of game.artifacts || []) {
      files.push({
        id: artifact.id,
        title: cleanTitle(game.title),
        name: artifact.name,
        platform: artifact.platform || game.platform,
        extension: (artifact.extension || "").toLowerCase(),
        cover: game.artwork_url ? `/media/${game.id}/cover` + tokenSuffix() : "",
      });
    }
  }

  const opener = document.querySelector("[data-ftp-open]");
  if (opener) opener.addEventListener("click", () => openModal(host));

  const list = host.querySelector("[data-ftp-list]");
  const output = host.querySelector("[data-ftp-output]");
  const count = host.querySelector("[data-ftp-count]");
  const allButton = host.querySelector("[data-ftp-all]");
  const sendButton = host.querySelector("[data-ftp-send]");
  const connectForm = host.querySelector("[data-ftp-connect]");
  const browser = host.querySelector("[data-ftp-browser]");
  const pathReadout = host.querySelector("[data-ftp-path]");
  const entriesEl = host.querySelector("[data-ftp-entries]");

  if (!files.length) {
    list.append(element("p", "col-span-full text-sm text-[var(--color-muted)]", "Your library is empty, add ROMs before sending them anywhere."));
  }

  for (const file of files) {
    const pick = element("label", "pick");
    pick.dataset.platform = file.platform;

    const check = document.createElement("input");
    check.type = "checkbox";
    check.value = file.id;
    check.className = "sr-only";

    const art = element("span", "grid aspect-[3/4] w-full place-items-center bg-[var(--color-well)] p-1.5");
    if (file.cover) {
      const image = document.createElement("img");
      image.loading = "lazy";
      image.src = file.cover;
      image.alt = "";
      image.className = "max-h-full max-w-full object-contain transition-opacity";
      image.addEventListener("error", () => image.remove());
      art.append(image);
    } else {
      const fallback = document.createElement("img");
      fallback.src = "/assets/logo/brand.png";
      fallback.alt = "";
      fallback.className = "max-h-10 max-w-10 object-contain opacity-40";
      art.append(fallback);
    }

    const caption = element("span", "grid gap-0.5 p-2");
    caption.append(element("strong", "text-xs leading-snug", file.title));
    caption.append(element("span", "truncate text-[.65rem] text-[var(--color-faint)]", file.name));

    // Just the format, no installability judgement: every platform and every
    // extension is equally eligible for a plain FTP copy.
    const tags = element("span", "pick-tags");
    tags.append(element("span", "pick-tag", file.extension.replace(/^\./, "").toUpperCase() || file.platform.toUpperCase()));

    pick.append(check, art, tags, element("span", "pick-mark", "✓"), caption);
    pick.title = file.name;
    list.append(pick);
  }

  // Platform filter: defaults to none active (every platform shown), unlike
  // FBI's 3DS-only default, any artifact is FTP-eligible.
  const platformChips = [...host.querySelectorAll("[data-ftp-platform]")];
  const applyPlatformFilter = () => {
    const active = new Set(platformChips.filter((chip) => chip.classList.contains("chip--active")).map((chip) => chip.dataset.ftpPlatform));
    for (const pick of list.querySelectorAll(".pick")) pick.hidden = active.size > 0 && !active.has(pick.dataset.platform);
  };
  platformChips.forEach((chip) => {
    chip.addEventListener("click", () => {
      chip.classList.toggle("chip--active");
      applyPlatformFilter();
    });
  });
  applyPlatformFilter();

  const boxes = () => [...list.querySelectorAll("input")];
  const selected = () => boxes().filter((box) => box.checked).map((box) => box.value);

  let connected = false;
  const updateSendVisibility = () => {
    sendButton.hidden = !(connected && selected().length);
  };
  const updateCount = () => {
    const n = selected().length;
    count.textContent = n ? `${n} selected` : "";
    updateSendVisibility();
  };
  list.addEventListener("change", updateCount);

  allButton.addEventListener("click", () => {
    const visible = boxes().filter((box) => !box.closest(".pick").hidden);
    const allChecked = visible.every((box) => box.checked);
    visible.forEach((box) => (box.checked = !allChecked));
    updateCount();
  });

  // --- Remote directory browser ---------------------------------------
  let currentPath = "";

  const connParams = () => ({
    host: connectForm.host.value.trim(),
    port: connectForm.port.value.trim(),
    user: connectForm.user.value.trim(),
    pass: connectForm.pass.value,
  });

  const renderEntries = (entries) => {
    entriesEl.replaceChildren();
    pathReadout.textContent = currentPath || "/";

    if (currentPath !== "") {
      const up = element("button", "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-[var(--color-surface-2)]", "⬆ ..");
      up.type = "button";
      up.addEventListener("click", () => browse(currentPath.slice(0, currentPath.lastIndexOf("/"))));
      entriesEl.append(up);
    }

    if (!entries.length) {
      entriesEl.append(element("p", "px-2 py-1.5 text-sm text-[var(--color-muted)]", "This folder is empty."));
    }

    for (const entry of entries) {
      if (entry.dir) {
        const row = element("button", "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-[var(--color-surface-2)]");
        row.type = "button";
        row.append(element("span", "", "📁"), element("span", "truncate", entry.name));
        row.addEventListener("click", () => browse(currentPath === "" ? "/" + entry.name : currentPath + "/" + entry.name));
        entriesEl.append(row);
      } else {
        const row = element("div", "flex w-full items-center gap-2 px-2 py-1.5 text-sm text-[var(--color-muted)]");
        row.append(element("span", "", "📄"), element("span", "truncate", entry.name));
        if (entry.size !== undefined) row.append(element("span", "ml-auto text-xs text-[var(--color-faint)]", formatSize(entry.size)));
        entriesEl.append(row);
      }
    }
  };

  const browse = async (path) => {
    output.replaceChildren();
    const body = new URLSearchParams({ ...connParams(), path });
    const resp = await fetch("/api/ftp/browse" + tokenSuffix(), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (!resp.ok) {
      connected = false;
      browser.hidden = true;
      updateSendVisibility();
      output.textContent = await resp.text();
      return;
    }
    const data = await resp.json();
    currentPath = data.path || "";
    connected = true;
    browser.hidden = false;
    renderEntries(data.entries || []);
    updateSendVisibility();
  };

  connectForm.addEventListener("submit", (event) => {
    event.preventDefault();
    currentPath = "";
    browse("");
  });

  sendButton.addEventListener("click", async () => {
    const ids = selected();
    if (!ids.length) {
      output.textContent = "Select at least one file.";
      return;
    }
    output.textContent = "Sending…";
    const body = new URLSearchParams({ ...connParams(), path: currentPath, artifact_ids: ids.join(",") });
    const resp = await fetch("/api/ftp/send" + tokenSuffix(), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (!resp.ok) {
      output.textContent = await resp.text();
      return;
    }
    const { sent, results } = await resp.json();
    output.replaceChildren();
    const total = (results || []).length;
    output.append(
      element(
        "p",
        "text-[var(--color-muted)]",
        sent === total ? `All ${sent} file${sent === 1 ? "" : "s"} sent.` : `${sent} of ${total} file${total === 1 ? "" : "s"} sent.`,
      ),
    );
    for (const result of results || []) {
      const row = element("p", result.error ? "text-[var(--color-danger)]" : "", `${result.error ? "✗" : "✓"} ${result.name}${result.error ? ", " + result.error : ""}`);
      output.append(row);
    }
  });
}
