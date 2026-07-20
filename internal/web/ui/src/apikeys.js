// The API key management screen, on the admin API-keys API.
//
// Mirrors users.js: nothing is applied optimistically, every action reports
// what the server said and reloads the list from it.
import { element } from "./util.js";

export function wireAPIKeys() {
  const page = document.querySelector("[data-apikeys]");
  if (!page) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const list = document.querySelector("#keys-list");
  const form = document.querySelector("#create-key-form");
  const formError = document.querySelector("#create-key-error");

  async function send(url, options = {}, onError) {
    const response = await fetch(url, { headers: { "X-CSRF-Token": csrf }, ...options });
    const text = await response.text();
    if (!response.ok) {
      const message = text.trim() || `That did not work (${response.status}).`;
      if (onError) onError(message);
      else alert(message);
      return null;
    }
    try {
      return JSON.parse(text);
    } catch {
      return {};
    }
  }

  // showToken surfaces a one-time key. It scrolls into view because it cannot
  // be recovered: an admin who misses it has to revoke the key and make a new one.
  function showToken(title, detail, token) {
    const panel = document.querySelector("#new-key");
    document.querySelector("#new-key-title").textContent = title;
    document.querySelector("#new-key-detail").textContent = detail || "";
    document.querySelector("#new-key-value").textContent = token;
    panel.classList.remove("hidden");
    panel.scrollIntoView({ behavior: "smooth", block: "center" });
  }

  document.querySelector("[data-dismiss-key]").addEventListener("click", () => {
    document.querySelector("#new-key").classList.add("hidden");
    document.querySelector("#new-key-value").textContent = "";
  });

  document.querySelector("[data-copy-key]").addEventListener("click", async (event) => {
    const token = document.querySelector("#new-key-value").textContent;
    try {
      await navigator.clipboard.writeText(token);
      event.target.textContent = "Copied";
      setTimeout(() => (event.target.textContent = "Copy"), 1500);
    } catch {
      const range = document.createRange();
      range.selectNodeContents(document.querySelector("#new-key-value"));
      getSelection().removeAllRanges();
      getSelection().addRange(range);
      const copied = document.execCommand("copy");
      event.target.textContent = copied ? "Copied" : "Select and copy";
      if (copied) setTimeout(() => (event.target.textContent = "Copy"), 1500);
    }
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    formError.classList.add("hidden");
    const body = new FormData(form);
    const created = await send("/api/admin/api-keys", { method: "POST", body }, (message) => {
      formError.textContent = message;
      formError.classList.remove("hidden");
    });
    if (!created) return;
    showToken(`Key created: ${created.name}`, created.detail, created.token);
    form.reset();
    load();
  });

  async function load() {
    const response = await fetch("/api/admin/api-keys");
    if (!response.ok) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", await response.text()));
      return;
    }
    const { keys } = await response.json();
    list.replaceChildren(
      ...(keys && keys.length ? keys.map(row) : [element("p", "text-sm text-[var(--color-muted)]", "No API keys yet.")]),
    );
  }

  function row(key) {
    const card = element("div", "surface flex flex-wrap items-center gap-4 p-4");

    const meta = element("div", "min-w-0 flex-1");
    const name = element("div", "flex flex-wrap items-center gap-2");
    name.append(element("strong", "text-base", key.name));
    if (key.revoked) name.append(element("span", "badge", "Revoked"));

    const lastUsed = key.last_used_at ? `Last used ${key.last_used_at}` : "Never used";
    meta.append(name, element("div", "mt-1 text-xs text-[var(--color-muted)]", `Created ${key.created_at} by ${key.created_by_username} · ${lastUsed}`));

    const actions = element("div", "flex flex-wrap items-center gap-2");
    if (!key.revoked) {
      const revoke = element("button", "button button--danger", "Revoke");
      revoke.type = "button";
      revoke.addEventListener("click", async () => {
        if (!confirm(`Revoke "${key.name}"? Anything using this key stops working immediately.`)) return;
        if (await send(`/api/admin/api-keys/${encodeURIComponent(key.id)}`, { method: "DELETE" })) load();
      });
      actions.append(revoke);
    }

    card.append(meta, actions);
    return card;
  }

  load();
}
