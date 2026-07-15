# ROM Relay

Your ROMs. Your network. Your relay. Also available with an entirely
unnecessary **Corn boost**. 🌽

ROM Relay is a single-file, self-hosted game library, patching bench, and QR
download relay for Nintendo handhelds, Delta, Manic EMU, and FBI. Point it at
ROMs you legally dumped, open the catalog on another device, and scan or tap.
No Node, Python, PHP, external database, or separate web server required.

It catalogs GB, GBC, GBA, NDS/DSi, 3DS, NES, SNES, N64, and Genesis files;
converts supported 3DS cartridge dumps to CIA; applies IPS/UPS/BPS and six other
patch formats; serves byte ranges; makes direct-download and FBI batch QR codes;
and can optionally search a private SFTP library you control. It does not ship
games, patches, keys, or a mysterious internet ROM fountain.

## Deploy in about a minute

1. Put `ROMRelay.exe`, `Setup-ROMRelay.bat`, and the supplied setup PowerShell
   script in an empty folder.
2. Double-click `Setup-ROMRelay.bat`. Confirm the detected LAN IP and port; it
   creates/updates `config.json`, backs up the old one, and installs a
   Private-network-only Windows Firewall rule after the UAC prompt.
3. Add your files under `platforms/<platform>/`.
4. Run `ROMRelay.exe` (the setup helper can launch it for you).
5. Open the printed catalog URL. Keep the separate admin URL private unless
   you enjoy surprise patch jobs.

```powershell
Copy-Item .\config.example.json .\config.json
.\ROMRelay.exe
```

The manual PowerShell commands still work, but the BAT avoids accidentally
advertising a WSL/Docker adapter and checks whether another program already owns
the chosen port. Reserving the selected IP in your router keeps old QR codes
from wandering off after DHCP changes.

The first run creates missing folders and `romrelay.db`. During the initial
library scan, conversion queue, and patch indexing, the site shows **Setup in
progress** and the terminal withholds its URLs. Both are replaced when the
catalog is ready. Metadata enrichment then continues in the background.
The catalog header reports live conversion, patch-index, and metadata progress.
3DS conversion stays serial and publishes each completed CIA immediately;
Libretro uses a small bounded worker pool for DATs and artwork so covers can
arrive while conversion continues without turning RAM into a boss battle.

When launched in a real terminal, ROM Relay opens a built-in operator console
with split panes for platform/base/patched/file counts, encrypted versus
unencrypted CIAs, active scans and per-ROM jobs, live logs, CPU/RAM, goroutines,
HTTP traffic, open downloads, and SQLite connection/WAL state. Press `q` for
the same clean shutdown as Ctrl+C. Both keys keep the dashboard visible while
HTTP, jobs, and SQLite close, write a final shutdown event, and only then
restore PowerShell. Redirected logs and Windows service runs
automatically use stable plain text instead. The console is compiled into the
EXE; there is still nothing extra to install.

To stop, use **Shut down ROM Relay cleanly** at the bottom of the private admin
page or press Ctrl+C in its console. Clean shutdown stops HTTP, cancels and
waits for scans/metadata/patch/SFTP jobs, checkpoints SQLite WAL, and releases
the database before exit. Avoid Task Manager, `Stop-Process -Force`, or closing
the console window while work is active; those are forced termination, not an
exit handler.

```text
ROMRelay.exe
Setup-ROMRelay.bat
Setup-ROMRelay.ps1
config.json
romrelay.db
platforms/
  gb/  gbc/  gba/  nds/  nes/  snes/  n64/  genesis/
  3ds/
    3ds/                         # .3ds/.cci inputs
    cia/encrypted/               # FBI / real 3DS
    cia/unencrypted/             # compatible mobile emulators
  <platform>/patches/<recipe>/   # permanent self-supplied patch recipes
  external/antisocial-expert/    # temporary remote downloads
patches/                         # temporary/legacy patch inbox; pruned
patched/<platform>/              # generated patched ROMs
metadata/libretro/               # optional offline metadata cache
internal/drivers/                # optional user-supplied 3DS bits
```

Platform `patches/` trees belong only to the recipe index, and legacy nested
`platforms/<platform>/patched/` trees are ignored. Even if either contains a
ROM-shaped file, it is not scanned or served as a normal library game;
reconciliation also removes legacy catalog rows for it. Published patched ROMs
belong under the separate configured `patched/<platform>/` output root.

Every new or changed ROM is streamed once for SHA-256, SHA-1, MD5, and CRC32.
Unchanged files reuse the SQLite cache, because re-hashing four gigabytes for
fun is not actually fun.

## Patching without base-ROM roulette

The normal **Patch Workspace** is available to catalog viewers. It never uses a
server ROM: the visitor uploads their own legal base ROM, then either uploads a
patch or chooses one of the relay's indexed recipes. Browser-created results
are temporary and expire after 24 hours. The private admin workspace is the
only place that can use catalog artifacts or local inbox paths, confirm a bad
source checksum, delete jobs, or make a completed temporary result permanent.
Both pages explain required fields inline; the admin artifact picker searches
the current catalog instead of demanding a mystery ID from memory.

Every submitted patch reports one stable per-ROM progress bar in the web
catalog's Activity area and terminal Activity / Jobs pane, followed by
validation and publication status. The public form also prompts for a description and
PNG/JPEG/WebP cover/icon. Those fields are optional, but useful: patched games
never query remote metadata and otherwise use the normal controller placeholder.
The private admin page is organized as a management workspace with searchable
base files, numbered patch steps, catalog previews and status badges, metadata
and artwork editing, retention, SFTP import, and safe server control.

Permanent patches belong with their platform:

```text
platforms/gba/patches/mariomon/mariomon.bps
platforms/gba/patches/radical-red/radical-red.ups
platforms/gba/patches/unbound/unbound.ups
```

ROM Relay hashes each patch and assigns a stable ID such as
`patch-4d0c…`. UPS/BPS embedded base checksums are indexed automatically. Add a
`recipe.json` when you want an explicit base SHA-256, friendly title, or output
filename:

```json
{
  "title": "Radical Red",
  "patch": "radical-red.ups",
  "output_filename": "Pokemon Radical Red.gba",
  "base": {
    "sha256": "sha256-of-your-exact-clean-firered-dump"
  }
}
```

This matters: “FireRed” is not a sufficiently precise base-ROM version.
ROM Relay checks the selected catalog artifact against the recipe SHA-256,
CRC32, or embedded UPS/BPS checksum before starting. Mariomon can point at the
correct Emerald dump while Radical Red and Unbound independently point at the
correct FireRed revision. A mismatch stops before publication.

The private patch page lists recipes by stable ID. Patch jobs and resulting
`rom_patch = TRUE` games both store that ID, so the database remembers exactly
which local patch produced an output. Recipe files are permanent and never
pruned. One-job uploads are copied into private staging and deleted afterward;
supported files left in the root `patches/` inbox are legacy/temporary and are
pruned after the configured retention window.

Recipe indexing reads UPS/BPS checksum footers natively and streams hashes; it
does not load a large patch into the JavaScript engine just because the admin
page was opened. The in-memory engine is reserved for an actual patch job.

Patched outputs can carry a custom title, developer, publisher, description,
release date, genre, region, and PNG/JPEG/WebP cover. Temporary outputs expire
after 24 hours; a completed download gives them another 24. Tick the keep box
to make one permanent. Source ROMs are always read-only copies during patching.

## Phones, QR codes, and tiny screens

- Delta and Manic: scan/tap the direct URL, then use iOS Share/Open In.
- FBI: scan a CIA QR or use the ordered batch QR/port-5000 queue.
- Encrypted CIA: best for FBI and a real or homelab 3DS.
- Unencrypted CIA: commonly needed by compatible mobile 3DS emulators.

The 3DS catalog has encrypted/unencrypted filters and badges. Converted
`.3ds`/`.cci` files land in `3ds/cia/encrypted`. Opening an encrypted CIA asks
which flavor you want instead of guessing what your phone can digest.

Open **Batch install selected CIAs** on the catalog, tick the encrypted CIA
files you want, and choose **Build batch QR**. ROM Relay splits oversized
payloads into numbered QR images that are scanned in order. The same selection
can be sent to FBI's port 5000 by entering the private IPv4 address shown in
FBI. Unencrypted/decrypted CIAs are emulator-oriented and intentionally do not
appear in FBI batches; encrypted CIAs are for FBI and physical/homelab 3DS use.

## Optional private SFTP search

When a local search finds nothing, an administrator can choose **Search
Remotely**. This searches only the SFTP server configured in `config.json`—not
the wild internet. Connections require a pinned SHA-256 host key, and results
stay private until a selected file is streamed, size-checked, and structurally
validated.

Temporary results live under `platforms/external/antisocial-expert`, get normal
URLs/QR codes, and expire after 24 hours. **Keep on this system** requires an
explicit local destination. Remote search works for every supported platform.
For remote 3DS CIA files only, the fetch form has an encryption choice and
defaults to **unencrypted**, which is the usual remote-library case.

Full SFTP fields, host-key verification, and folder mapping are in
[CONFIGURATION.md](CONFIGURATION.md#remote-search-sftp).

## Metadata that still works when the internet takes a nap

Libretro DAT matching is the default: exact SHA-1, MD5, or CRC32, cached in
SQLite, with optional locally cached artwork. When all hashes miss, ROM Relay
also permits an exact case-insensitive release-filename match inside the same
platform. This conservative fallback handles converted 3DS containers and the
short internal titles common to GB/GBC/GBA/NDS headers without fuzzy guessing.
Set auto-update off and install
the DATs manually for a fully offline box. Optional IGDB can add richer text and
covers using your own Twitch developer credentials. Patched games never enter
either provider queue—your magnificent “Pikachu But Everyone Is A Tractor”
metadata stays exactly as entered.

Libretro does not provide prose game synopses. With IGDB disabled, description
fields therefore remain empty and the game page says so instead of inventing a
summary. Enable IGDB when descriptions are desired.

Provider guesses are never final. Open the private admin workspace and use
**Edit game metadata** to change the title, description, developer, publisher,
release date, genre, region, players, rating, or cover for any catalog game.
PNG, JPEG, and WebP covers are decoded and validated before publication; the
same form can remove an existing cover. Saving creates a manual override, so
later Libretro or IGDB sweeps leave your corrections alone.

IGDB enrichment groups up to ten games from the same platform into one
slug-filtered `/v4/games` request, keeps requests below four per second, honors
`Retry-After`, and stops a sweep
after an exhausted 429 instead of repeating the same warning for every game.
Exact normalized matching folds Unicode accents (for example, Pokémon and
Pokemon) but remains platform-scoped and otherwise non-fuzzy. Both providers
report every ROM as querying, matched, unmatched, throttled, or failed. An
interactive console renders each ROM in the Activity / Jobs pane; redirected
output prints one deterministic line per ROM state. During a sweep, the catalog and game
pages keep one live HTML progress bar per ROM, plus the overall provider bar.

Setup details live in [CONFIGURATION.md](CONFIGURATION.md#metadata).

The embedded frontend uses a responsive media-library layout—no Node build or
browser framework hiding under the rug. Cards and detail pages include inline
SVG status icons for patched games and encrypted/unencrypted CIAs, compact
metadata facts, readable download variants, direct URLs, and collapsible QR
codes. Default and Corn Mode share the layout while keeping their own palette
and copy.

## Optional 3DS ingredients

Most of ROM Relay needs no dumped driver.

- Original-NCCH-encrypted `.3ds`/`.cci` conversion needs your own retail
  `boot9.bin` or `boot9_prot.bin` under `internal/drivers/boot9-3ds/`.
- Preparing an unencrypted CIA can use an operator-installed
  Batch-CIA-3DS-Decryptor-Redux bundle under
  `internal/drivers/cia-3ds-decryptor/`.

Neither path is configurable, downloadable through ROM Relay, or included in
the executable. See each driver folder's README before adding files.

## Clean mode or Corn Mode

Set `ui_theme` to `default` for the clean blue interface or `corn` for gold,
corny vocabulary, and agriculturally questionable progress bars. The selection
changes the complete web/admin/CLI presentation and nothing else—hashes,
security, output bytes, and routes remain identical. Corn is a theme, not a
cryptographic primitive.

## Remote use and secrets

A private VPN is the easiest remote setup. For a reverse proxy, set
`public_base_url` to its HTTPS origin. Viewer and admin tokens are separate;
sharing a catalog URL never grants patch, upload, SFTP, retention, or decrypt
permissions. Blank admin tokens are generated per run and printed only in the
private startup output.

Protect `config.json` when it contains SFTP, IGDB, or access credentials. Admin
mutations use an HttpOnly SameSite cookie plus CSRF protection; downloads use
safe attachment headers, identity encoding, traversal checks, and HTTP ranges.

## Build and test

Go 1.25 or newer:

```powershell
go test ./...
go vet ./...
go build -buildvcs=false -trimpath -o ROMRelay.exe ./cmd/romrelay
```

The normal suite uses synthetic, copyright-free fixtures. To exercise your own
local ROM/patch collection without committing any bytes:

```powershell
$env:ROMRELAY_FIXTURE_ROOT='D:\Rom Relay'
go test -v ./internal/integration -run TestOperatorSuppliedROMAndPatchFixtures
```

That optional test copies selected files into a temporary directory, verifies
recipe/base matching, applies the supplied UPS/BPS files, catalogs representative
GB/GBC/GBA/NDS games, and checks JSON, range download, and QR generation. The
mock SFTP suite runs normally and needs no external server.

ROM Relay is for ROMs and patches you are legally entitled to use. Bring your
own bytes; we bring the relay—and, if requested, the corn.
