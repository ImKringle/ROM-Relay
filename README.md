<p align="center">
  <img src="assets/core/transparent-logo.png" alt="ROM Relay" width="220">
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white" alt="Go 1.25+"></a>
  <a href="docker-compose.yml"><img src="https://img.shields.io/badge/docker-compose-2496ED?logo=docker&logoColor=white" alt="Docker Compose"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="AGPL-3.0 License"></a>
</p>

# ROM Relay

**Your ROMs. Your network. Your relay.**

ROM Relay is a self-hosted game library and Pokémon toolkit for ROMs you legally dumped. Point it at your collection, open it from any device on your network, and download, patch, install, and track. It ships no games, patches, or keys, and only fetches from sources you configure yourself.

The whole stack runs from one `docker compose up`: the app, PostgreSQL, and sidecars for save parsing, 3DS/NDS crypto, randomizing, and torrents.

---

## What it does

| | |
|---|---|
| 🎮 **Game Library** | Catalogs GB, GBC, GBA, NDS/DSi, 3DS, NES, SNES, N64, and Genesis files with box art and metadata (Libretro; optional IGDB). One card per release, even when a game exists as both an encrypted and unencrypted CIA. |
| 📱 **QR downloads** | Direct-download QR codes for Delta and Manic EMU, plus ordered FBI batch QR codes and port-5000 push for installing CIAs on a real 3DS. |
| 🧩 **ROM Patcher** | Applies IPS, UPS, BPS, PPF, RUP, APS, BSDiff, PMSR, and VCDiff against a clean base you upload, with checksum validation before publication. Public results expire in 24 h unless an admin keeps them. |
| 🔄 **3DS conversion** | Converts supported `.3ds`/`.cci` cartridge dumps to installable CIAs (needs your own `boot9.bin`). |
| 🔐 **NDS/3DS crypto** | Encrypt or decrypt raw `.nds`/`.3ds`/`.cci` dumps through the NDecrypt sidecar (needs your own keys). |
| 📡 **FTP send** | Browse a 3DS/NDS running a homebrew FTP server (e.g. ftpd) and copy library files straight onto it. |
| 📖 **Pokédex** | Upload a Pokémon save and ROM Relay reads it with PKHeX: a Living Dex across every game you play, per-region and per-game Pokédexes, trainers, badges, Hall of Fame, and a type chart. Species detail comes from an offline PokeAPI dump. |
| 🏠 **PokeHOME** | Deposit Pokémon out of saves into a per-user box, edit them with legality checks, and download any one as a `.pkm` that desktop PKHeX opens. |
| 🎲 **Randomizer** | Randomize Gen 1–5 Pokémon ROMs with Universal Pokemon Randomizer ZX, configured from the web UI. |
| 💾 **Save Vault** | Versioned storage for game saves. Every upload is kept as a new version; pick your progress up on any device. |
| 👥 **Accounts** | Multi-user with roles. Each user has their own Pokédex and saves; admins manage users, metadata, patch publishing, retention, and API keys. |
| 🗂️ **WebDAV** | Optional read-only `/webdav/` view of the catalog for emulators that import from a "cloud drive", with a key per user. |

### Optional integrations

None of these are needed. Each one turns on once you configure it:

| Integration | What it adds | Configure |
|---|---|---|
| **RomM** | Search and grab ROMs from your RomM instance, and fill in metadata/covers by hash match. Grabs are kept for 7 days unless an admin retains them. | `ROMM_URL` + `ROMM_TOKEN` (`roms.read`, add `assets.read` for covers) |
| **Torrents** | Admins upload `.torrent` sources and users grab single games. Leech-only (no seeding); grabs are pruned after 7 days unless kept. | Bundled qBittorrent sidecar; forward `QBT_TORRENTING_PORT` (6881 TCP/UDP) |
| **IGDB** | Descriptions and extra metadata. | `[metadata.igdb] enabled` + `IGDB_CLIENT_ID`/`IGDB_CLIENT_SECRET` |
| **Remote SFTP** | Search and import from a private SFTP library, with pinned host keys. | `[remote_search]` + `REMOTE_SEARCH_PASSWORD` |
| **Reverse proxy / tunnel** | Remote access with working QR codes. | `PUBLIC_BASE_URL` |

## Quick start

You need Docker (Desktop on Windows/macOS, Engine on Linux) and ROMs laid out as `platforms/<platform>/`:

```text
platforms/
  gb/  gbc/  gba/  nds/  nes/  snes/  n64/  genesis/
  3ds/
    3ds/               # .3ds/.cci conversion inputs
    cia/encrypted/     # FBI / real 3DS
    cia/unencrypted/   # mobile & desktop emulators
  <platform>/patches/<recipe>/   # permanent patch recipes
```

Then:

```bash
git clone --recursive <repo-url> && cd ROM-Relay   # or: git submodule update --init
cp .env.example .env    # set POSTGRES_PASSWORD, ADMIN_PASSWORD, ROMS_DIR, PUBLIC_BASE_URL
docker compose up -d --build
```

Open `http://<host>:8080` and sign in as `admin`. The first boot indexes ROM headers in seconds, even for a 50 GB library, and hashes content in the background while you browse.

- **Submodules are required**: `third_party/ndecrypt` is needed for the build, and `third_party/api-data` feeds the Pokédex.
- **Set `PUBLIC_BASE_URL`** (or `ADVERTISE_IP`) in `.env`. Inside a container, auto-detection only sees the compose network, so without it QR codes and download links point nowhere. Behind a reverse proxy or tunnel (e.g. Cloudflare Tunnel), use its public URL.
- **Settings**: the image bakes in `config.docker.ini`. To customize, copy it to `config.ini` (gitignored), edit, and rebuild. Secrets go in `.env`. Every setting: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
- **Optional key material** (read-only `DRIVERS_DIR`, default `./drivers`): `3ds/boot9.bin` for CIA conversion, `ndecrypt/config.json` for NDS/3DS crypto (format in `third_party/ndecrypt/config-default.json`).
- **Ports**: `HTTP_PORT` (default 8080) for the app, and `QBT_TORRENTING_PORT` (default 6881 TCP+UDP) for torrent peers. The sidecars are never published.
- **Back up** the Postgres volume, `romrelay-storage` (user saves and `.torrent` sources), and `romrelay-patches` (uploaded patches). Saves live on disk, not in the database.
- **Walkthrough**: [docs/SETUP-GUIDE.md](docs/SETUP-GUIDE.md) · **API**: [docs/API.md](docs/API.md)

## The stack

```
docker-compose.yml
├── romrelay     Go app: library, web UI, patcher, QR, Pokédex, WebDAV
├── db           PostgreSQL 16 (catalog, users, dex)
├── pkhex        .NET PKHeX sidecar: save parsing, legality, editing
├── ndecrypt     .NET NDS/3DS encrypt/decrypt sidecar
├── randomizer   Java Universal Pokemon Randomizer ZX sidecar
└── qbittorrent  leech-only torrent client, on its own network with romrelay only
```

Only `romrelay` (HTTP) and qBittorrent's peer port are published; every sidecar API stays on the internal network. The web UI is embedded in the Go binary, no Node at runtime. The image builds multi-arch (`linux/amd64`, `linux/arm64`) for mini PCs and ARM boards alike.

### Running without Docker

`go build ./cmd/romrelay` produces a single binary that reads `config.ini` and `.env` from its own directory (or `-config-dir`). Leave `DATABASE_URL` blank for SQLite. Sidecar features stay off until you run those services yourself and set `POKEMON_SIDECAR_URL`, `RANDOMIZER_SIDECAR_URL`, and `NDECRYPT_SIDECAR_URL`. With `[torrent] enabled = true` and no `QBITTORRENT_URL`, it runs a private `qbittorrent-nox` from `PATH`. Run in a terminal, it shows a live operator dashboard.

## Patch recipes

Permanent patches live with their platform, and each gets a stable hashed ID:

```text
platforms/gba/patches/radical-red/radical-red.ups
```

UPS/BPS embedded base checksums are indexed automatically; add a `recipe.json` for an explicit base SHA-256, title, or output filename. A base-ROM mismatch stops the job before publication, "FireRed" is not a sufficiently precise base version, and ROM Relay refuses to guess.

## 3DS notes

- **Encrypted CIA** installs on a real 3DS through FBI (QR or port 5000).
- **Unencrypted CIA** is for emulators; the two are offered side by side on each game's page and FBI batches intentionally exclude unencrypted files.
- `.3ds`/`.cci` → CIA conversion is pure Go and works in the container; point `DRIVERS_DIR` at a folder containing `3ds/boot9.bin`.
- Preparing *unencrypted* CIAs uses the Windows-only Redux decryptor and does not run in the Linux container. See `internal/drivers/*/README.md`.

## Security model

- Anonymous LAN browsing is on by default; set `REMOTE_ACCESS_TOKEN` to require a token for downloads and a sign-in for browsing. A private VPN is the simplest remote setup; behind a reverse proxy or tunnel, set `PUBLIC_BASE_URL`.
- Viewer access and admin rights are separate. Admin mutations use an HttpOnly SameSite session plus CSRF; downloads use attachment headers, traversal checks, and HTTP ranges.
- Scripts and other tools authenticate as named, revocable API keys (`Authorization: Bearer <token>`) issued
  from **Admin → API Keys**, instead of sharing a login session. See [docs/API.md](docs/API.md).
- The UI ships a strict Content-Security-Policy, no inline scripts or styles.
- RomM tokens never leave the server: the relay proxies every RomM call.
- WebDAV is read-only, with no write path, and each user has their own revocable key.
- Protect `.env`: it holds the database password and any SFTP/IGDB/RomM credentials.

## Developing

Go 1.25+, and Node only if you touch the UI source (`web/`, React + Vite):

```bash
go test ./...                     # full suite, synthetic fixtures
cd web && npm ci && npm run build # regenerate internal/web/spa/dist (committed) after UI changes
```

An optional integration test exercises your own local collection without committing bytes: set `ROMRELAY_FIXTURE_ROOT` and run `go test ./internal/integration -run TestOperatorSuppliedROMAndPatchFixtures`.

## License

ROM Relay is licensed under [AGPL-3.0](LICENSE). Third-party components keep their own licenses, credited in [third_party/THIRD_PARTY_NOTICES.md](third_party/THIRD_PARTY_NOTICES.md).

## Legal

ROM Relay manages files you already have the rights to use. Bring your own bytes; we bring the relay, and the corn. 🌽
