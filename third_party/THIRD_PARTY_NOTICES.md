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
context; no AGPL ROMM wrapper code is included.

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
