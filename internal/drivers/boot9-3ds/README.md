# Private 3DS boot9 dump

Place one user-owned retail dump here beside the deployed executable tree:

- `boot9.bin`, or
- `boot9_prot.bin`

ROM Relay discovers this fixed directory automatically. The file is required
only for Original-NCCH-encrypted `.3ds`/`.cci` conversion, is validated against
the known retail SHA-256, and is never served or copied. Dump files are ignored
by Git and are not distributed with ROM Relay.
