// Type Chart: click a defending type's badge to see just its matchups, click
// a second to combine them (a real Pokémon has up to two types), everything
// that doesn't apply dims out instead of making you scan all 18 columns.
const CLASS_MULT = { mx0: 0, mx25: 0.25, mx50: 0.5, mx100: 1, mx200: 2, mx400: 4 };

function multClass(mult) {
  if (mult === 0) return "mx0";
  if (mult === 0.25) return "mx25";
  if (mult === 0.5) return "mx50";
  if (mult === 2) return "mx200";
  if (mult === 4) return "mx400";
  return "mx100";
}

function multLabel(mult) {
  return { mx0: "0×", mx25: "¼×", mx50: "½×", mx100: "1×", mx200: "2×", mx400: "4×" }[multClass(mult)];
}

function cap(s) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function wireTypeChart() {
  const table = document.querySelector("#typechart-table");
  if (!table) return;

  const rows = [...table.querySelectorAll("tbody tr[data-type]")];
  const attackTypes = [...table.querySelectorAll("thead th[data-type]")].map((th) => th.dataset.type);
  const selected = [];

  const summary = document.createElement("div");
  summary.className = "typechart-summary";
  summary.hidden = true;
  table.parentElement.before(summary);

  // rowMultipliers reads the already-classified cells for one defending
  // type's row (see classifyMultiplier in pokemon.go) rather than
  // recomputing anything, the server already did the float math once.
  function rowMultipliers(type) {
    const row = rows.find((r) => r.dataset.type === type);
    const cells = [...row.querySelectorAll("td")];
    return cells.map((cell) => {
      const cls = [...cell.classList].find((c) => c in CLASS_MULT);
      return cls ? CLASS_MULT[cls] : 1;
    });
  }

  function render() {
    table.classList.toggle("typechart-filtered", selected.length > 0);
    table.querySelectorAll("[data-type]").forEach((el) => {
      el.classList.toggle("typechart-selected", selected.includes(el.dataset.type));
    });

    if (!selected.length) {
      summary.hidden = true;
      table.querySelectorAll(".typechart-dim").forEach((el) => el.classList.remove("typechart-dim"));
      return;
    }

    const per = selected.map(rowMultipliers);
    const combined = attackTypes.map((_, i) => per.reduce((acc, m) => acc * m[i], 1));

    rows.forEach((row) => {
      const on = selected.includes(row.dataset.type);
      row.querySelectorAll("td, th").forEach((el) => el.classList.toggle("typechart-dim", !on));
    });

    summary.hidden = false;
    summary.replaceChildren();
    const label = document.createElement("strong");
    label.textContent = `${selected.map(cap).join(" / ")} takes:`;
    summary.append(label);
    attackTypes.forEach((type, i) => {
      const mult = combined[i];
      if (mult === 1) return;
      const chip = document.createElement("span");
      chip.className = `typechart-summary-chip typechart-cell ${multClass(mult)}`;
      chip.textContent = `${cap(type)} ${multLabel(mult)}`;
      summary.append(chip);
    });
  }

  // Only the body's row headers (defending types) are selectable, the
  // column headers across the top stay static labels, since "what am I"
  // (defending) is the question this filter answers, not "what am I facing."
  table.addEventListener("click", (event) => {
    const target = event.target.closest("tbody [data-type]");
    if (!target) return;
    const type = target.dataset.type;
    const i = selected.indexOf(type);
    if (i >= 0) selected.splice(i, 1);
    else {
      selected.push(type);
      if (selected.length > 2) selected.shift();
    }
    render();
  });
}
