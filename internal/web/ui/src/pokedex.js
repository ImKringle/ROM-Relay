// The Pokédex grid (wirePokedex) and the trainer roster (wireTrainers) are
// separate pages now, each wiring function gates on an element only its own
// page has, so importing both into the shared app.js bundle is safe.
//
// The National Pokédex is 1,025 entries, far too much markup to render
// server-side on every visit, so the page ships its shell and fills it from
// /api/dex and /api/userdex.
import { element, spriteURL, tokenSuffix } from "./util.js";
import { wireUploadPanel } from "./saves.js";

function dexNumber(n) {
  return "#" + String(n).padStart(3, "0");
}

function shinyIcon(className) {
  const img = document.createElement("img");
  img.className = className;
  img.src = "/assets/shiny/shiny.png";
  img.alt = "Shiny";
  return img;
}

function formatMinutes(minutes) {
  const h = Math.floor((minutes || 0) / 60);
  const m = (minutes || 0) % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function formatMoney(n) {
  return "₽" + (n || 0).toLocaleString();
}

// A chip-shaped toggle button; gold when active.
function pill(label, active, onClick, hint) {
  const button = element("button", "chip" + (active ? " chip--active" : ""), label);
  button.type = "button";
  if (hint) button.title = hint;
  if (active) button.setAttribute("aria-pressed", "true");
  button.addEventListener("click", onClick);
  return button;
}

// wireUploadTrigger opens the shared upload-save popup (a plain .modal-scrim,
// closed the same way every other modal in this app is: wireModals(), called
// once from app.js, already wires the scrim click / Escape / close button for
// any .modal-scrim present, this only needs to handle opening it).
function wireUploadTrigger() {
  const scrim = document.querySelector("[data-upload-scrim]");
  const opener = document.querySelector("[data-upload-open]");
  opener?.addEventListener("click", () => {
    scrim.dataset.open = "true";
  });
}

export function wirePokedex() {
  const grid = document.querySelector("#dex-grid");
  if (!grid) return;
  wireUploadTrigger();

  // ---- The dex grid --------------------------------------------------------

  const stats = document.querySelector("#dex-stats");
  const shinyLabel = document.querySelector("#dex-shiny");
  const progress = document.querySelector("#dex-progress");
  const regionsRow = document.querySelector("#dex-regions");
  const gamesRow = document.querySelector("#dex-games");
  const controls = document.querySelector("#dex-controls");

  // The current view: a region filter on its own, optionally narrowed to one
  // game inside it. Scope (regional/national numbering) applies per game.
  let currentRegion = "";
  let currentGame = "";
  let currentScope = "";
  let currentScopeName = "";
  let currentFilter = "all";
  let currentEntries = [];
  let entriesByDex = new Map();
  let knownGames = []; // last /api/dex games list: [{code,name,region}]
  const speciesNames = {};

  async function load() {
    const query = new URLSearchParams();
    const token = new URLSearchParams(location.search).get("token");
    if (token) query.set("token", token);
    if (currentGame) {
      query.set("game", currentGame);
      if (currentScope) query.set("scope", currentScope);
    } else if (currentRegion) {
      query.set("region", currentRegion);
    }
    const response = await fetch("/api/dex" + (query.toString() ? "?" + query : ""));
    if (!response.ok) {
      stats.textContent = "Could not load dex: " + (await response.text());
      return;
    }
    const view = await response.json();
    if (view.games && view.games.length) knownGames = view.games;
    if (currentGame) currentScope = view.scope_kind || "";
    currentScopeName = view.scope_name || view.scope;

    const label =
      view.scope_kind === "global"
        ? "Global Pokédex"
        : view.scope_kind === "region"
          ? `${view.region} Pokédex, every ${view.region} game you play`
          : `${view.scope_name}: ${view.scope_kind === "regional" ? view.region + " Pokédex" : "National Pokédex"}`;
    const verb = "caught";
    // caught_count (PC copies actually held) is a second, independent figure
    // from owned/percent (the save's own obtained flag), shown only when it
    // says something the obtained count doesn't already, so an account with
    // no PC data at all reads exactly as it did before this existed.
    const held = (view.entries || []).reduce((n, e) => n + (e.caught_count || 0), 0);
    stats.textContent =
      `${label} · ${view.owned} / ${view.total} ${verb} (${view.percent}%)` + (held > 0 ? ` · ${held} held in PC` : "");
    shinyLabel.replaceChildren();
    if (view.shiny) shinyLabel.append(shinyIcon("dex-shiny-icon"), document.createTextNode(`${view.shiny} shiny`));
    progress.style.width = view.percent + "%";
    progress.parentElement.title = `${view.percent}% of ${view.total}`;

    renderRegionRow();
    renderGameRow();
    renderControls(view);

    currentEntries = view.entries || [];
    entriesByDex = new Map(currentEntries.map((entry) => [entry.dex, entry]));
    for (const entry of currentEntries) speciesNames[entry.dex] = entry.name;
    renderGrid();
  }

  function regionsOf(games) {
    const seen = [];
    for (const game of games) {
      const region = game.region || "Other";
      if (!seen.includes(region)) seen.push(region);
    }
    return seen;
  }

  function renderRegionRow() {
    regionsRow.replaceChildren();
    regionsRow.append(element("span", "eyebrow eyebrow--muted", "Region"));
    regionsRow.append(
      pill("Global", !currentRegion && !currentGame, () => {
        currentRegion = "";
        currentGame = "";
        currentScope = "";
        load();
      }, "Every species you hold, across every region and game."),
    );
    for (const region of regionsOf(knownGames)) {
      regionsRow.append(
        pill(region, currentRegion === region, () => {
          currentRegion = region;
          currentGame = "";
          currentScope = "";
          load();
        }, `${region}'s Pokédex across every ${region} game you play.`),
      );
    }
  }

  function renderGameRow() {
    gamesRow.replaceChildren();
    if (!currentRegion) {
      gamesRow.hidden = true;
      return;
    }
    const games = knownGames.filter((game) => (game.region || "Other") === currentRegion);
    gamesRow.hidden = games.length === 0;
    gamesRow.append(element("span", "eyebrow eyebrow--muted", "Game"));
    gamesRow.append(
      pill(`All ${currentRegion} games`, !currentGame, () => {
        currentGame = "";
        currentScope = "";
        load();
      }),
    );
    for (const game of games) {
      gamesRow.append(
        pill(game.name, currentGame === game.code, () => {
          currentGame = game.code;
          currentScope = "";
          load();
        }, `${game.name}'s own Pokédex: what you caught while playing it.`),
      );
    }
  }

  // The scope toggle (Regional / National, when the game has both) and the
  // ownership filters (every view).
  function renderControls(view) {
    controls.replaceChildren();
    if (currentGame && (view.scopes || []).length > 1) {
      const scopeRow = element("div", "flex flex-wrap items-center gap-1.5");
      scopeRow.append(element("span", "eyebrow eyebrow--muted", "Numbering"));
      scopeRow.append(
        pill(`Regional (${view.region})`, view.scope_kind === "regional", () => {
          currentScope = "regional";
          load();
        }, "The game's own Pokédex, in its own numbering."),
        pill("National", view.scope_kind === "national", () => {
          currentScope = "national";
          load();
        }, "Everything this game's generation can know about."),
      );
      controls.append(scopeRow);
    }

    const filterRow = element("div", "flex flex-wrap items-center gap-1.5");
    filterRow.append(element("span", "eyebrow eyebrow--muted", "Show"));
    for (const [key, label, hint] of [
      ["all", "All", "Every entry in this dex."],
      ["missing", "Missing", "Entries you have not caught in this view."],
      ["owned", "Caught", "Entries you have caught in this view."],
      ["shiny", "Shiny", "Entries you hold a shiny of."],
    ]) {
      filterRow.append(
        pill(label, currentFilter === key, () => {
          currentFilter = key;
          renderControls(view);
          renderGrid();
        }, hint),
      );
    }
    controls.append(filterRow);
  }

  function renderGrid() {
    const show = {
      all: () => true,
      missing: (entry) => !entry.owned,
      owned: (entry) => entry.owned,
      shiny: (entry) => entry.owned && entry.shiny,
    }[currentFilter];
    const fragment = document.createDocumentFragment();
    for (const entry of currentEntries) {
      if (show(entry)) fragment.append(dexCell(entry));
    }
    grid.replaceChildren(fragment);
  }

  function dexCell(entry) {
    const cell = element(
      "div",
      "relative cursor-pointer rounded-lg border border-[var(--color-line)] bg-[var(--color-surface)] px-1 pb-1 pt-1.5 text-center transition-colors hover:border-[var(--color-accent)]",
    );

    // A greyed-out cell says nothing about why on its own, and neither does a
    // "×3" or a sparkle. Spell each out on hover rather than expecting the grid
    // to be self-explanatory.
    cell.title = describeCell(entry);
    // Opening is handled by one delegated listener on the grid, not a pair
    // per cell, the National dex is 1,025 of these.
    cell.dataset.dex = entry.dex;

    const image = document.createElement("img");
    image.loading = "lazy";
    image.alt = entry.name;
    image.src = spriteURL(entry.dex, entry.owned && entry.shiny);
    image.className =
      "mx-auto block h-[72px] w-[72px] [image-rendering:pixelated]" +
      (entry.owned ? "" : " opacity-55 grayscale brightness-[.35]");
    // A sprite may be missing for a very new species; hide it rather than show a
    // broken-image icon in the middle of the grid.
    image.addEventListener("error", () => (image.style.visibility = "hidden"));
    cell.append(image);

    if (entry.owned && entry.shiny) {
      cell.append(shinyIcon("absolute right-1.5 top-1 h-3.5 w-3.5"));
    }
    // A regional view reads in the game's own numbering, with the National
    // number kept underneath, both are real, and hiding either invites a hunt.
    if (entry.regional) {
      cell.append(element("div", "pixel text-[.5rem] text-[var(--color-muted)]", dexNumber(entry.regional)));
    } else {
      cell.append(element("div", "pixel text-[.5rem] text-[var(--color-muted)]", dexNumber(entry.dex)));
    }
    cell.append(
      element("div", "truncate text-[.72rem] capitalize" + (entry.owned ? "" : " text-[var(--color-muted)]"), entry.name),
    );

    cell.tabIndex = 0;
    return cell;
  }

  // One click/keydown listener for the whole grid instead of two per cell
  // (up to ~2,050 across the National dex), event.target.closest finds which
  // cell was actually activated, entriesByDex resolves it back to its entry.
  grid.addEventListener("click", (event) => {
    const cell = event.target.closest("[data-dex]");
    if (!cell) return;
    const entry = entriesByDex.get(Number(cell.dataset.dex));
    if (entry) openSpecies(entry);
  });
  grid.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const cell = event.target.closest("[data-dex]");
    if (!cell) return;
    event.preventDefault();
    const entry = entriesByDex.get(Number(cell.dataset.dex));
    if (entry) openSpecies(entry);
  });

  // describeCell is the hover text for one species: what it is, and what this
  // view is saying about it.
  function describeCell(entry) {
    const number = entry.regional
      ? `${currentScopeName} ${dexNumber(entry.regional)} (National ${dexNumber(entry.dex)})`
      : dexNumber(entry.dex);
    const where = currentGame || currentRegion ? "in " + currentScopeName : "in your Pokédex";
    if (!entry.owned) return `${entry.name} ${number}, not caught ${where} yet`;
    return `${entry.name} ${number}, caught ${where}${entry.shiny ? " · shiny" : ""}`;
  }

  // ---- Species panel ------------------------------------------------------
  // Reference data lives behind /api/dex/species/{dex} and is only fetched when
  // a cell is actually opened, the grid is 1,025 cells.

  const scrim = document.querySelector("[data-species-scrim]");

  // Guards the panel against a slow response landing after a faster one: only
  // the most recently opened species may fill it.
  let openToken = 0;

  function table(headers, rows) {
    const wrap = element("div", "table-scroll");
    const tbl = element("table", "table");
    const thead = document.createElement("thead");
    const headRow = document.createElement("tr");
    for (const h of headers) headRow.append(element("th", "", h));
    thead.append(headRow);
    const tbody = document.createElement("tbody");
    for (const cells of rows) {
      const tr = document.createElement("tr");
      for (const cell of cells) {
        const td = element("td", "");
        if (cell instanceof Node) td.append(cell);
        else td.textContent = cell;
        tr.append(td);
      }
      tbody.append(tr);
    }
    tbl.append(thead, tbody);
    wrap.append(tbl);
    return wrap;
  }

  function speciesCell(dex) {
    const wrap = element("span", "inline-flex items-center gap-1.5 whitespace-nowrap");
    const image = document.createElement("img");
    image.loading = "lazy";
    image.width = 32;
    image.height = 32;
    image.className = "h-8 w-8 [image-rendering:pixelated]";
    image.src = spriteURL(dex, false);
    image.alt = "";
    image.addEventListener("error", () => (image.style.visibility = "hidden"));
    wrap.append(
      image,
      element("span", "font-semibold capitalize", speciesNames[dex] || "???"),
      element("span", "pixel text-[.5rem] text-[var(--color-faint)]", dexNumber(dex)),
    );
    return wrap;
  }

  const tidy = (value) => (value || "").replaceAll("-", " ");
  const titleCase = (value) => value.replace(/\w\S*/g, (w) => w[0].toUpperCase() + w.slice(1));
  const ROMAN = ["", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X"];

  function typeIcon(type, size) {
    const img = document.createElement("img");
    img.src = `/assets/typebadges/${type}.png`;
    img.alt = "";
    img.width = size;
    img.height = size;
    img.addEventListener("error", () => (img.style.visibility = "hidden"));
    return img;
  }
  function typeChip(type) {
    const chip = element("span", "type-chip");
    const badge = document.createElement("img");
    badge.src = `/assets/typebadges/${type}.png`;
    badge.alt = tidy(type);
    badge.width = 64;
    badge.height = 28;
    badge.addEventListener("error", () => (badge.style.visibility = "hidden"));
    chip.append(badge);
    return chip;
  }

  function genderIcon(which) {
    const img = document.createElement("img");
    img.src = `/assets/gender/${which}.png`;
    img.alt = which === "male" ? "Male" : "Female";
    img.className = "gender-icon";
    img.width = 16;
    img.height = 16;
    return img;
  }

  // ---- Type matchups: fixed 18-slot ledger, grouped by what the multiplier
  // means rather than spelled out as a sentence per type. ---------------------
  const ALL_TYPES = [
    "normal", "fire", "water", "electric", "grass", "ice", "fighting", "poison", "ground",
    "flying", "psychic", "bug", "rock", "ghost", "dragon", "dark", "steel", "fairy",
  ];
  function matchupLedgerRow(label, hint, types, matchups, state) {
    const row = element("div", "mb-3.5 last:mb-0");
    const head = element("div", "mb-1.5 flex items-baseline gap-2");
    head.append(element("span", "text-sm font-semibold", label), element("span", "text-xs text-[var(--color-faint)]", hint));
    row.append(head);
    const grid = element("div", "ledger-grid");
    for (const type of types) {
      const chip = element("div", "tchip");
      chip.append(typeIcon(type, 20));
      chip.dataset.state = state === "weak" && matchups[type] >= 4 ? "quad" : state;
      chip.style.setProperty("--tc", `var(--type-${type})`);
      chip.title = `${tidy(type)}: ×${matchups[type]}`;
      grid.append(chip);
    }
    if (!types.length) grid.append(element("div", "col-span-full text-sm text-[var(--color-faint)]", "None."));
    row.append(grid);
    return row;
  }
  function renderMatchups(matchups) {
    const box = document.querySelector("#species-matchups");
    const weak = ALL_TYPES.filter((t) => matchups[t] > 1);
    const resist = ALL_TYPES.filter((t) => matchups[t] > 0 && matchups[t] < 1);
    const immune = ALL_TYPES.filter((t) => matchups[t] === 0);
    box.replaceChildren(
      matchupLedgerRow("Weak to", "takes extra damage", weak, matchups, "weak"),
      matchupLedgerRow("Resists", "takes less damage", resist, matchups, "resist"),
      matchupLedgerRow("Immune to", "takes none", immune, matchups, "immune"),
    );
    const quad = weak.filter((t) => matchups[t] >= 4);
    const bits = [];
    if (quad.length) bits.push(`4× ${quad.map(tidy).join(", ")}`);
    const doubleWeak = weak.filter((t) => matchups[t] < 4);
    if (doubleWeak.length) bits.push(`weak to ${doubleWeak.map(tidy).join(", ")}`);
    if (resist.length) bits.push(`resists ${resist.length}`);
    if (immune.length) bits.push(`immune to ${immune.map(tidy).join(", ")}`);
    document.querySelector("#species-matchup-glance").textContent = bits.join(" · ") || "No notable matchups.";
  }

  // ---- Base Stats: the same kernel-striped bar the dex completion meter uses,
  // stacked six ways and scaled to 255, the real universal cap every base
  // stat in the games tops out at (Blissey's 255 HP, Shuckle's 230 Defense).
  // Reusing .kernelbar rather than a new chart style keeps the reading "one
  // more kernel row," not a foreign widget bolted onto the cartridge look.
  const STAT_CAP = 255;
  const STAT_LABELS = [
    ["stat_hp", "HP"],
    ["stat_atk", "Atk"],
    ["stat_def", "Def"],
    ["stat_spa", "SpA"],
    ["stat_spd", "SpD"],
    ["stat_spe", "Spe"],
  ];
  function statRow(label, value) {
    const row = element("div", "grid grid-cols-[2.6rem_1fr_2.2rem] items-center gap-2.5");
    row.append(element("span", "pixel text-[.55rem] text-[var(--color-muted)]", label));
    const bar = element("div", "kernelbar");
    const fill = document.createElement("i");
    fill.style.width = Math.min(100, (value / STAT_CAP) * 100) + "%";
    bar.append(fill);
    row.append(bar);
    row.append(element("span", "pixel text-right text-[.6rem]", String(value)));
    return row;
  }
  function renderStats(species) {
    const box = document.querySelector("#species-stats");
    const total = STAT_LABELS.reduce((sum, [key]) => sum + (species[key] || 0), 0);
    if (total === 0) {
      box.replaceChildren(element("p", "m-0 text-sm text-[var(--color-muted)]", "Base stats are not available yet."));
      document.querySelector("#species-stats-glance").textContent = "";
      return;
    }
    const grid = element("div", "grid gap-2");
    for (const [key, label] of STAT_LABELS) grid.append(statRow(label, species[key] || 0));
    box.replaceChildren(grid);
    document.querySelector("#species-stats-glance").textContent = `BST ${total}`;
  }

  // ---- Breeding: egg groups, gender ratio, egg cycles. -----------------------
  function renderBreeding(species) {
    const box = document.querySelector("#species-breeding");
    const grid = element("div", "grid grid-cols-2 gap-2.5 sm:grid-cols-3");
    const fact = (label, value, extra) => {
      const cell = element("div", "rounded-lg border border-[var(--color-line)] bg-[var(--color-surface-2)] px-3 py-1.5");
      const valueEl = element("div", "mt-1 text-sm font-semibold");
      if (value instanceof Node) valueEl.append(value);
      else valueEl.textContent = value;
      cell.append(element("div", "eyebrow eyebrow--muted", label), valueEl);
      if (extra) cell.append(extra);
      return cell;
    };
    const groups = (species.egg_groups || "").split(",").filter(Boolean).map(tidy).map(titleCase).join(" / ") || "Undiscovered";
    grid.append(fact("Egg groups", groups));

    let genderText = "Genderless";
    let genderValue = "Genderless";
    let genderBar = null;
    if (species.gender_rate >= 0) {
      const female = (species.gender_rate / 8) * 100;
      const male = 100 - female;
      genderText = `${male.toFixed(1)}% male / ${female.toFixed(1)}% female`;
      genderValue = element("span", "inline-flex items-center gap-1");
      genderValue.append(genderIcon("male"), document.createTextNode(`${male.toFixed(1)}%`), genderIcon("female"), document.createTextNode(`${female.toFixed(1)}%`));
      genderBar = element("div", "gender-bar mt-1.5");
      genderBar.append(element("i", "m"), element("i", "f"));
      genderBar.children[0].style.width = male + "%";
      genderBar.children[1].style.width = female + "%";
    }
    grid.append(fact("Gender ratio", genderValue, genderBar));

    const cycles = species.hatch_counter || 0;
    grid.append(fact("Egg cycles", cycles ? `${cycles} cycles` : "Unknown", cycles ? element("div", "mt-1 text-xs text-[var(--color-faint)]", `≈${(cycles * 257).toLocaleString()} steps`) : null));

    box.replaceChildren(grid);
    document.querySelector("#species-breeding-glance").textContent = `${groups} · ${genderText}${cycles ? " · " + cycles + " cycles" : ""}`;
  }

  // ---- Moves: level-up and machine, generation-filtered server-side so the
  // panel never has to hold more than one generation's kit in memory. ---------
  function renderMoves(learnset, generation, generations) {
    const box = document.querySelector("#species-moves");
    box.replaceChildren();

    if (generations.length > 1) {
      const genRow = element("div", "mb-3 flex flex-wrap items-center gap-1.5");
      genRow.append(element("span", "eyebrow eyebrow--muted", "Generation"));
      for (const g of generations) {
        genRow.append(pill(ROMAN[g] || String(g), g === generation, () => loadSpecies(currentSpeciesEntry, g)));
      }
      box.append(genRow);
    }

    const levelCell = (m) => {
      if (m.level !== 1) return String(m.level || "—");
      const cell = element("span", "inline-flex items-center gap-1");
      const egg = document.createElement("img");
      egg.src = "/sprites/pokemon/egg.png";
      egg.alt = "Likely egg move";
      egg.title = "Level 1, likely an egg move";
      egg.width = 14;
      egg.height = 14;
      egg.addEventListener("error", () => (egg.style.visibility = "hidden"));
      cell.append(document.createTextNode("1"), egg);
      return cell;
    };
    const moveRow = (m) => [
      m.method === "level-up" ? levelCell(m) : "TM",
      (() => {
        const cell = element("span", "capitalize");
        const dot = typeIcon(m.type, 14);
        dot.className = "move-type-dot";
        cell.append(dot, document.createTextNode(tidy(m.move)));
        return cell;
      })(),
      (() => {
        if (!m.category) return "—";
        const cat = document.createElement("img");
        cat.className = "movecat";
        cat.src = `/assets/movecat/${m.category}.png`;
        cat.alt = tidy(m.category);
        cat.title = tidy(m.category);
        cat.addEventListener("error", () => (cat.style.visibility = "hidden"));
        return cat;
      })(),
      m.power || "—",
      m.accuracy ? m.accuracy + "%" : "—",
    ];

    const levelMoves = learnset.filter((m) => m.method === "level-up").sort((a, b) => (a.level || 0) - (b.level || 0));
    const machineMoves = learnset.filter((m) => m.method === "machine").sort((a, b) => a.move.localeCompare(b.move));

    box.append(element("p", "mb-1.5 text-sm font-semibold", "Level up"));
    box.append(
      levelMoves.length
        ? table(["Lv.", "Move", "Cat", "Pwr", "Acc"], levelMoves.map(moveRow))
        : element("p", "m-0 mb-3 text-sm text-[var(--color-muted)]", "No level-up moves recorded for this generation."),
    );
    box.append(element("p", "mb-1.5 mt-3.5 text-sm font-semibold", "TM / Machine"));
    box.append(
      machineMoves.length
        ? table(["Move", "Cat", "Pwr", "Acc"], machineMoves.map((m) => moveRow(m).filter((_, i) => i !== 0)))
        : element("p", "m-0 text-sm text-[var(--color-muted)]", "No machine moves recorded for this generation."),
    );

    document.querySelector("#species-moves-glance").textContent =
      `${ROMAN[generation] || generation}: ${levelMoves.length} level-up, ${machineMoves.length} TM`;
  }

  // ---- Where to find it: region-filtered client-side, since one response
  // already carries every region the species has encounters in. --------------
  let currentEncounterRegion = "";
  function renderEncounters(spots, regions) {
    const box = document.querySelector("#species-encounters");
    box.replaceChildren();
    if (!spots.length) {
      box.append(
        element(
          "p", "m-0 text-sm text-[var(--color-muted)]",
          currentGame
            ? "No wild encounters recorded for this game. Evolve, trade, or transfer it."
            : "No wild encounters recorded. Pick a game to see per-version locations.",
        ),
      );
      document.querySelector("#species-encounters-glance").textContent = "Not found in the wild";
      return;
    }
    if (!regions.includes(currentEncounterRegion)) currentEncounterRegion = "";

    if (regions.length > 1) {
      const regionRow = element("div", "mb-3 flex flex-wrap items-center gap-1.5");
      regionRow.append(element("span", "eyebrow eyebrow--muted", "Region"));
      regionRow.append(pill("All", !currentEncounterRegion, () => { currentEncounterRegion = ""; renderEncounters(spots, regions); }));
      for (const region of regions) {
        regionRow.append(pill(region, currentEncounterRegion === region, () => { currentEncounterRegion = region; renderEncounters(spots, regions); }));
      }
      box.append(regionRow);
    }

    const shown = currentEncounterRegion ? spots.filter((s) => s.region === currentEncounterRegion) : spots;
    box.append(
      table(
        ["Version", "Location", "Method", "Level", "Rate", "Conditions"],
        shown.map((spot) => [
          element("span", "capitalize", tidy(spot.version)),
          element("span", "capitalize", tidy(spot.location)),
          element("span", "capitalize", tidy(spot.method)),
          spot.min_level
            ? spot.min_level === spot.max_level
              ? String(spot.min_level)
              : `${spot.min_level}–${spot.max_level}`
            : "",
          spot.chance ? spot.chance + "%" : "",
          element("span", "capitalize", tidy(spot.conditions)),
        ]),
      ),
    );
    const uniqueRegions = [...new Set(spots.map((s) => s.region).filter(Boolean))];
    document.querySelector("#species-encounters-glance").textContent =
      uniqueRegions.length ? uniqueRegions.join(", ") : `${spots.length} location${spots.length === 1 ? "" : "s"}`;
  }

  let currentSpeciesEntry = null;

  async function openSpecies(entry) {
    if (!scrim) return;
    currentSpeciesEntry = entry;
    scrim.dataset.open = "true";
    scrim.querySelector(".modal").scrollTop = 0;

    document.querySelector("#species-name").textContent = entry.name;
    document.querySelector("#species-number").textContent =
      (entry.regional ? dexNumber(entry.regional) + " · National " : "") + dexNumber(entry.dex);
    const sprite = document.querySelector("#species-sprite");
    sprite.src = spriteURL(entry.dex, entry.owned && entry.shiny);
    sprite.alt = entry.name;
    document.querySelector("#species-genus").textContent = "";
    document.querySelector("#species-types").replaceChildren();
    document.querySelector("#species-flavor").textContent = "Loading…";
    for (const id of ["species-matchups", "species-stats", "species-breeding", "species-moves", "species-evolution", "species-encounters"]) {
      document.querySelector("#" + id).replaceChildren();
    }
    for (const id of ["species-matchup-glance", "species-stats-glance", "species-breeding-glance", "species-moves-glance", "species-evolution-glance", "species-encounters-glance"]) {
      document.querySelector("#" + id).textContent = "";
    }

    await loadSpecies(entry, null);
  }

  // loadSpecies fills the panel for the currently open species; a generation
  // switch re-calls it with an explicit gen rather than re-opening the modal.
  async function loadSpecies(entry, gen) {
    if (!scrim) return;
    const token = ++openToken;
    const flavor = document.querySelector("#species-flavor");

    const query = new URLSearchParams();
    const viewerToken = new URLSearchParams(location.search).get("token");
    if (viewerToken) query.set("token", viewerToken);
    // Same game/region priority as the grid's own /api/dex fetch: a game
    // scopes to its own region implicitly, a bare region view narrows without
    // one, and the global dex (neither set) leaves every region in.
    if (currentGame) query.set("game", currentGame);
    else if (currentRegion) query.set("region", currentRegion);
    if (gen) query.set("gen", String(gen));
    const response = await fetch(`/api/dex/species/${entry.dex}` + (query.toString() ? "?" + query : ""));
    if (token !== openToken) return;
    if (!response.ok) {
      // Enrichment is optional, so say so plainly rather than showing blanks.
      flavor.textContent =
        response.status === 404
          ? "Pokédex details are not available. Switch on [metadata.pokeapi] in config.ini to fill them in."
          : await response.text();
      return;
    }
    const data = await response.json();
    if (token !== openToken) return;
    const { species, evolutions, encounters: spots, encounter_regions: regions, matchups, learnset, learnset_generation: activeGen, learnset_generations: gens } = data;

    document.querySelector("#species-genus").textContent = species.genus || "";
    flavor.textContent = species.flavor_text || "No description recorded.";
    document.querySelector("#species-types").replaceChildren(
      ...(species.types || "").split("/").filter(Boolean).map(typeChip),
    );

    renderMatchups(matchups || {});
    renderStats(species);
    renderBreeding(species);
    renderMoves(learnset || [], activeGen || 0, gens || []);
    renderEncounters(spots || [], regions || []);

    const evolution = document.querySelector("#species-evolution");
    if (!evolutions || evolutions.length === 0) {
      evolution.append(element("p", "m-0 text-sm text-[var(--color-muted)]", "This Pokémon does not evolve."));
      document.querySelector("#species-evolution-glance").textContent = "Does not evolve";
    } else {
      evolution.append(
        table(
          ["From", "Into", "How"],
          evolutions.map((evo) => [
            speciesCell(evo.from_dex),
            speciesCell(evo.to_dex),
            evo.condition || "Level up",
          ]),
        ),
      );
      document.querySelector("#species-evolution-glance").textContent =
        evolutions.map((evo) => `${speciesNames[evo.from_dex] || "?"} → ${speciesNames[evo.to_dex] || "?"}`).join(", ");
    }
  }

  // A save the parser read changes the grid, so refresh it.
  wireUploadPanel((result) => {
    if (result.parsed) load();
  });
  load();
  wireLookupWidget();

  // ---- Pokemon Lookup: a pure reference-data search (type, move-by-method,
  // version availability) independent of caught/seen state. Results render
  // only inside this widget; clicking one opens the same species panel the
  // dex grid uses, since that panel is itself reference data, not dex state. --
  function wireLookupWidget() {
    const lookupScrim = document.querySelector("#lookup-modal");
    const openBtn = document.querySelector("[data-lookup-open]");
    if (!lookupScrim || !openBtn) return;

    let gamesLoaded = false;
    async function ensureLookupGames() {
      if (gamesLoaded) return;
      gamesLoaded = true;
      const response = await fetch("/api/pokedex-lookup/games" + tokenSuffix());
      if (!response.ok) return;
      const { games } = await response.json();
      for (const select of document.querySelectorAll("[data-lookup-games]")) {
        for (const game of games || []) {
          const opt = document.createElement("option");
          opt.value = game.code;
          opt.textContent = game.name;
          select.append(opt);
        }
      }
    }

    openBtn.addEventListener("click", () => {
      lookupScrim.dataset.open = "true";
      ensureLookupGames();
    });

    const form = document.querySelector("[data-lookup-form]");
    const results = document.querySelector("[data-lookup-results]");
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      results.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-muted)]", "Searching…"));
      const params = new URLSearchParams();
      for (const [key, value] of new FormData(form).entries()) {
        if (value) params.set(key, value);
      }
      const viewerToken = new URLSearchParams(location.search).get("token");
      if (viewerToken) params.set("token", viewerToken);
      const response = await fetch("/api/pokedex-lookup?" + params.toString());
      if (!response.ok) {
        results.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-danger)]", await response.text()));
        return;
      }
      const { species } = await response.json();
      if (!species || !species.length) {
        results.replaceChildren(element("p", "col-span-full text-sm text-[var(--color-muted)]", "No matches."));
        return;
      }
      results.replaceChildren(
        ...species.map((sp) => {
          const cell = element(
            "div",
            "cursor-pointer rounded-lg border border-[var(--color-line)] bg-[var(--color-surface)] px-1 pb-1 pt-1.5 text-center transition-colors hover:border-[var(--color-accent)]",
          );
          const image = document.createElement("img");
          image.loading = "lazy";
          image.alt = sp.name;
          image.src = spriteURL(sp.dex, false);
          image.className = "mx-auto block h-[72px] w-[72px] [image-rendering:pixelated]";
          image.addEventListener("error", () => (image.style.visibility = "hidden"));
          cell.append(image);
          cell.append(element("div", "pixel text-[.5rem] text-[var(--color-muted)]", dexNumber(sp.dex)));
          cell.append(element("div", "truncate text-[.72rem] capitalize", sp.name));
          cell.addEventListener("click", () => openSpecies({ dex: sp.dex, name: sp.name }));
          return cell;
        }),
      );
    });
  }
}

export function wireTrainers() {
  const profilesBox = document.querySelector("#userdex-profiles");
  if (!profilesBox) return;
  wireUploadTrigger();

  const totals = document.querySelector("#userdex-totals");
  const trainerSearch = document.querySelector("#trainer-q");
  const trainerFilters = document.querySelector("#trainer-filters");
  let allProfiles = [];
  let trainerGame = "";

  // A note replaces the value it explains, in place and permanently, an icon
  // and the sentence the sidecar wrote. A zero and "this game cannot record it"
  // must never look the same. The sentences come from the sidecar because it is
  // the only component that knows the game.
  function noteEl(text) {
    const note = element("span", "inline-flex items-start gap-1 text-xs text-[var(--color-muted)]");
    note.append(element("span", "shrink-0", "⚠"), element("span", "", text));
    return note;
  }

  // tipEl is a hover/focus popup for a caveat that is true but secondary, like
  // "this game only keeps the last 50 Hall of Fame visits".
  function tipEl(text) {
    const tip = element("span", "tip");
    tip.tabIndex = 0;
    tip.append(
      element("span", "grid h-4 w-4 place-items-center rounded-full border border-[var(--color-line)] text-[.6rem] text-[var(--color-muted)]", "i"),
      element("span", "tip-bubble", text),
    );
    return tip;
  }

  function fieldOrNote(profile, field, render) {
    const note = (profile.notes || {})[field];
    if (note) return noteEl(note);
    return render();
  }

  // Real badge art, one file per badge (internal/web/ui/dist/badges), in the
  // order each region's games award them. The sidecar reports only a count,
  // not which specific badges were earned, so these are shown positionally,
  // the first N of the set for a count of N, which is what every earned
  // gym badge in a normal, forward playthrough actually looks like.
  const BADGE_SETS = {
    Kanto: ["boulder-badge", "cascade-badge", "thunder-badge", "rainbow-badge", "soul-badge", "marsh-badge", "volcano-badge", "earth-badge"],
    Johto: ["zephyr-badge", "hive-badge", "plain-badge", "fog-badge", "storm-badge", "mineral-badge", "glacier-badge", "rising-badge"],
    Hoenn: ["stone-badge", "knuckle-badge", "dynamo-badge", "heat-badge", "balance-badge", "feather-badge", "mind-badge", "rain-badge"],
    Sinnoh: ["coal-badge", "forest-badge", "cobble-badge", "fen-badge", "relic-badge", "mine-badge", "icicle-badge", "beacon-badge"],
    Unova: ["trio-badge", "basic-badge", "insect-badge", "bolt-badge", "quake-badge", "jet-badge", "freeze-badge", "legend-badge"],
    Kalos: ["bug-badge", "cliff-badge", "rumble-badge", "plant-badge", "voltage-badge", "fairy-badge", "psychic-badge", "iceberg-badge"],
    Paldea: ["svbadge-victoryroad-bug", "svbadge-victoryroad-grass", "svbadge-victoryroad-electric", "svbadge-victoryroad-water",
      "svbadge-victoryroad-normal", "svbadge-victoryroad-ghost", "svbadge-victoryroad-psychic", "svbadge-victoryroad-ice"],
  };
  // Galar's 4th and 7th gyms are version-exclusive (Fighting/Ghost, Rock/Ice);
  // the save's own game code says which half of the pair that trainer has.
  function badgeSetFor(profile) {
    if (profile.region === "Galar") {
      const fourth = profile.game === "SH" ? "ghost-badge" : "fighting-badge";
      const seventh = profile.game === "SH" ? "ice-badge" : "rock-badge";
      return ["grass-badge", "water-badge", "fire-badge", fourth, "galarfairy-badge", seventh, "dark-badge", "dragon-badge"];
    }
    return BADGE_SETS[profile.region] || null;
  }
  function badgeRow(profile) {
    const set = badgeSetFor(profile);
    if (!set) return null;
    const row = element("div", "flex flex-wrap gap-1.5");
    row.setAttribute("role", "img");
    row.setAttribute("aria-label", `${profile.progress_count} of ${profile.progress_max} badges`);
    set.forEach((slug, i) => {
      const earned = i < profile.progress_count;
      const img = document.createElement("img");
      img.src = `/assets/badges/${slug}.png`;
      img.width = 22;
      img.height = 22;
      img.alt = "";
      img.className = "h-[22px] w-[22px] shrink-0 object-contain" + (earned ? "" : " opacity-25 grayscale");
      img.addEventListener("error", () => (img.style.visibility = "hidden"));
      row.append(img);
    });
    return row;
  }

  function stat(label, value) {
    const box = element("div", "rounded-lg border border-[var(--color-line)] bg-[var(--color-surface-2)] px-3 py-1.5");
    box.append(
      element("div", "eyebrow eyebrow--muted", label),
      typeof value === "string" ? element("div", "text-sm font-semibold", value) : value,
    );
    return box;
  }

  async function loadTrainers() {
    const response = await fetch("/api/userdex" + tokenSuffix());
    if (!response.ok) {
      profilesBox.replaceChildren(element("p", "text-sm text-[var(--color-muted)]", await response.text()));
      return;
    }
    const { profiles, totals: sums } = await response.json();
    allProfiles = profiles || [];

    totals.replaceChildren(
      stat("Total Pokedollars On Hand", formatMoney(sums?.money)),
      stat("Total Playtime", formatMinutes(sums?.played_minutes)),
      stat("Hall of Fame clears", String(sums?.clears ?? 0)),
    );

    renderTrainerFilters();
    renderTrainers();
  }

  function renderTrainerFilters() {
    trainerFilters.replaceChildren();
    const games = [];
    for (const profile of allProfiles) {
      const key = profile.game;
      if (key && !games.some((g) => g.code === key)) games.push({ code: key, name: profile.game_name || key });
    }
    if (games.length < 2) return;
    trainerFilters.append(
      pill("All games", !trainerGame, () => {
        trainerGame = "";
        renderTrainerFilters();
        renderTrainers();
      }),
    );
    for (const game of games) {
      trainerFilters.append(
        pill(game.name, trainerGame === game.code, () => {
          trainerGame = game.code;
          renderTrainerFilters();
          renderTrainers();
        }),
      );
    }
  }

  function renderTrainers() {
    const needle = (trainerSearch?.value || "").trim().toLowerCase();
    const shown = allProfiles.filter((profile) => {
      if (trainerGame && profile.game !== trainerGame) return false;
      if (!needle) return true;
      return `${profile.ot || ""} ${profile.game_name || ""} ${profile.game || ""}`.toLowerCase().includes(needle);
    });

    if (!shown.length) {
      profilesBox.replaceChildren(
        element(
          "div",
          "rounded-lg border border-dashed border-[var(--color-line)] p-7 text-center text-[var(--color-muted)] md:col-span-2",
          allProfiles.length
            ? "No playthroughs match that filter."
            : "No playthroughs yet. Upload a Pokémon save above and its trainer appears here.",
        ),
      );
      return;
    }
    profilesBox.replaceChildren(...shown.map(trainerCard));
  }

  function trainerCard(profile) {
    const box = element("div", "card p-4");

    const head = element("div", "flex flex-wrap items-baseline gap-2");
    head.append(element("h2", "m-0 text-base", profile.ot || "Unknown trainer"));
    head.append(element("span", "pixel text-[.55rem] text-[var(--color-faint)]", "TID " + String(profile.tid).padStart(5, "0")));
    head.append(element("span", "badge", profile.game_name || profile.game));
    if (!profile.trusted) head.append(element("span", "badge", "identity only"));
    box.append(head);

    const facts = element("div", "mt-3 grid gap-2");

    // Progress: badges for gens 1-6, trials for Alola, under its own name, with
    // the sidecar's note shown inline beside it, not instead of it.
    const progressRow = element("div", "flex flex-wrap items-center gap-2");
    if (profile.progress_kind) {
      const label = profile.progress_kind === "trials" ? "Trials" : "Badges";
      const icons = profile.progress_kind === "badges" ? badgeRow(profile) : null;
      if (icons) {
        progressRow.append(icons, element("span", "text-xs text-[var(--color-faint)]", `${profile.progress_count}/${profile.progress_max}`));
      } else {
        progressRow.append(element("span", "badge badge--accent", `${label} ${profile.progress_count}/${profile.progress_max}`));
      }
      const note = (profile.notes || {}).progress;
      if (note) progressRow.append(noteEl(note));
    } else {
      progressRow.append(fieldOrNote(profile, "trainer", () => element("span", "text-xs text-[var(--color-muted)]", "No progress recorded.")));
    }
    facts.append(progressRow);

    const numbers = element("div", "flex flex-wrap gap-2 text-sm");
    if (profile.trusted) {
      numbers.append(element("span", "", formatMoney(profile.money) + " on hand"));
      numbers.append(element("span", "text-[var(--color-muted)]", formatMinutes(profile.played_minutes) + " played"));
      if (profile.dex_caught > 0) numbers.append(element("span", "text-[var(--color-muted)]", profile.dex_caught + " Pokémon caught this run"));
    } else {
      numbers.append(fieldOrNote(profile, "trainer", () => element("span", "", "")));
    }
    facts.append(numbers);

    // Times beaten: null is "this game does not count", which is not zero, it
    // renders as its note, never as 0. A caveat on a real count (gen 2's
    // last-50 window) goes behind a hover tip instead of shouting inline.
    const clearsRow = element("div", "flex flex-wrap items-center gap-2 text-sm");
    if (profile.hof_clears === null || profile.hof_clears === undefined) {
      const note = (profile.notes || {})["hall_of_fame_clears"] || (profile.notes || {})["hall_of_fame"];
      if (note) clearsRow.append(noteEl(note));
      else if (profile.trusted) clearsRow.append(element("span", "text-xs text-[var(--color-muted)]", "No Hall of Fame recorded yet."));
    } else {
      clearsRow.append(element("span", "font-semibold", `Hall of Fame clears: ${profile.hof_clears}`));
      const note = (profile.notes || {})["hall_of_fame_clears"];
      if (note) clearsRow.append(tipEl(note));
    }
    facts.append(clearsRow);

    // The latest Elite Four team, as sprites. Entry 0 is the most recent visit.
    const hof = (profile.hof || []).filter((e) => e.entry_index === 0);
    if (hof.length > 0) {
      const team = element("div", "mt-1 flex flex-wrap items-end gap-2");
      for (const member of hof) {
        const cell = element("span", "inline-flex flex-col items-center");
        const img = document.createElement("img");
        img.loading = "lazy";
        img.width = 48;
        img.height = 48;
        img.className = "h-12 w-12 [image-rendering:pixelated]";
        img.src = spriteURL(member.species);
        img.alt = "#" + member.species;
        img.addEventListener("error", () => (img.style.visibility = "hidden"));
        cell.append(img);
        if (member.level > 0) cell.append(element("span", "text-[.65rem] text-[var(--color-muted)]", "Lv " + member.level));
        team.append(cell);
      }
      facts.append(element("div", "mt-1 eyebrow eyebrow--muted", "Latest Hall of Fame team"), team);
    }

    box.append(facts);
    return box;
  }

  // Debounced: filtering re-renders every visible trainer card, which is
  // wasted work on every keystroke of a fast typist.
  let searchDebounce;
  trainerSearch?.addEventListener("input", () => {
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(renderTrainers, 180);
  });

  // A save the parser read changes the roster, so refresh it.
  wireUploadPanel((result) => {
    if (result.parsed) loadTrainers();
  });
  loadTrainers();
}
