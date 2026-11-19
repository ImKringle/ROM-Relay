# Frontend rewrite: React + Vite + TypeScript

Decision (2026-11-19): replace the html/template + htmx UI with a React SPA in
one release. Go stays the backend; the built SPA is embedded in the binary, so
the Docker image needs no Node at runtime.

## Layout

- `web/`: the Vite + React + TS app (author-time only, like the old Tailwind build).
- `web` builds into `internal/web/spa/dist/`, which is committed and embedded via `go:embed`.
- Go serves `index.html` for every human page route (after the existing auth
  gate) and `/app-assets/*` for hashed bundles. Everything else is JSON under `/api/`.

## Security (must not regress)

- Same access model as `security()` in `internal/web/auth.go`: public paths
  (login, assets, sprites), machine paths (viewer token or session), human
  routes (session only, with the open-LAN fallback when no viewer token is set),
  admin routes (`adminOK`), the forced password change, WebDAV's separate gate.
- The CSP stays `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'`.
  Vite output has no inline script; React sets text via JSX (escaped).
- NEW hardening: one middleware enforces `X-CSRF-Token` on every cookie-authenticated
  unsafe request (POST/PUT/PATCH/DELETE) plus a same-origin `Origin`/`Sec-Fetch-Site`
  check. Bearer API keys stay exempt. Today several mutations rely on SameSite=Lax only.
- Login and logout move to JSON (`POST /api/session`, `DELETE /api/session`), keeping
  `/login` as the SPA page. Rate limits and validation stay in Go.
- The RomM token, qBittorrent password, and other secrets never appear in API responses.

## API additions (pages that were server-rendered)

`GET /api/session` (user, csrf, tools, theme), `GET /api/library` (games + facets),
`GET /api/library/{id}`, `GET /api/romm/library`, `GET /api/romm/games/{id}`,
`GET /api/admin/overview`, `GET /api/admin/roms`, `GET /api/admin/settings`,
`GET /api/admin/users`... (existing JSON endpoints are reused as-is).

## Pages

Library (ROM Relay / RomM switch), Game, RomM game, Saves, Pokédex, Trainers,
PokeHome, Editor, Type chart, Randomizer, Patcher, Torrents, Account (password,
WebDAV keys), Admin hub, Admin ROMs, Admin saves, Admin settings, Users, API keys,
Torrent sources, Patch workspace, Login, Setup.

## Design

Retro, game-y: blue/black palette (bg #080b12, surfaces #111827/#172033,
accent cyan #67e8f9/#22d3ee), the Press Start 2P display face for headings
(self-hosted), CRT-scanline hero, pixel borders, chunky buttons. The corn theme
is dropped.

## Status (2026-11-19): shipped

All pages are ported; the html/template + htmx UI, its templates, scripts and
Tailwind build are deleted. `internal/web/ui` now only embeds static images
and fonts. Build the UI with `npm run build` in `web/` (output is committed).

Hardening landed with it: `forgeryOK` (origin + CSRF on every cookie-backed
mutation), sign-in throttling (`loginThrottle`), admin pages gated before the
shell is served, settings read through an explicit allowlist.

Known leftovers: the "corn" theme no longer affects the UI (`ui_theme` only
changes a few server error strings); tools for disabled features still show in
the nav and explain themselves on open; `cmd/romrelay`'s
TestConvertV2ConvertsQueueConcurrently fails on machines without the 3DS keys.
