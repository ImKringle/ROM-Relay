// ROM Relay's client-side behaviour.
//
// htmx does the request/swap work; everything below is the small remainder that
// htmx has no opinion about. It is bundled (with htmx) into dist/app.js, which is
// served as a same-origin file, that is what lets the CSP drop 'unsafe-inline'.
//
// No hx-on attributes are used anywhere in the templates, for the same reason:
// they would need an inline-script exemption.
import "htmx.org";
import { wireAdmin, wireShutdown } from "./admin.js";
import { wirePokedex, wireTrainers } from "./pokedex.js";
import { wirePokehome } from "./pokehome.js";
import { wireStandaloneEditor } from "./editor.js";
import { wireSavesList, wireUploadPanel } from "./saves.js";
import { wireBackgroundWork, wireFBIBatch, wireFTPSend, wireGameModal, wireLibraryUpload, wireModals, wireUploadTools } from "./library.js";
import { wirePatchWorkspace } from "./patches.js";
import { wireRandomizer } from "./randomizer.js";
import { wireTypeChart } from "./typechart.js";
import { wireSetup } from "./setup.js";
import { wireUsers } from "./users.js";
import { wireAPIKeys } from "./apikeys.js";
import { wireWebDAVKeys } from "./webdavkeys.js";
import { wireRoms } from "./roms.js";
import { wirePlayers } from "./players.js";
import { wireSettings } from "./settings.js";

// Progressive enhancement: with JavaScript off, the filter form is submitted by
// its Apply button and the page reloads. Once this runs, htmx applies filters as
// they change, so the button is redundant and hidden. Doing it here rather than
// in CSS means the button is visible exactly when it is needed.
function hideJavaScriptOnlyControls() {
  for (const element of document.querySelectorAll("[data-js-hidden]")) {
    element.classList.add("htmx-only-hidden");
  }
}

// The filter drawer (narrow screens only). The panel and its contents are the
// same markup at every width; this only toggles its open state.
function wireFilterDrawer() {
  const panel = document.querySelector("[data-filters]");
  const toggle = document.querySelector("[data-filters-toggle]");
  if (!panel || !toggle) return;

  const setOpen = (open) => {
    panel.dataset.open = String(open);
    toggle.setAttribute("aria-expanded", String(open));
    scrim().hidden = !open;
  };

  const scrim = () => {
    let element = document.querySelector("[data-filters-scrim]");
    if (!element) {
      element = document.createElement("div");
      element.className = "filters-scrim";
      element.dataset.filtersScrim = "";
      element.hidden = true;
      element.addEventListener("click", () => setOpen(false));
      document.body.appendChild(element);
    }
    return element;
  };

  toggle.addEventListener("click", () => setOpen(panel.dataset.open !== "true"));
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && panel.dataset.open === "true") setOpen(false);
  });
}

// The collapsed navigation (narrow screens). The links are server-rendered in
// the header; this only opens and closes the dropdown panel.
function wireNav() {
  const button = document.querySelector("[data-nav-toggle]");
  const links = document.querySelector("#site-nav");
  if (!button || !links) return;

  // The activity widget is a sibling <details>, not inside #site-nav, so
  // opening one never closed the other on its own, they'd stack on top of
  // each other in the same corner of the header.
  const activity = document.querySelector("#activity-widget");

  const setOpen = (open) => {
    links.dataset.open = String(open);
    button.setAttribute("aria-expanded", String(open));
    if (open && activity) activity.removeAttribute("open");
  };

  button.addEventListener("click", (event) => {
    event.stopPropagation();
    setOpen(links.dataset.open !== "true");
  });
  document.addEventListener("click", (event) => {
    if (links.dataset.open === "true" && !links.contains(event.target)) setOpen(false);
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") setOpen(false);
  });
  if (activity) {
    activity.addEventListener("toggle", () => {
      if (activity.open) setOpen(false);
    });
  }
}

function start() {
  hideJavaScriptOnlyControls();
  wireFilterDrawer();
  wireNav();
  wireBackgroundWork();
  wireModals();
  wireGameModal();
  wireFBIBatch();
  wireFTPSend();
  wireUploadTools();
  wireLibraryUpload();
  wireUploadPanel();
  wirePokedex();
  wireTrainers();
  wirePokehome();
  wireStandaloneEditor();
  wireSavesList();
  wireSetup();
  wirePatchWorkspace();
  wireRandomizer();
  wireTypeChart();
  wireAdmin();
  wireShutdown();
  wireUsers();
  wireAPIKeys();
  wireWebDAVKeys();
  wireRoms();
  wirePlayers();
  wireSettings();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}
