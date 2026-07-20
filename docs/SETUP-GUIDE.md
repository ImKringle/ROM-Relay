# ROM Relay Setup Guide

This guide assumes you are comfortable copying files and editing a text file,
but do not want to learn Go, databases, or web-server administration.

ROM Relay does not include ROMs, patches, console keys, metadata accounts, or
access to a public ROM site. Use only files you are allowed to possess and
share on your own network.

## 1. Install with Docker Compose

ROM Relay runs as one Docker Compose stack: the app, PostgreSQL, and the PKHeX
save-parsing sidecar. You need Docker Desktop (Windows/macOS) or Docker Engine
(Linux).

1. Clone or download this repository to a permanent location.
2. From the repo root, copy `.env.example` to `.env`.
3. Edit `.env`: set a strong `POSTGRES_PASSWORD`, an `ADMIN_PASSWORD` for the
   first admin account, and point `ROMS_DIR` at your `platforms/` folder
   (Windows paths may use `/`, e.g. `D:/Rom Relay/platforms`).
4. `docker compose up -d --build`, run from the repo root.
5. Open `http://<your-host>:8080` (or `HTTP_PORT` from `.env`).

Non-secret settings live in `config.docker.ini`; secrets stay in `.env` at
the repo root. Keep `config.example.ini` and `.env.example` unchanged as
references. To advertise the right address in QR and download URLs, set
`PUBLIC_BASE_URL` in `.env`, either your host's LAN address
(`http://192.168.1.50:8080`) or, for remote access, a reverse proxy's public
URL such as a Cloudflare Tunnel hostname (`https://romrelay.example.com`).
Inside the container, auto-detection can only see the compose network, so
this is required for working links, not optional.

## 2. Folder layout and what belongs where

The release contains the empty folder structure. ROM Relay also creates missing
configured folders automatically.

```text
platforms/
  gb/                            .gb games
  gbc/                           .gbc games
  gba/                           .gba games
  nds/                           .nds or .ds games
  nes/                           .nes games
  snes/                          .smc, .sfc, or .fig games
  n64/                           .n64 or .z64 games
  genesis/                       .gen, .bin, .md, or .smd games
  3ds/
    3ds/                         .3ds or .cci cartridge images
    cia/encrypted/               encrypted .cia for FBI / physical 3DS
    cia/unencrypted/             decrypted .cia for compatible emulators
  <platform>/patches/<recipe>/   permanent patch recipes
patched/<platform>/              ROM Relay's generated patched games
patches/                         temporary/legacy patch inbox
metadata/libretro/databases/     downloaded or manually supplied DAT indexes
metadata/libretro/thumbnails/    cached cover artwork
internal/drivers/
  3ds/                           optional user-supplied boot9.bin/boot9_prot.bin/seeddb.bin
  cia-3ds-decryptor/             optional user-supplied CIA decryptor driver
```

Do not put normal games inside a platform's `patches` or `patched` subfolder.
Generated patched ROMs go in the separate top-level `patched` tree. The main
landing page hides them; use **Patched** or a normal per-platform page to see
them.

## 3. Every configuration line

Non-secret settings live in `config.ini`, grouped in `[section]` blocks as
`key = value`. Lines starting with `#` or `;` are comments. Secrets live in
`.env` as `KEY=VALUE`. Windows paths may use `/`, for example
`D:/Games/ROM Relay/platforms`.

### `[library]`

| Setting | What it does | Normal value |
|---|---|---|
| `directory` | Root of all platform folders. | `platforms` |
| `database_path` | SQLite catalog cache created by ROM Relay. | `romrelay.db` |
| `scan_interval_hours` | Rechecks for changed files; `0` disables scheduled rescans. | `1` |
| `metadata_refresh_days` | Days before healthy metadata is refreshed. | `30` |
| `existing_output` | Existing converted output behavior. | Must be `skip` |
| `ui_theme` | Site and terminal colors and wording: `default` is dark blue, `corn` is harvest gold with corn vocabulary. | `default` or `corn` |

### `[server]`

| Setting | Meaning |
|---|---|
| `listen_address` | Interfaces accepting requests. `0.0.0.0` serves your LAN. |
| `port` | Port opened by ROM Relay, normally `8080`. |
| `advertise_ip` | LAN IP placed in URLs. In Docker, set `PUBLIC_BASE_URL` instead. Reserve the IP in your router. |
| `advertise_port` | Port placed in URLs. Use the same as `port`; `0` becomes `port`. |
| `public_base_url` | Advanced HTTPS/reverse-proxy override. Leave blank on a normal LAN. |

The optional viewer secret is `REMOTE_ACCESS_TOKEN` in `.env` (at least 24
characters). Generate viewer/admin secrets locally, not on a password website:

```powershell
$bytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToHexString($bytes).ToLowerInvariant()
```

Run it twice: viewer and admin secrets must differ. Anyone with the admin URL
can create persistent outputs and edit metadata, so do not share screenshots
containing it.

### `[metadata.libretro]`

| Setting | Meaning |
|---|---|
| `enabled` | Exact Libretro identification; no account needed. |
| `database_directory` | Local DAT/index cache. |
| `thumbnail_directory` | Local cover and manual-art cache. |
| `auto_update` | Downloads current DAT sources. Disable for offline use. |
| `download_thumbnails` | Downloads matching box art locally. |

The source is the official [Libretro database repository](https://github.com/libretro/libretro-database).
Metadata continues after the catalog is usable, so covers may fill in gradually.

### `[metadata.igdb]` (optional descriptions)

| Setting | Meaning |
|---|---|
| `enabled` (config.ini) | Turns IGDB enrichment on. |
| `IGDB_CLIENT_ID` (.env) | Twitch application's Client ID. |
| `IGDB_CLIENT_SECRET` (.env) | Twitch application's secret; treat it like a password. |

Follow Twitch's official [Register Your App](https://dev.twitch.tv/docs/authentication/register-app)
guide. A Twitch account with two-factor authentication is required. Register a
confidential application, use `http://localhost` as its redirect URL, set
`IGDB_CLIENT_ID` and `IGDB_CLIENT_SECRET` in `.env`, and set `enabled = true`
under `[metadata.igdb]`. ROM Relay obtains temporary access tokens itself; do
not paste an OAuth token here.

### `[patching]`

| Setting | Meaning |
|---|---|
| `enabled` | Enables public/private patch workspaces. |
| `patch_directory` | Temporary/legacy patch inbox. |
| `output_directory` | Separate generated-patched-ROM tree. |
| `retention_hours` | Lifetime of temporary results; downloads renew it. |
| `max_rom_size_mb` | Largest patch-job source ROM. |
| `max_patch_size_mb` | Largest patch file. |
| `timeout_seconds` | Maximum execution time for one patch. |

The stable admin secret is `ADMIN_ACCESS_TOKEN` in `.env` (at least 24
characters, different from the viewer token; blank generates one per run).

Permanent recipes go at `platforms/<platform>/patches/<recipe>/file.bps` (or
another supported extension). Optional `recipe.json` supplies a title, output
name, and exact base hash. IPS, UPS, BPS, PPF, RUP, APS, BSDiff, PMSR, and
VCDiff are supported.

### `remote_search` (optional private SFTP)

Leave `enabled` false unless you administer a separate SFTP server.

| Setting | Meaning |
|---|---|
| `host` / `port` | Server name without `sftp://`, and its port. |
| `username` / `password` | Dedicated read-only account. |
| `root_directory` | Absolute remote root such as `/srv/rom-library`. |
| `host_key_sha256` | Independently verified fingerprint beginning `SHA256:`. |
| `max_results` | Search cap, 1 through 2000. |
| `max_file_size_mb` | Refuses larger remote files. |
| `platform_directories` | Maps each platform ID to a folder below the remote root. |

Ask the SFTP administrator for its fingerprint. On OpenSSH they can run:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256
```

Compare it through a separate trusted channel.

## 4. Feature setup

### Convert 3DS or CCI to CIA

Put `.3ds`/`.cci` in `platforms/3ds/3ds`. ROM Relay checks them at startup. You
can also use **Convert 3DS / CCI to CIA** in the private admin site. Encrypted
CIA output goes to `platforms/3ds/cia/encrypted`; sources are never changed.

Original-NCCH encrypted images may require your legally obtained `boot9.bin`
or `boot9_prot.bin` under `internal/drivers/3ds`. ROM Relay does not
download or provide console keys.

### Prepare unencrypted CIA copies

ROM Relay can prepare an encrypted CIA for compatible mobile emulators through
the workflow from Batch-CIA-3DS-Decryptor-Redux. The upstream repository does
not provide a repository-level redistribution license and its included Windows
executables are unsigned, so ROM Relay does not bundle them.

To enable the feature, obtain the bundle yourself from [xxmichibxx/Batch-CIA-3DS-Decryptor-Redux](https://github.com/xxmichibxx/Batch-CIA-3DS-Decryptor-Redux)'s repository.

Place these files under the drivers directory (`DRIVERS_DIR` in `.env`;
note the decryptor's Windows executables do not run in the Linux container,
this feature needs a Windows host running the app natively):

```text
internal/drivers/cia-3ds-decryptor/
  Batch CIA 3DS Decryptor Redux.bat
  bin/
    ctrtool.exe
    decrypt.exe
    makerom.exe
    seeddb.bin
```

ROM Relay copies the bundle and source CIA into a private per-run directory,
runs one job at a time with a timeout, validates the output, atomically publishes
it under `platforms/3ds/cia/unencrypted`, then deletes the private workspace.
The encrypted source is never modified. Driver files and logs are never served.

### Install with FBI

Use encrypted CIAs. Choose **Batch install selected CIAs** for ordered QR codes,
or send the selection to the private IP shown by FBI's port-5000 receiver.
Leave ROM Relay running until downloads finish.

### Edit metadata

Use **Edit game metadata and artwork** in admin. Manual changes are protected
from providers. Patched games never receive automatic provider metadata.

## 5. Troubleshooting

- **No URL yet:** setup is scanning, converting, or indexing. The stage is on
  the setup page and in `docker compose logs romrelay`. Metadata downloads do
  not block ready.
- **Phone cannot connect:** use the same LAN and avoid guest Wi-Fi client
  isolation; on Docker Desktop, make sure the chosen port is published.
- **Wrong QR address:** set `PUBLIC_BASE_URL` in `.env` to the host's LAN
  address (and reserve that IP in your router) or your reverse proxy's public
  URL if you're accessing ROM Relay remotely.
- **Config parse error:** check `[section]` headers and `key = value` lines
  against `config.example.ini`.
- **Patch mismatch:** use the exact clean revision required by the patch.
- **CIA output exists:** ROM Relay never overwrites it; decide which to keep.
- **IGDB empty:** check enabled, credentials, internet, and exact title/platform.

See `CONFIGURATION.md` for advanced validation and deployment details.
