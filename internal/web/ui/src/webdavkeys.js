// The self-service WebDAV key management screen.
//
// Mirrors apikeys.js: nothing is applied optimistically, every action reports
// what the server said and reloads the list from it.
import { element } from "./util.js";

export function wireWebDAVKeys() {
  const page = document.querySelector("[data-webdavkeys]");
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

  async function copyText(text, button) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const scratch = element("textarea");
      scratch.value = text;
      scratch.style.position = "fixed";
      scratch.style.opacity = "0";
      document.body.append(scratch);
      scratch.select();
      document.execCommand("copy");
      scratch.remove();
    }
    button.textContent = "Copied";
    setTimeout(() => (button.textContent = "Copy"), 1500);
  }

  // showToken surfaces a one-time key. It scrolls into view because it cannot
  // be recovered: missing it means revoking the key and making a new one.
  function showToken(title, detail, token, url) {
    const panel = document.querySelector("#new-key");
    document.querySelector("#new-key-title").textContent = title;
    document.querySelector("#new-key-detail").textContent = detail || "";
    document.querySelector("#new-key-value").textContent = token;
    document.querySelector("#new-key-url").textContent = url;
    panel.classList.remove("hidden");
    panel.scrollIntoView({ behavior: "smooth", block: "center" });
  }

  document.querySelector("[data-dismiss-key]").addEventListener("click", () => {
    document.querySelector("#new-key").classList.add("hidden");
    document.querySelector("#new-key-value").textContent = "";
  });

  document.querySelector("[data-copy-key]").addEventListener("click", (event) => {
    copyText(document.querySelector("#new-key-value").textContent, event.target);
  });
  document.querySelector("[data-copy-url]").addEventListener("click", (event) => {
    copyText(document.querySelector("#new-key-url").textContent, event.target);
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    formError.classList.add("hidden");
    const body = new FormData(form);
    const created = await send("/api/account/webdav-keys", { method: "POST", body }, (message) => {
      formError.textContent = message;
      formError.classList.remove("hidden");
    });
    if (!created) return;
    showToken(created.label ? `Key created: ${created.label}` : "Key created", created.detail, created.token, created.url);
    form.reset();
    load();
  });

  async function load() {
    const response = await fetch("/api/account/webdav-keys");
    if (!response.ok) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", await response.text()));
      return;
    }
    const { keys } = await response.json();
    list.replaceChildren(
      ...(keys && keys.length ? keys.map(row) : [element("p", "text-sm text-[var(--color-muted)]", "No WebDAV keys yet.")]),
    );
  }

  function row(key) {
    const card = element("div", "surface flex flex-wrap items-center gap-4 p-4");

    const meta = element("div", "min-w-0 flex-1");
    const name = element("div", "flex flex-wrap items-center gap-2");
    name.append(element("strong", "text-base", key.label || "(untitled)"));
    if (key.revoked) name.append(element("span", "badge", "Revoked"));

    const lastUsed = key.last_used_at ? `Last used ${key.last_used_at}` : "Never used";
    meta.append(name, element("div", "mt-1 text-xs text-[var(--color-muted)]", `Created ${key.created_at} · ${lastUsed}`));

    const actions = element("div", "flex flex-wrap items-center gap-2");
    if (!key.revoked) {
      const revoke = element("button", "button button--danger", "Revoke");
      revoke.type = "button";
      revoke.addEventListener("click", async () => {
        if (!confirm(`Revoke "${key.label || "this key"}"? Any device using it stops working immediately.`)) return;
        if (await send(`/api/account/webdav-keys/${encodeURIComponent(key.id)}`, { method: "DELETE" })) load();
      });
      actions.append(revoke);
    }

    card.append(meta, actions);
    return card;
  }

  load();
}
