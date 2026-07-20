# ROM Relay API reference

ROM Relay's UI is a thin client over this same API; nothing here is a separate integration surface bolted on afterward. Every route below is reachable two ways:

- **Browser session**, the cookie set by `POST /login`.
- **API key**, `Authorization: Bearer <token>`, created from **Admin → API
  Keys** by a signed-in admin. See [CONFIGURATION.md#api-access](CONFIGURATION.md#api-access).

Auth column:

- **public**, no auth.
- **machine**, `/d/`, `/files/`, `/queue`, `/healthz` only. Accepts the viewer token (`REMOTE_ACCESS_TOKEN`) as `?token=`, for devices that can't hold a session or a bearer header (a 3DS scanning a QR code, FBI's port-5000 receiver), or a session/API key like everything else.
- **user**, any signed-in account (session or API key). If no `REMOTE_ACCESS_TOKEN` is configured at all, the LAN is treated as trusted and these routes stay open without one.
- **admin**, an admin session, the legacy admin token, or an API key (which is always admin-scoped).

## Auth

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /login` | public | Sign in, sets the session cookie. |
| `POST /logout`, `GET /logout` | public | End the session. |
| `GET /api/me` | public | Current identity, `{authenticated, username, role, admin}`. |
| `GET /account/password`, `POST /account/password` | user | Change your own password. |

## Catalog & downloads

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /`, `GET /games/{id}` | user | Catalog and game detail pages. |
| `GET /api/games` | user | Catalog as JSON. |
| `GET /healthz` | machine | Live background-work state, polled by the UI. |
| `GET`/`HEAD /d/{id}` | machine | Direct file download. |
| `GET /download/{id}` | user | Download-choice page (encrypted/unencrypted, mobile/desktop). |
| `GET /qr/{file}`, `GET /qr/fbi/{batch}` | user | QR codes for a download or an FBI batch. |
| `GET /api/fbi/batches` | user | Ordered FBI batch listing. |
| `GET /media/{id}/cover` | user | Cover art. |
| `POST /queue` | machine | Push a selection to FBI's port-5000 receiver. |
| `GET /saves`, `GET /api/saves` | user | Your saves, page and JSON. |
| `GET /saves/{id}/download` | user | Download one of your saves. |
| `DELETE /api/saves/{id}` | user | Delete a save. |
| `POST /api/saves/{id}/game-override` | user | Correct which game a save belongs to. |
| `GET`/`POST /api/saves/{id}/bag` | user | Read or edit a save's item bag. |
| `POST /api/saves/upload` | user | Upload a save. |

## Conversion & crypto tools

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /api/upload/convert`, `GET /api/upload/convert/{id}` | user | Upload a `.3ds`/`.cci` for CIA conversion; poll the job. |
| `POST /api/upload/crypt`, `GET /api/upload/crypt/{id}` | user | Upload a CIA to encrypt/decrypt; poll the job. |
| `POST /api/library/convert/{id}`, `POST /api/library/crypt/{id}` | user | Run conversion/crypt against a catalog artifact instead of an upload. |
| `POST /api/library/upload` | user | Add a file to the library directly. |
| `POST /api/ftp/browse`, `POST /api/ftp/send` | user | Browse and push to a homebrew FTP server (e.g. ftpd). |

## Patching (public workspace)

"Public" names the workspace, not the auth tier, it's the one open to any
signed-in user rather than admins only.

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /patches` | user | Public patch workspace page. |
| `GET /api/patch-files` | user | Available patch recipes. |
| `POST /api/patch-jobs`, `GET /api/patch-jobs/{id}` | user | Start and poll a patch job. Results expire in 24h unless an admin retains them. |

## Pokédex & Pokémon

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /pokedex`, `GET /pokedex/types`, `GET /trainers` | user | Pokédex, type chart, and Trainers pages. |
| `GET /api/dex`, `GET /api/dex/species/{dex}` | user | Living Dex and species detail. |
| `GET /api/pokedex-lookup`, `GET /api/pokedex-lookup/games` | user | Cross-game Pokédex lookup. |
| `GET /api/userdex`, `GET /api/userdex/{id}/dex` | user | Per-trainer dex views. |
| `GET /editor` | user | Standalone Pokémon editor page. |
| `GET /api/pokemon` | user | List your stored Pokémon. |
| `POST /api/pokemon` | user | Create a Pokémon from scratch. |
| `POST /api/pokemon/{id}/edit` | user | Edit a stored Pokémon. |
| `DELETE /api/pokemon/{id}` | user | Delete a stored Pokémon. |
| `GET /api/pokemon/{id}/download` | user | Download a `.pkm` file. |
| `GET /api/reference` | user | Species/item/move/ability reference data for the editor. |

## PokeHome (box storage)

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /pokehome` | user | PokeHome page. |
| `GET /api/pokehome` | user | List deposited Pokémon. |
| `GET /api/pokehome/boxes` | user | Box grid for a save. |
| `POST /api/pokehome/{id}/deposit`, `POST /api/pokehome/{id}/withdraw` | user | Move a Pokémon into or out of PokeHome. |

## Admin

Every admin route also requires CSRF (`X-CSRF-Token` header or
`csrf_token` form field) on mutations, except when authenticated with an API
key. A bearer token is never attached automatically by a browser, so there is
nothing for a forged cross-site request to ride.

| Method & path | Purpose |
|---|---|
| `GET /admin` | Admin hub. |
| `GET /admin/roms`, `DELETE /api/admin/roms/{id}` | Browse and remove catalog files. |
| `GET /admin/saves`, `GET /api/admin/users/{id}/saves` | A user's saves. |
| `GET /admin/users/{id}/saves/{sid}/download`, `DELETE /api/admin/users/{id}/saves/{sid}` | Download or delete one save. |
| `GET /api/admin/users/{id}/dex`, `GET /api/admin/users/{id}/pokehome`, `DELETE /api/admin/users/{id}/pokehome/{pid}` | A user's dex/PokeHome data. |
| `GET /admin/settings`, `POST /api/admin/settings` | Non-secret configuration. |
| `GET /admin/api-keys`, `GET`/`POST /api/admin/api-keys`, `DELETE /api/admin/api-keys/{id}` | This page: list, create, revoke API keys. |
| `GET /admin/patches`, `GET /api/admin/patch-files`, `GET /api/admin/games` | Patch workspace and catalog for admin use. |
| `POST /api/admin/patch-jobs`, `GET .../{id}`, `DELETE .../{id}`, `POST .../{id}/publish`, `POST .../{id}/retain` | Private patch job lifecycle. |
| `POST /api/admin/games/{id}/metadata` | Edit game metadata/artwork. |
| `POST /api/admin/decrypt/{id}`, `POST /api/admin/convert/{id}` | Prepare unencrypted CIA / convert `.3ds`-`.cci`. |
| `GET /api/admin/remote-search`, `POST /api/admin/remote-jobs`, `GET .../{id}`, `DELETE .../{id}` | Remote SFTP search and fetch. |
| `POST /api/admin/shutdown` | Clean shutdown. |
| `GET /admin/users`, `GET`/`POST /api/admin/users` | List and create accounts. |
| `POST /api/admin/users/{id}/role`, `.../disabled`, `.../reset-password` | Change role, access, or password. |
| `POST /api/admin/users/{id}/clear-dex`, `.../clear-trainers` | Wipe a user's derived dex/trainer data. |
| `POST /api/admin/dex/prune` | Remove Pokémon whose source save is gone, across all users. |
| `DELETE /api/admin/users/{id}` | Delete an account. |
