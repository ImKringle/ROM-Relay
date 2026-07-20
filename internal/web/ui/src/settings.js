// Settings: submits the allowlisted config.ini fields. Current values are
// server-rendered into the form already, so there is no GET fetch here.
export function wireSettings() {
  const form = document.querySelector("#settings-form");
  if (!form) return;
  const csrf = document.querySelector("meta[name=csrf-token]")?.content ?? "";
  const out = document.querySelector("#settings-result");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    out.textContent = "Saving…";
    const response = await fetch("/api/admin/settings", {
      method: "POST",
      headers: { "X-CSRF-Token": csrf },
      body: new FormData(form),
    });
    if (!response.ok) {
      out.textContent = await response.text();
      return;
    }
    const { detail } = await response.json();
    out.textContent = detail || "Saved.";
  });
}
