# ROM Relay configuration

ROM Relay reads two files once during startup, both in its config directory (`config.docker.ini` and `.env` at the repo root in the Docker deployment):

- `config.ini`, non-secret settings, grouped in `[section]` blocks. Copy `config.example.ini` to `config.ini` and edit. Every key shows its default; delete a line to accept the default.
- `.env`, secrets (tokens, passwords, IGDB credentials). Copy `.env.example` to `.env`. Never commit it. Values already present in the process environment (for example Docker `--env-file .env`) override the file, so a container needs no on-disk `.env`.

Changes, including `ui_theme`, require a restart. Relative paths in `config.ini` resolve from the file's directory. A different directory can be pointed at with the `-config-dir <path>` flag.

Secrets live only in `.env`:

| Environment variable | Purpose |
|---|---|
| `REMOTE_ACCESS_TOKEN` | Public catalog viewer token (min 24 chars). |
| `ADMIN_ACCESS_TOKEN` | Admin/patch-workspace token (min 24 chars, must differ from the viewer token). Blank = generated per run. |
| `IGDB_CLIENT_ID`, `IGDB_CLIENT_SECRET` | IGDB metadata credentials. |
| `REMOTE_SEARCH_PASSWORD` | SFTP password for remote search. |
| `DATABASE_URL` | Optional `postgres://` URL selecting the PostgreSQL backend. Blank = SQLite. |

## API access

A browser session (cookie, established at `/login`) is the normal way to reach the internal API. Scripts and other tools that cannot hold a session authenticate instead with a named, revocable API key, created from **Admin → API Keys** by a signed-in admin. A key carries that admin's authority everywhere a session would: general routes and the admin-only surface alike.

Send it as a bearer token:

```sh
curl -H "Authorization: Bearer rr_your-key-here" https://your-relay/api/admin/games
```

Revoking a key from the same page takes effect immediately. Revoking is preferred over deleting the admin account that issued it; disabling or deleting that account also revokes every key it created. See [API.md](API.md) for the full route reference.

## Clean shutdown

Use the authenticated **Shut down ROM Relay cleanly** button on the admin page, or Ctrl+C in the foreground console. The admin route requires the admin cookie and CSRF token. Shutdown stops accepting HTTP, cancels active background work and queued patch/SFTP jobs, waits for their goroutines, checkpoints SQLite WAL, closes the database, and then exits. A normal clean exit releases the TCP port and removes SQLite WAL/SHM sidecars. Forced process termination cannot run this sequence and may leave recoverable sidecars for the next SQLite open.

## Database backend

By default ROM Relay uses an embedded SQLite catalog (the `[library]` `database_path` file), which is ideal for single-user/local deployments. For multi-user or large deployments, set `DATABASE_URL` to a `postgres://` URL in `.env`; ROM Relay then uses PostgreSQL and ignores `database_path`. The schema is created automatically on first start against either backend. Only catalog metadata lives in the database, ROM bytes always stay on the mounted filesystem.

## `[library]`

| Key | Default | Meaning |
|---|---:|---|
| `directory` | `platforms` | Platform-shaped ROM library. Created automatically. |
| `database_path` | `romrelay.db` | SQLite catalog cache (ignored when `DATABASE_URL` is set); ROM bytes are never stored here. |
| `scan_interval_hours` | `1` | Reconciliation interval. `0` disables repeats, not startup reconciliation. |
| `metadata_refresh_days` | `30` | Libretro index and metadata staleness interval; also controls optional IGDB refresh. |
| `existing_output` | `skip` | Must be `skip`; ROM Relay never overwrites a CIA. |
| `ui_theme` | `default` | `default` or `corn`; presentation only, read once at startup. |

## Theme contract

- `default` uses clean ROM Relay copy and the dark-blue palette.
- `corn` uses Corn Mode copy and the harvest-gold palette: corn vocabulary in the catalog, game details, patch admin page, authentication errors, startup summary, scans, conversions, warnings, server/admin URLs, and shutdown output.
- Both themes share every layout and component; only the design-token values and the wording change.
- Theme choice never changes file formats, paths, database contents, hashes, authentication decisions, or HTTP behavior.
- When stdout is redirected, progress becomes deterministic plain lines and ANSI/color/cursor sequences are disabled.
- A real stdin/stdout terminal automatically receives the compiled-in tview dashboard: library counts, active jobs, logs, CPU/RAM/HTTP/download and SQLite health. Press `q` or Ctrl+C for a clean shutdown. No configuration field or separate runtime is required.
- `q` and Ctrl+C enter the same coordinated shutdown path. The dashboard stays on screen until workers and SQLite close and the final shutdown event is rendered; tview never exits independently.

## boot9 and dump dependencies

| Operation/input | boot9 needed? |
|---|---|
| Original-NCCH-encrypted `.3ds` or `.cci` conversion | Yes |
| Decrypted or zero-key `.3ds`/`.cci` conversion | No |
| Supplied encrypted or unencrypted `.cia` | No |
| Prepare an encrypted CIA for a mobile emulator | No boot9; optional operator-installed Redux driver bundle |
| Catalog/hash/header scan | No |
| HTTP download, ranges, FBI QR/queue | No |
| Other supported platforms | No |

Place a full `boot9.bin` or protected `boot9_prot.bin` under the fixed `internal/drivers/3ds` directory under the drivers root (`DRIVERS_DIR` in Docker). The converter checks it against the known retail SHA-256. ROM Relay never downloads, copies, or exposes it. Developer-unit keys are not supported. A configuration that still contains `boot9_path` is rejected with migration guidance.

## Optional CIA decryption driver

Encrypted CIA downloads always remain available for FBI and real/homelab 3DS use. Mobile-emulator preparation is enabled only when the files documented in `internal/drivers/cia-3ds-decryptor/README.md` exist beside the executable. There is no configurable driver path. The Redux-compatible workflow is administrator-only and never runs from a public catalog session. Its upstream bundle includes unsigned third-party executables and has no aggregate repository license, so ROM Relay does not redistribute it.

## `[server]`

| Key | Default | Meaning |
|---|---:|---|
| `listen_address` | `0.0.0.0` | Local bind IP. |
| `port` | `8080` | Local TCP port. |
| `advertise_ip` | auto | Address placed in direct URLs and QR codes. Set it when multiple LAN/VPN adapters are present. |
| `advertise_port` | local port | Externally advertised port. |
| `public_base_url` | blank | Complete HTTP(S) reverse-proxy/VPN origin; overrides advertised host/port. |

The viewer token is the `REMOTE_ACCESS_TOKEN` secret in `.env` (not `config.ini`). Configured values require at least 24 characters.

> `public_base_url` may not contain credentials, a query, or a fragment. Plain HTTP tokenizes access but does not encrypt traffic; prefer a private VPN or an HTTPS reverse proxy.

## `metadata`

Metadata providers never delay HTTP startup. ROM Relay always shows local filename/header metadata first and enriches non-patched games in the background. Provider errors preserve the previous SQLite metadata and cached artwork. The public catalog polls `/healthz` for live background state and shows platform fallback covers until validated box art is cached. Libretro uses two DAT workers and three enrichment/artwork workers; IGDB requests remain serialized. The scheduler checks hourly, but healthy provider data is not refreshed until `metadata_refresh_days`; interrupted or offline refreshes become retry-eligible at the next hourly check instead of being treated as fresh.

### Libretro setup

Libretro is enabled by default and needs no account or API key:

```ini
[metadata.libretro]
enabled = true
database_directory = metadata/libretro/databases
thumbnail_directory = metadata/libretro/thumbnails
auto_update = true
download_thumbnails = true

[metadata.igdb]
enabled = false
# client_id and client_secret come from .env (IGDB_CLIENT_ID / IGDB_CLIENT_SECRET)
```

With `auto_update = true`, ROM Relay retrieves the plain Libretro DAT sources for GB, GBC, GBA, NDS, 3DS, NES, SNES, N64, and Genesis from the official `libretro-database` GitHub repository. Each response is size-bounded and written to a temporary file before publication. Each platform's parsed SQLite index is replaced in one transaction; an interrupted or failed update leaves that platform's previous index usable.

Matching is platform-constrained: SHA-1 first, then MD5, then CRC32. If every hash misses, a same-platform exact case-insensitive full release filename is allowed. There is no partial or fuzzy filename match. The fallback is important for converted 3DS/CIA containers and ROMs whose display title was shortened by GB/GBC/GBA/NDS cartridge headers. An unmatched game keeps its locally parsed metadata. Cached box art is decoded, format/dimension/size validated, stored under `thumbnail_directory`, and served by ROM Relay rather than hot-linked.

Libretro supplies identification, release fields, and artwork names, but not prose descriptions. Enable the optional IGDB provider for game synopses.

For a network-isolated installation, set `auto_update = false` and `download_thumbnails = false`. Place clrmamepro-format files in this layout:

```text
metadata/libretro/databases/
  gba/
    no-intro.dat       # required identification DAT
    developer.dat      # optional overlays
    publisher.dat
    genre.dat
    releaseyear.dat
  gb/                  # same filenames
  gbc/
  nds/
  3ds/
  nes/
  snes/
  n64/
  genesis/
```

The source database names are listed in `internal/metadata/libretro.go`. The
required and overlay DATs come from the corresponding folders under
<https://github.com/libretro/libretro-database/tree/master/metadat>. Restart ROM
Relay after installing or replacing manual DAT files. The normal metadata pass
imports them without any network request.

### Optional IGDB setup

IGDB is disabled unless explicitly enabled. It is title/platform enrichment,
not ROM verification; Libretro remains the exact identification source.

1. Sign in to Twitch, enable two-factor authentication, and create a
   confidential application in the Twitch Developer Console. `localhost` is a
   suitable OAuth redirect URL because ROM Relay uses the client-credentials
   flow.
2. Set `IGDB_CLIENT_ID` and `IGDB_CLIENT_SECRET` in `.env`, and set
   `enabled = true` under `[metadata.igdb]` in `config.ini`.
3. Restart ROM Relay. It obtains short-lived Twitch access tokens in memory,
   groups up to ten games from one platform into each slug-filtered
   `/v4/games` request, keeps HTTP calls below the official
   four-request-per-second limit, honors `Retry-After`,
   caches covers locally, and never writes access tokens or credentials to logs
   or SQLite. An exhausted 429 ends that sweep with one warning and becomes
   eligible for the next hourly retry check.

Metadata progress is per ROM. Interactive terminals use the embedded split-pane
operator console; redirected logs emit deterministic provider/title/outcome
lines. Catalog and game-detail pages poll `/healthz` once per second and retain
a native progress bar for every ROM until that sweep finishes, plus the overall
provider bar. Completion summaries distinguish
matches, exact misses, throttles, and failures. IGDB title comparison removes
Unicode combining marks so accented provider names such as `Pokémon` still
match an otherwise exact local `Pokemon` title.

```ini
# config.ini
[metadata.igdb]
enabled = true
```

```env
# .env
IGDB_CLIENT_ID=your-twitch-client-id
IGDB_CLIENT_SECRET=your-twitch-client-secret
```

Keep `.env` readable only by the ROM Relay service account. ROM Relay requires an
exact normalized title from IGDB's platform-filtered results and does not accept
the first fuzzy result. Patched games are excluded from Libretro and IGDB by the
database query itself and by a second provider-side guard.

### Optional PokeAPI setup

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Fills the Pokédex with species detail, evolution requirements, and wild encounters. |
| `data_directory` | `third_party/api-data/data/api/v2` | An on-disk [PokeAPI/api-data](https://github.com/PokeAPI/api-data) endpoint tree. When it exists, the fill reads from disk and never touches the network. |

```ini
[metadata.pokeapi]
enabled = false
data_directory = third_party/api-data/data/api/v2
```

The api-data checkout is a git submodule: run `git submodule update --init`
to fetch it. The Docker deployment bind-mounts it read-only and points
`data_directory` at the mount, so the fill is fully offline there. Without
the checkout, the fill falls back to `pokeapi.co` (no credentials required),
runs once in the background, and is then offline; the Pokédex works without
it either way, showing whatever the local game data already supplies.

The private admin workspace also contains **Edit game metadata**. It can replace
all user-facing text fields and a PNG/JPEG/WebP cover for any game, or remove a
cover. A saved row is marked as a manual override and is excluded from all later
provider queues.

Patch jobs publish phase events to `/healthz`, the web Activity panel, the
terminal Activity / Jobs pane, and normal logs. Both patch workspaces accept an
optional description and validated PNG/JPEG/WebP cover. The public result is
always temporary; the organized private workspace additionally exposes catalog
base-ROM selection, permanent retention, remote import, and server control.

The catalog's **Batch install selected CIAs** panel lists only encrypted CIA
artifacts. The selection can produce one or more ordered FBI QR codes or be sent
to the selected device on port 5000. Unencrypted/decrypted CIAs are intended for
compatible emulators and are excluded from FBI batches.

For identified Libretro releases, ROM Relay prefers the clean DAT title over a
longer filename containing dump tags such as `[!]`, `[C]`, `(UE)`, revisions,
or duplicate-copy numbers. Batched slug lookup is followed by a serialized
exact search only for misses. Subtitle token order may differ (for example,
Super Mario Advance naming), but platform and complete normalized title tokens
must still agree. ROM hacks that are not published through the patch workspace
remain local-only rather than inheriting potentially misleading base-game
prose.

Files below `platforms/<platform>/patches/` belong exclusively to the patch
recipe index. Legacy nested `platforms/<platform>/patched/` directories are
also ignored; published outputs belong under the separate configured
`patched/<platform>/` root. Reconciliation skips both reserved subtrees and
prunes old catalog rows without deleting their files.

## Remote search (SFTP)

Remote search is disabled by default. It is intended for a private SFTP library
you administer, following romPad's search-and-stream interaction model. ROM
Relay never contacts a public ROM catalog and never downloads unless an
administrator explicitly selects a result.

```ini
# config.ini
[remote_search]
enabled = true
host = sftp.example.internal
port = 22
username = romrelay-readonly
root_directory = /srv/rom-library
host_key_sha256 = SHA256:replace-with-the-verified-fingerprint
max_results = 200
max_file_size_mb = 4096

# Optional per-platform directory overrides. When present this section fully
# replaces the built-in defaults, so list every platform you use.
[remote_search.platforms]
gb = gb
gbc = gbc
gba = gba
nds = nds
3ds = 3ds
nes = nes
snes = snes
n64 = n64
genesis = genesis
```

```env
# .env
REMOTE_SEARCH_PASSWORD=use-a-dedicated-strong-password
```

Use a dedicated, read-only SFTP account restricted to `root_directory`. Obtain
the server's SHA-256 host-key fingerprint from the server administrator or from
the server itself. For an OpenSSH server, an administrator can inspect it with:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256
```

Do not trust an unverified `ssh-keyscan` result by itself; compare any scanned
key through a separate trusted channel. ROM Relay refuses the connection when
the presented key differs from `host_key_sha256`. The host, username, password,
and remote paths are never placed in download URLs, logs, or SQLite. The
password lives in `.env` (`REMOTE_SEARCH_PASSWORD`), so restrict that file to the
ROM Relay service account.

`root_directory` must be an absolute SFTP path. Each relative
`platform_directories` value is joined beneath it. Omit a platform mapping to
make that platform unavailable remotely. Searches are bounded by `max_results`;
symlinks, non-regular files, unsupported extensions, and files exceeding
`max_file_size_mb` are ignored. Downloads re-check the remote size, validate
the local result, run one at a time, and delete partial files on failure or
cancellation.

Operator workflow:

1. Search the public catalog normally. If it has no match, use **Search
   Remotely** and authenticate with the separate admin token.
2. Select a platform and search term, then copy the opaque result ID into the
   fetch form. The ID represents only the latest result set and expires when a
   new search replaces it.
3. Leave **Keep on this system** clear for a temporary download. It is published
   under `platforms/external/antisocial-expert/<platform>/<job-id>/`, appears in
   the catalog with a direct QR, and expires after 24 hours. A successfully
   completed full or ranged GET resets that expiry; HEAD and aborted transfers
   do not. For a 3DS `.cia`, choose encrypted or unencrypted; the remote default
   is unencrypted. This choice is not shown or stored for other platforms.
4. To retain it, select **Keep on this system** and enter its explicit local
   destination: `gb`, `gbc`, `gba`, `nds`, `nes`, `snes`, `n64`, `genesis`,
   `3ds/3ds`, `3ds/cia/encrypted`, or `3ds/cia/unencrypted`. The destination
   must match the searched platform. Existing files are never overwritten.

Remote search and fetch APIs require the admin cookie; fetch/cancel mutations
also require CSRF. Catalog viewers can download a completed external artifact
and scan its QR, but cannot browse the SFTP server, start a transfer, retain a
file, or choose its destination.

## `[storage]`

| Key | Default | Meaning |
|---|---:|---|
| `save_directory` | `storage/saves` | Users' uploaded game saves, stored as `<owner-id>/<save-id>.sav`. Created automatically. |

Saves are uploaded from a game's page in the library, from the Living Dex, or
from the Save Data view (waffle → Save Data), and belong to the signed-in user.
Every upload is kept as a new version, so re-uploading never overwrites earlier
progress; the newest version is what the download button returns.

Only save *metadata* (game, size, hash, timestamp, and any Pokémon details)
lives in the database, the bytes are files. **A backup must cover this
directory as well as the database**, or restoring leaves rows pointing at saves
that no longer exist. In Docker this path is `/app/data/storage/saves`, held in
the `romrelay-storage` volume.

Uploads are capped at 16 MB and require a signed-in session. A save the PKHeX
sidecar recognises also updates that user's Living Dex; a save it cannot read
(any non-Pokémon game) is still stored, and the response says so.

## `[patching]`

| Key | Default | Meaning |
|---|---:|---|
| `enabled` | `true` | Enables the private patch workspace URL. |
| `patch_directory` | `patches` | Temporary/legacy root patch inbox; supported files older than `retention_hours` are pruned. |
| `output_directory` | `patched` | Platform-shaped patched output tree. |
| `retention_hours` | `24` | Temporary patched-output lifetime, reset by completed downloads. |
| `max_rom_size_mb` | `512` | Patch-job source/output limit. |
| `max_patch_size_mb` | `64` | Patch upload/local-file limit. |
| `timeout_seconds` | `120` | Patch execution timeout. |

The administrator secret is the `ADMIN_ACCESS_TOKEN` secret in `.env` (not
`config.ini`). Configured values require at least 24 characters and must differ
from `REMOTE_ACCESS_TOKEN`; blank generates a fresh token each run.

Viewer and administrator tokens must differ. The workspace indexes all nine
supported extensions and applies them through embedded RomPatcher.js in an
isolated Go JavaScript runtime. Mutations require the private admin cookie and
CSRF token. A job can hold source, patch, and output concurrently, so allow
roughly three times the source size in free memory.

Permanent self-supplied patches live under
`platforms/<platform>/patches/<recipe>/`, not the root inbox. Each supported
patch file is hashed and assigned a stable content-derived `patch-<hash>` ID. ROM
Relay indexes UPS/BPS embedded source checksums automatically. An optional
`recipe.json` in the same directory supplies an explicit clean-base fingerprint
and friendly defaults:

```json
{
  "title": "Radical Red",
  "patch": "radical-red.ups",
  "output_filename": "Pokemon Radical Red.gba",
  "base": {
    "sha256": "lowercase-or-uppercase-sha256-of-the-exact-base-rom",
    "crc32": "optional-eight-digit-crc32"
  }
}
```

Unknown manifest fields, oversized manifests, symlink patches, changed indexed
bytes, cross-platform recipes, and base-ROM checksum mismatches are rejected.
When both SHA-256 and CRC32 are supplied, both must match. The patch job and
resulting patched-game row retain `patch_file_id`, providing a durable answer
to which local recipe produced a ROM. Recipe directories are never removed by
temporary cleanup. One-job multipart uploads remain private to their job and
are deleted on completion or failure.

The admin form optionally accepts patched-game title, developer, publisher,
description, release date, genre, region, and cover art. Covers must decode as
PNG, JPEG, or WebP, may not exceed 8 MiB, 8192 pixels on either axis, or 40
million total pixels; SVG and other active formats are rejected. Manual fields
are written only to the distinct `rom_patch = TRUE` game after atomic output
publication. They survive checksum-mismatch confirmation in the private job
directory and are removed with an expired or discarded patched game.

## Migrating from earlier configurations

The former single `config.json` and the legacy CornTool v1 configuration are no
longer read. Move settings into `config.ini` (mirroring the sections above) and
the four secrets into `.env`. Start from `config.example.ini` and `.env.example`.
`boot9_path` and `screenscraper` are not accepted; place boot9 files under
`internal/drivers/3ds` and use `[metadata]` instead.
