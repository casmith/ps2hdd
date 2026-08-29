# VCD header fixtures

`*.header.bin` files are the first 2048 bytes of VCD images produced by the
reference converter, **cue2pops v2.0** (`github.com/makefu/cue2pops-linux`,
`cue2pops.c`, built with `gcc -O1`), from the matching cuesheet in
`testdata/cue/` and a synthetic MODE2/2352 BIN.

The remaining 0x100000 - 2048 bytes of each reference header are all zero,
which was verified when the fixtures were captured, so 2048 bytes is the whole
of the non-zero header.

BIN sizes used to produce them:

| fixture     | cuesheet         | BIN sectors | BIN bytes  |
|-------------|------------------|-------------|------------|
| multitrack  | multitrack.cue   | 36000       | 84672000   |
| single      | single.cue       | 9000        | 21168000   |
| cdrwin      | cdrwin.cue       | 36000       | 84672000   |

`internal/platform/ps1` must reproduce these byte for byte. They are the
contract that ps2hdd's native converter stays compatible with the converter the
PS2 homebrew community uses.
