# Optional CIA decryption driver

ROM Relay can prepare an encrypted CIA for compatible mobile emulators through
the workflow from Batch-CIA-3DS-Decryptor-Redux. The upstream repository does
not provide a repository-level redistribution license and its included Windows
executables are unsigned, so ROM Relay does not bundle them.

To enable the feature, obtain the bundle yourself from:

https://github.com/xxmichibxx/Batch-CIA-3DS-Decryptor-Redux

Place these files under the drivers directory (`DRIVERS_DIR` in `.env`; the
decryptor's Windows executables do not run in the Linux container, so this
feature needs a Windows host running the app natively):

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

## Verifying your download

The batch script itself is plain, readable text with no network calls or
persistence, read it before running it, that costs nothing. The three
`bin/*.exe` are unsigned, so ROM Relay checked the `main` branch as of
2026-07-19 by hand: full script read, PE header/signature inspection, and a
string scan for network/injection/persistence indicators. Nothing suspicious
turned up (`decrypt.exe` is a PyInstaller build of a Python NCCH decryptor
using pycryptodome; `ctrtool.exe`/`makerom.exe` match the well-known
Project_CTR-lineage tools). This is static due diligence, not a sandboxed or
multi-engine AV scan, verify independently if that matters to you. SHA-256 of
that checked revision, so you can confirm you got the same bytes:

```text
ctrtool.exe   921ab18aa2e0ca4ed1e7537f7dc1803a6eaae7c7251a678d6704111c7cb912e5
decrypt.exe   35506bc9c5610cd41c7a6e6c377b01bca30217e61d5b5e4842156120fa9e59a0
makerom.exe   db810fb9c3d41ecaefa8fcbd13140ecafe9b95e6303cd9244c12a2bca70fa938
seeddb.bin    ccdcea5e4465194158737462436ec10ae48669dd961902282ac2861e260d03c9
```
