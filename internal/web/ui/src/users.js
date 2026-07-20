// The account management screen, on the admin user API.
//
// Every control here is a request the server may refuse on a rule the page
// cannot check for itself (the last admin, your own account). So nothing is
// applied optimistically: each action reports what the server said and reloads
// the list from it.
import { element } from "./util.js";

export function wireUsers() {
  const page = document.querySelector("[data-users]");
  if (!page) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const list = document.querySelector("#users-list");
  const form = document.querySelector("#create-user-form");
  const formError = document.querySelector("#create-user-error");

  // send performs one action and returns the parsed body, or null once it has
  // reported the refusal. The API answers errors as plain text, and those texts
  // are written for the admin ("This is the last admin account…"), so they are
  // shown as-is rather than replaced with a generic failure.
  //
  // onError lets a caller put the message somewhere better than an alert; the
  // row controls have nowhere of their own, so they fall back to one.
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

  // showPassword surfaces a one-time password. It scrolls into view because it
  // cannot be recovered: an admin who misses it has to reset the account again.
  function showPassword(title, detail, password) {
    const panel = document.querySelector("#temp-password");
    document.querySelector("#temp-password-title").textContent = title;
    document.querySelector("#temp-password-detail").textContent = detail || "";
    document.querySelector("#temp-password-value").textContent = password;
    panel.classList.remove("hidden");
    panel.scrollIntoView({ behavior: "smooth", block: "center" });
  }

  document.querySelector("[data-dismiss-password]").addEventListener("click", () => {
    document.querySelector("#temp-password").classList.add("hidden");
    document.querySelector("#temp-password-value").textContent = "";
  });

  document.querySelector("[data-copy-password]").addEventListener("click", async (event) => {
    const password = document.querySelector("#temp-password-value").textContent;
    try {
      await navigator.clipboard.writeText(password);
      event.target.textContent = "Copied";
      setTimeout(() => (event.target.textContent = "Copy"), 1500);
    } catch {
      // Clipboard access is refused outside a secure context, and this relay is
      // usually plain http on a LAN. Select the text and try the older
      // execCommand copy; only fall back to "select it yourself" if that also
      // fails, rather than claiming a copy that never happened.
      const range = document.createRange();
      range.selectNodeContents(document.querySelector("#temp-password-value"));
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
    // A rejected username ("that username is taken") belongs against the field
    // that caused it, where the admin is already looking.
    const created = await send("/api/admin/users", { method: "POST", body }, (message) => {
      formError.textContent = message;
      formError.classList.remove("hidden");
    });
    if (!created) return;
    showPassword(`Password for ${created.username}`, created.detail, created.temporary_password);
    form.reset();
    load();
  });

  async function load() {
    const response = await fetch("/api/admin/users");
    if (!response.ok) {
      list.replaceChildren(element("p", "text-sm text-[var(--color-danger)]", await response.text()));
      return;
    }
    const { users } = await response.json();
    list.replaceChildren(...(users || []).map(row));
  }

  function row(user) {
    const card = element("div", "surface flex flex-wrap items-center gap-4 p-4");

    const meta = element("div", "min-w-0 flex-1");
    const name = element("div", "flex flex-wrap items-center gap-2");
    name.append(element("strong", "text-base", user.username));
    if (user.role === "admin") name.append(element("span", "badge badge--accent", "Admin"));
    if (user.disabled) name.append(element("span", "badge", "Disabled"));
    if (user.must_change_password) name.append(element("span", "badge", "Must change password"));

    // What this account owns is shown next to the controls that would destroy it.
    const owns = `${user.saves} save${user.saves === 1 ? "" : "s"} · ${user.pokemon} Pokémon`;
    meta.append(name, element("div", "mt-1 text-xs text-[var(--color-muted)]", `Created ${user.created_at} · ${owns}`));

    // An account with nothing stored should not be described as losing "0 saves":
    // the warning is worth reading only when it names something real.
    const destroys = user.saves || user.pokemon ? ` This also destroys their ${owns}.` : "";

    const actions = element("div", "flex flex-wrap items-center gap-2");

    const role = element("select", "field w-auto");
    role.append(new Option("User", "user", false, user.role === "user"));
    role.append(new Option("Admin", "admin", false, user.role === "admin"));
    role.setAttribute("aria-label", `Role for ${user.username}`);
    role.addEventListener("change", async () => {
      const body = new URLSearchParams({ role: role.value });
      // A refused change must not leave the select showing a role the server
      // never applied, so the list is reloaded either way.
      await send(`/api/admin/users/${encodeURIComponent(user.id)}/role`, { method: "POST", body });
      load();
    });

    const access = element("button", "button", user.disabled ? "Enable" : "Disable");
    access.type = "button";
    access.addEventListener("click", async () => {
      const body = new URLSearchParams({ disabled: String(!user.disabled) });
      if (await send(`/api/admin/users/${encodeURIComponent(user.id)}/disabled`, { method: "POST", body })) load();
    });

    const reset = element("button", "button", "Reset password");
    reset.type = "button";
    reset.addEventListener("click", async () => {
      if (!confirm(`Issue a new temporary password for ${user.username}? Their current password stops working and their sessions end.`)) return;
      const done = await send(`/api/admin/users/${encodeURIComponent(user.id)}/reset-password`, { method: "POST" });
      if (!done) return;
      showPassword(`New password for ${done.username}`, done.detail, done.temporary_password);
      load();
    });

    const clearDex = element("button", "button", "Clear Pokédex");
    clearDex.type = "button";
    clearDex.addEventListener("click", async () => {
      // Saves stay; only the derived Pokédex data goes, and re-uploading a save
      // brings it back. Still destructive enough to name what happens.
      if (!confirm(`Clear ${user.username}'s Pokédex? Their ${user.pokemon} stored Pokémon, dex progress, and trainers are removed. Their saves are kept, and re-uploading a save rebuilds the Pokédex.`)) return;
      if (await send(`/api/admin/users/${encodeURIComponent(user.id)}/clear-dex`, { method: "POST" })) load();
    });

    const remove = element("button", "button button--danger", "Delete");
    remove.type = "button";
    remove.addEventListener("click", async () => {
      // Deleting takes the saves with it, so the prompt names what goes rather
      // than asking whether the admin is sure in the abstract.
      if (!confirm(`Delete ${user.username}?${destroys} This cannot be undone.`)) return;
      if (await send(`/api/admin/users/${encodeURIComponent(user.id)}`, { method: "DELETE" })) load();
    });

    actions.append(role, access, reset, clearDex, remove);
    card.append(meta, actions);
    return card;
  }

  load();
}
