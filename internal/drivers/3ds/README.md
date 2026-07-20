# Private 3DS crypto material

Place user-owned key/seed material under this fixed path inside the drivers
directory (`DRIVERS_DIR` in `.env` for the Docker deployment):

- `boot9.bin` or `boot9_prot.bin`, a retail boot9 dump. Required only for
  Original-NCCH-encrypted `.3ds`/`.cci` conversion (`internal/converter`) and
  for the NDecrypt sidecar's non-seed titles. ROM Relay validates it against
  the known retail SHA-256 before use; it is never served or copied.
- `seeddb.bin`, the public per-title seed database. Required by the NDecrypt
  sidecar for "9.x seed crypto" titles (the 3DS content flag that boot9-derived
  keys alone cannot decrypt). Not sensitive, but still not distributed with
  ROM Relay, grab it from the same tools that ship it (e.g. the Batch CIA 3DS
  Decryptor Redux bundle, see `../cia-3ds-decryptor/README.md`).

ROM Relay discovers this fixed directory automatically. Files here are ignored
by Git and are not distributed with ROM Relay.
