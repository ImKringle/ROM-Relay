// The first-boot page.
//
// It is shown before the catalog exists, so it cannot link anywhere useful yet.
// Instead it watches readiness and reloads itself the moment the catalog is
// browsable, otherwise a fresh install looks stuck until someone refreshes.
import { tokenSuffix } from "./util.js";

export function wireSetup() {
  const page = document.querySelector("[data-setup]");
  if (!page) return;
  const stage = page.querySelector("[data-setup-stage]");

  async function poll() {
    try {
      const response = await fetch("/healthz" + tokenSuffix());
      const health = await response.json();
      if (health.ready) {
        location.reload();
        return;
      }
      stage.textContent = health.setup_stage || "Preparing catalog";
    } catch {
      // Mid-restart the endpoint may be briefly unreachable; the next poll is a
      // second away, and reporting it would only add noise to a working boot.
    }
    setTimeout(poll, 1000);
  }
  poll();
}
