# ROM Relay Setup Guide

This guide assumes you are comfortable copying files and editing a text file,
but do not want to learn Go, databases, or web-server administration.

ROM Relay does not include ROMs, patches, console keys, metadata accounts, or
access to a public ROM site. Use only files you are allowed to possess and
share on your own network.

## 1. Unzip and run the setup helper

1. Extract the release ZIP into a permanent folder such as `C:\ROMRelay`.
2. Double-click `Setup-ROMRelay.bat`.
3. Approve the administrator prompt. It creates a Private-network firewall rule.
4. Confirm the detected LAN adapter and port. Port `8080` is normally fine.
5. Let setup create `config.json` and optionally start ROM Relay.
6. On a fresh boot, the browser shows **Setup in progress** while the initial
   library scan, 3DS conversion queue, and patch index finish. The catalog and
   private admin URLs appear in the terminal only when those are ready.

Keep `config.example.json` unchanged as a reference. Make personal changes in
`config.json`; it is intentionally excluded from release bundles.

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
  boot9-3ds/                     optional user-supplied boot9.bin/boot9_prot.bin
  cia-3ds-decryptor/             optional user-supplied CIA decryptor driver
```

Do not put normal games inside a platform's `patches` or `patched` subfolder.
Generated patched ROMs go in the separate top-level `patched` tree. The main
landing page hides them; use **Patched** or a normal per-platform page to see
them.

## 3. Every configuration line

JSON is strict: keep double quotes, commas, braces, and field names exactly as
shown. Windows paths may use `/`, for example `D:/Games/ROM Relay/platforms`.

### Main settings

| Setting | What it does | Normal value |
|---|---|---|
| `library_directory` | Root of all platform folders. | `platforms` |
| `database_path` | SQLite catalog cache created by ROM Relay. | `romrelay.db` |
| `library_scan_interval_hours` | Rechecks for changed files; `0` disables scheduled rescans. | `1` |
| `metadata_refresh_days` | Days before healthy metadata is refreshed. | `30` |
| `existing_output` | Existing converted output behavior. | Must be `skip` |
| `ui_theme` | Site and terminal colors. | `default` or `corn` |

### `server`

| Setting | Meaning |
|---|---|
| `listen_address` | Interfaces accepting requests. `0.0.0.0` serves your LAN. |
| `port` | Port opened by ROM Relay, normally `8080`. |
| `advertise_ip` | LAN IP placed in URLs. The setup helper fills this in. Reserve it in your router. |
| `advertise_port` | Port placed in URLs. Use the same as `port`; `0` becomes `port`. |
| `public_base_url` | Advanced HTTPS/reverse-proxy override. Leave blank on a normal LAN. |
| `remote_access_token` | Optional viewer secret, at least 24 characters. |

Generate viewer/admin secrets locally, not on a password website:

```powershell
$bytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToHexString($bytes).ToLowerInvariant()
```

Run it twice: viewer and admin secrets must differ. Anyone with the admin URL
can create persistent outputs and edit metadata, so do not share screenshots
containing it.

### `metadata.libretro`

| Setting | Meaning |
|---|---|
| `enabled` | Exact Libretro identification; no account needed. |
| `database_directory` | Local DAT/index cache. |
| `thumbnail_directory` | Local cover and manual-art cache. |
| `auto_update` | Downloads current DAT sources. Disable for offline use. |
| `download_thumbnails` | Downloads matching box art locally. |

The source is the official [Libretro database repository](https://github.com/libretro/libretro-database).
Metadata continues after the catalog is usable, so covers may fill in gradually.

### `metadata.igdb` (optional descriptions)

| Setting | Meaning |
|---|---|
| `enabled` | Turns IGDB enrichment on. |
| `client_id` | Twitch application's Client ID. |
| `client_secret` | Twitch application's secret; treat it like a password. |

Follow Twitch's official [Register Your App](https://dev.twitch.tv/docs/authentication/register-app)
guide. A Twitch account with two-factor authentication is required. Register a
confidential application, use `http://localhost` as its redirect URL, and copy
the Client ID and newly generated Client Secret into `config.json`. ROM Relay
obtains temporary access tokens itself; do not paste an OAuth token here.

### `patching`

| Setting | Meaning |
|---|---|
| `enabled` | Enables public/private patch workspaces. |
| `patch_directory` | Temporary/legacy patch inbox. |
| `output_directory` | Separate generated-patched-ROM tree. |
| `admin_access_token` | Stable admin secret, at least 24 characters and different from viewer token. Blank generates one per run. |
| `retention_hours` | Lifetime of temporary results; downloads renew it. |
| `max_rom_size_mb` | Largest patch-job source ROM. |
| `max_patch_size_mb` | Largest patch file. |
| `timeout_seconds` | Maximum execution time for one patch. |

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
or `boot9_prot.bin` under `internal/drivers/boot9-3ds`. ROM Relay does not
download or provide console keys.

### Prepare unencrypted CIA copies

ROM Relay can prepare an encrypted CIA for compatible mobile emulators through
the workflow from Batch-CIA-3DS-Decryptor-Redux. The upstream repository does
not provide a repository-level redistribution license and its included Windows
executables are unsigned, so ROM Relay does not bundle them.

To enable the feature, obtain the bundle yourself from [xxmichibxx/Batch-CIA-3DS-Decryptor-Redux](https://github.com/xxmichibxx/Batch-CIA-3DS-Decryptor-Redux)'s repository.

Place these files here beside `ROMRelay.exe`:

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

- **No URL yet:** setup is scanning, converting, or indexing. The stage is in
  the terminal/setup page. Metadata downloads do not block ready.
- **Phone cannot connect:** use the same LAN, a Private Windows network profile,
  and avoid guest Wi-Fi client isolation.
- **Wrong QR IP:** rerun setup after changing adapters and reserve that IP.
- **Config parse error:** JSON has no comments or trailing commas; compare the
  example file.
- **Patch mismatch:** use the exact clean revision required by the patch.
- **CIA output exists:** ROM Relay never overwrites it; decide which to keep.
- **IGDB empty:** check enabled, credentials, internet, and exact title/platform.

See `CONFIGURATION.md` for advanced validation and deployment details.
