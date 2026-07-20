# Third-party notices

ROM Relay's converter is a native Go port of **3dsconv 4.21** by ihaveamac,
pinned to Git commit `50a30d2`. The embedded retail CIA certificate chain and
blank ticket/TMD template are reproduced from that source. 3dsconv is licensed
under the MIT License; its exact notice is stored at
`internal/converter/assets/3dsconv-LICENSE.md`.

QR encoding uses **go-qrcode** by skip2, pinned to Git commit
`da1b6568686e`. It is licensed under the MIT License; its license and source is
vendored under `third_party/`.

Neither component supplies Nintendo console secrets. ROM Relay does not bundle
boot9, games, title keys, or generated CIA files.

The embedded catalog uses **modernc.org/sqlite** v1.53.0 and its transitive
pure-Go runtime dependencies. modernc.org/sqlite is distributed under the
3-clause BSD license; its notice is stored at
`third_party/modernc-sqlite-LICENSE`.

The remote-acquisition design references **romPad** at commit `52c95bb`, an
MIT-licensed SFTP browser by ImKringle. ROM Relay does not copy its Python UI or
bundle its dependencies.

FTP push to a homebrew FTP server (e.g. **ftpd** by mtheall, GPL-3.0) uses
**jlaffaye/ftp** v0.2.0, an FTP client library by Julien Laffaye under an
ISC-style license (see `internal/ftpxfer`). ROM Relay does not vendor or link
against ftpd itself, it only speaks the FTP protocol to a server the operator
already runs on their own console.

CLI tables and interactive progress use **go-pretty/v6** v6.8.2 by Jason
Varghese. It is licensed under the MIT License; its notice is stored at
`third_party/go-pretty-LICENSE`.

The terminal operator dashboard uses **tview** v0.42.0 by Oliver Kuederle under
the MIT License and **tcell/v2** v2.8.1 under Apache-2.0. The tview notice is
stored at `third_party/tview-LICENSE`; tcell's full license is published in its
upstream [LICENSE](https://github.com/gdamore/tcell/blob/v2.8.1/LICENSE). Both are compiled into ROM Relay and require no separately
installed terminal package.

ROM patching embeds **RomPatcher.js** v3.2.1 by Marc Robledo under the MIT
License and executes it with **goja**. The RomPatcher.js license is stored at
`internal/patch/engine/LICENSE`. ROMM was consulted only as architectural
context; no AGPL ROMM wrapper code is included. This one is a curated,
renamed subset of upstream's `js/` files (not a full checkout) rather than a
submodule, because Go's `go:embed` cannot reach outside the package directory
it lives in; a submodule pinned elsewhere in the tree could not be embedded
here at all.

The optional CIA-decryption lifecycle references
**Batch-CIA-3DS-Decryptor-Redux** at commit `267592eb40d603c30086e214f180e9cf88d01ed5`.
That repository combines a batch script, seed database, and unsigned binaries
from several projects but provides no top-level redistribution license. ROM
Relay therefore includes none of those files. Operators who independently
install the bundle under `internal/drivers/cia-3ds-decryptor` are responsible
for reviewing its upstream terms and provenance.

Optional metadata identification downloads selected DAT files from the
**Libretro Database**. ROM Relay contains an original Go parser and does not
copy RetroArch/Ludo database parser code or redistribute the DAT collection in
the executable. Cached data retains its upstream provenance; Libretro's
repository and its constituent imported datasets may carry different notices,
documented by the upstream DAT headers and repository.

Optional richer metadata uses the **IGDB API** with operator-supplied Twitch
application credentials. No IGDB dataset, credential, or access token is
distributed with ROM Relay. Cached images and metadata remain subject to the
operator's applicable IGDB/Twitch terms.

Pokémon save parsing uses **PKHeX.Core** by Kurt (kwsch) et al., run as an
isolated .NET sidecar (`sidecar/pkhex`). PKHeX is distributed under the GPL-3.0
license; ROM Relay calls it over localhost and does not modify or statically link
it into the Go application. See <https://github.com/kwsch/PKHeX>.

The Living Dex uses the front-default and shiny Pokémon sprite set from
**PokéAPI/sprites** (<https://github.com/PokeAPI/sprites>), vendored under
`internal/dex/assets/sprites/pokemon` and served locally rather than hot-linked.
The sprite images depict Pokémon that are trademarks and copyright of Nintendo,
Game Freak, and The Pokémon Company; they are included only for personal,
non-commercial Pokédex display in this self-hosted tool.

The PokeHome editor's held-item icons use the same **PokéAPI/sprites** item
icon set, vendored under `internal/dex/assets/sprites/items` and served
locally rather than hot-linked, under the same non-commercial terms as the
Pokémon sprites above.
