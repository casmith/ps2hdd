# Installing the external tools

ps2hdd needs no external tools to *read* a PS2 HDD: it parses the APA
partition table and the HDLoader game headers itself. A machine with none of
these installed still gets a working read-only browser — `detect`, `status`,
`list`, `info` and the whole interface.

Two tools are needed to *write*:

| Tool | Needed for | Without it |
|---|---|---|
| `hdl_dump` | installing and removing PS2 games | reading still works |
| `pfsfuse` | reaching `+OPL` and `__.POPS` | no artwork, no PS1 games |
| `fusermount3` | releasing PFS mounts | ships with `fuse3` |
| `lsblk` | `detect`, device identity | ships with `util-linux`; needs a build with `libudev` |
| `7z` | reading and installing archived sources | `p7zip`; only needed for `.7z`/`.zip`/`.rar` libraries |

`ps2hdd doctor` reports which are present and what each one is for.

---

## A trap on any machine with Homebrew

If you have Homebrew for Linux, its `pkg-config` shadows the system one and
searches only Homebrew's own directories:

```console
$ pkg-config --variable pc_path pkg-config
/home/linuxbrew/.linuxbrew/lib/pkgconfig:...        # note: no /usr/lib/pkgconfig
```

System libraries are then invisible to any source build, and meson fails with
`Dependency "fuse" not found` even though FUSE is installed. Every build
command below sets `PKG_CONFIG_PATH=/usr/lib/pkgconfig` to work around it. It
is harmless if you do not have Homebrew.

---

## pfsfuse

`pfsfuse` is part of `pfsshell` upstream. Two things are easy to miss: it is
**off by default** in the build, and it wants FUSE **2** (pkg-config name
`fuse`, not `fuse3`) even though the mounts are released with `fusermount3`.

### Arch / Manjaro

```sh
sudo pacman -S --needed base-devel git meson ninja fuse2 fuse3

git clone --recursive https://github.com/ps2homebrew/pfsshell.git
cd pfsshell
PKG_CONFIG_PATH=/usr/lib/pkgconfig meson setup build -Denable_pfsfuse=true
PKG_CONFIG_PATH=/usr/lib/pkgconfig meson compile -C build
sudo meson install -C build          # installs to /usr/local/bin
```

The AUR has `pfsfuse-git`, but it is orphaned and was last updated in 2022.
Building from source is the better bet.

### Debian / Ubuntu

```sh
sudo apt install build-essential git meson ninja-build libfuse-dev fuse3

git clone --recursive https://github.com/ps2homebrew/pfsshell.git
cd pfsshell
meson setup build -Denable_pfsfuse=true
meson compile -C build
sudo meson install -C build
```

`libfuse-dev` is the FUSE 2 development package; `libfuse3-dev` will not
satisfy the dependency.

### Fedora

```sh
sudo dnf install @development-tools git meson ninja-build fuse-devel fuse3

git clone --recursive https://github.com/ps2homebrew/pfsshell.git
cd pfsshell
meson setup build -Denable_pfsfuse=true
meson compile -C build
sudo meson install -C build
```

### Things that go wrong

**`meson setup` succeeds but there is no `pfsfuse` binary.** You omitted
`-Denable_pfsfuse=true`. A plain `meson setup` builds `pfsshell` and `pfs2tar`
only, with no warning about the missing third binary.

**`Dependency "fuse" not found (tried pkg-config and cmake)`.** Either FUSE 2
development files are missing, or Homebrew's `pkg-config` is shadowing the
system one — see above.

**Submodule errors during the build.** The clone needs `--recursive`; it pulls
in `ps2sdk`. If you already cloned without it:
`git submodule update --init --recursive`.

You also get `pfsshell` and `pfs2tar` out of the same build. `pfsshell` is
optional for ps2hdd — `doctor` uses it as a cross-check on the partition list.

---

## hdl_dump

A plain Makefile, no pkg-config, no external libraries.

```sh
git clone --recursive https://github.com/ps2homebrew/hdl-dump.git
cd hdl-dump
make RELEASE=yes
sudo install -m0755 hdl_dump /usr/local/bin/
```

That is the same on every distribution; you need only a C compiler, `make` and
`git`. The code is C89 and modern compilers emit warnings (a
`-Wstringop-overflow` in `iin_gi.c`, among others). Warnings are expected; it
builds and links.

Arch and Manjaro have `hdl-dump-git` in the AUR, which works if you already
have `base-devel`. The Makefile is less trouble.

### What hdl_dump cannot do

**It cannot read a disk image file.** It needs a real block device, a `hdd1:`
style target, or a networked PS2. Passing a path to an image gives
`Input or output is unsupported`, and the `dbg:` moniker is a debug format
with its own sector remapping, not a raw image reader.

ps2hdd has no such restriction — it reads images directly, which is how the
test suite works and how you can rehearse against a copy of your drive. To
point hdl_dump at an image, map it to a loop device first:

```sh
udisksctl loop-setup -r -f ps2.img       # -r is read-only; usually no sudo needed
sudo hdl_dump hdl_toc /dev/loop1
udisksctl loop-delete -b /dev/loop1
```

Reading `/dev/loopN` needs root or membership of the `disk` group.

---

## Checking the install

```sh
ps2hdd doctor
```

Green looks like this:

```
External tools
TOOL         STATUS  NEEDED FOR
lsblk        OK      enumerating block devices for `detect`
hdl_dump     OK      installing and removing PS2 games
pfsfuse      OK      mounting +OPL and __.POPS
fusermount3  OK      unmounting PFS partitions
pfsshell     OK      cross-checking the partition list
```

If a tool is somewhere unusual, point at it rather than moving it:

```sh
ps2hdd config set tools.hdl_dump /opt/ps2/hdl_dump
ps2hdd config set tools.pfsfuse  /opt/ps2/pfsfuse
```

---

## Not needed: cue2pops

BIN/CUE to VCD conversion is built into ps2hdd and is verified byte-for-byte
against cue2pops v2.0 by the test suite, so there is nothing to install. If you
want the original anyway — for its optional per-game patches, which ps2hdd
deliberately does not apply — set `tools.cue2pops` and it will be used instead.

---

## Cross-checking against hdl_dump

Once both are installed you can verify that ps2hdd's native reader agrees with
the reference implementation:

```sh
sudo scripts/crosscheck-hdl.sh /dev/disk/by-id/ata-YOUR_DRIVE
```

It is read-only. See `docs/hardware-validation.md`; a disagreement there means
no write should follow.
