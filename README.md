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

ROM Relay is a self-hosted game library and Pokémon toolkit for ROMs you legally dumped. Point it at your collection, open it from any device on your network, and download, patch, install, and track, it ships no games, patches, or keys, and it will not find them for you.

The whole stack runs from one `docker compose up`: the app, PostgreSQL, and the PKHeX save-parsing sidecar.

---

## What it does

| | |
|---|---|
| 🎮 **Game Library** | Catalogs GB, GBC, GBA, NDS/DSi, 3DS, NES, SNES, N64, and Genesis files with box art and metadata (Libretro; optional IGDB). One card per release, even when a game exists as both an encrypted and unencrypted CIA. |
| 📱 **QR downloads** | Direct-download QR codes for Delta and Manic EMU, plus ordered FBI batch QR codes and port-5000 push for installing CIAs on a real 3DS. |
| 🧩 **ROM Patcher** | Applies IPS, UPS, BPS, PPF, RUP, APS, BSDiff, PMSR, and VCDiff against a clean base you upload, with checksum validation before publication. Public results expire in 24 h unless an admin keeps them. |
| 🔄 **3DS conversion** | Converts supported `.3ds`/`.cci` cartridge dumps to installable CIAs (needs your own `boot9.bin`). |
| 📖 **Pokédex** | Upload a Pokémon save and ROM Relay reads it with PKHeX: a Living Dex across every game you play, per-region and per-game Pokédexes, trainers, badges, and Hall of Fame. |
| 💾 **Save Vault** | Versioned storage for game saves. Every upload is kept as a new version; pick your progress up on any device. |
| 👥 **Accounts** | Multi-user with roles. Each user has their own Pokédex and saves; admins manage users, metadata, patch publishing, and retention. |
| 🔍 **Remote search** | Optional search-and-import from a private SFTP library you control, with pinned host keys. |

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
cp .env.example .env    # set POSTGRES_PASSWORD, ADMIN_PASSWORD, ROMS_DIR
docker compose up -d --build
```

Run both from the repo root. Open `http://<host>:8080` and sign in as `admin`. The first boot indexes ROM headers in seconds, even for a 50 GB library, and hashes content in the background while you browse.

- **Walkthrough**: [docs/SETUP-GUIDE.md](docs/SETUP-GUIDE.md)
- **Every setting**: [docs/CONFIGURATION.md](docs/CONFIGURATION.md), non-secret settings live in `config.docker.ini`, secrets in `.env`.
- **API reference**: [docs/API.md](docs/API.md).
- **Back up** the Postgres volume *and* the save directory: user saves live on disk, not in the database.
- **Remote access**: point a reverse proxy or tunnel (e.g. a Cloudflare Tunnel) at the published port and set `PUBLIC_BASE_URL` in `.env` to its public URL, otherwise QR codes and download links advertise an address only reachable on your LAN.

## The stack

```
docker-compose.yml
├── romrelay   Go app: library, web UI, patcher, QR, Pokédex
├── db         PostgreSQL 16 (catalog, users, dex)
├── pkhex      .NET save-parsing sidecar (internal network only)
└── ndecrypt   .NET encrypt/decrypt sidecar (internal network only)
```

The web UI is embedded in the Go binary, no Node at runtime. The image builds multi-arch (`linux/amd64`, `linux/arm64`) for mini PCs and ARM boards alike.

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
- Protect `.env`: it holds the database password and any SFTP/IGDB credentials.

## Developing

Go 1.25+, and Node only if you touch the UI source:

```bash
go test ./...           # full suite, synthetic fixtures
npm run build           # regenerate internal/web/ui/dist after UI changes
```

An optional integration test exercises your own local collection without committing bytes: set `ROMRELAY_FIXTURE_ROOT` and run `go test ./internal/integration -run TestOperatorSuppliedROMAndPatchFixtures`.

## License

ROM Relay is licensed under [AGPL-3.0](LICENSE). Third-party components keep their own licenses, credited in [third_party/THIRD_PARTY_NOTICES.md](third_party/THIRD_PARTY_NOTICES.md).

## Legal

ROM Relay manages files you already have the rights to use. Bring your own bytes; we bring the relay, and the corn. 🌽
