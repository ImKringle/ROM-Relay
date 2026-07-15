# Optional CIA decryption driver

ROM Relay can prepare an encrypted CIA for compatible mobile emulators through
the workflow from Batch-CIA-3DS-Decryptor-Redux. The upstream repository does
not provide a repository-level redistribution license and its included Windows
executables are unsigned, so ROM Relay does not bundle them.

To enable the feature, obtain the bundle yourself from:

https://github.com/xxmichibxx/Batch-CIA-3DS-Decryptor-Redux

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
