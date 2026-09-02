#!/bin/sh
#
# Install ps2hdd from a GitHub release.
#
#   ./install.sh                                      # latest release
#   ./install.sh v0.7.3                               # a specific version
#   curl -fsSL <this url> | sh                        # without saving it first
#   curl -fsSL <this url> | sh -s -- v0.7.3
#
# Environment:
#   PREFIX          where to install (default /usr/local/bin)
#   PS2HDD_RETRIES  download attempts before giving up (default 10)
#   PS2HDD_BASE_URL download from here instead of GitHub (a mirror, or a
#                   file:// directory)
#
# Two things this does that a curl one-liner does not.
#
# It retries a download that 404s. A release becomes visible a minute or two
# before its binaries are attached to it, so an install run the moment a release
# appears asks for files that do not exist yet. curl's own --retry does not
# cover 404 -- that is a definite answer, not a transient failure -- so the wait
# is here, and it says what it is waiting for.
#
# And it verifies the checksum before installing rather than after, so a
# truncated or substituted download never reaches PATH. Without `-f` curl writes
# the server's error page into the file and exits 0; `install` then marks nine
# bytes of "Not Found" executable, and the first sign of trouble is a shell
# trying to run English.

# POSIX sh, not bash: `curl ... | sh` is what people type, and /bin/sh is dash
# on Debian and Ubuntu, where `set -o pipefail` is a syntax error. Nothing here
# needs it -- every pipeline's failure is caught by checking what came out.
set -eu

# Everything happens inside main, called on the last line, so that a download cut
# halfway defines an incomplete function and never runs it. Piping a script into
# a shell executes each line as it arrives; without this, a connection dropped
# partway through leaves whatever had been read already done.
main() {

  REPO=${PS2HDD_REPO:-casmith/ps2hdd}
  PREFIX=${PREFIX:-/usr/local/bin}
  RETRIES=${PS2HDD_RETRIES:-10}
  VERSION=${1:-latest}

  # $0 is "sh" when piped, so the name is written out rather than derived.
  die() { printf 'ps2hdd install: %s\n' "$*" >&2; exit 1; }
  note() { printf '  %s\n' "$*"; }

  for tool in curl install uname; do
    command -v "$tool" >/dev/null || die "$tool is required"
  done

  # sha256sum is GNU; the BSD and openssl spellings are accepted so the script is
  # not the reason an install fails on an unusual box.
  sha256_of() {
    if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1
    elif command -v openssl >/dev/null; then openssl dgst -sha256 "$1" | awk '{print $NF}'
    else die "no sha256 tool found (looked for sha256sum, shasum, openssl)"
    fi
  }

  case "$(uname -s)" in
    Linux) ;;
    *) die "ps2hdd is Linux-only: it reads raw block devices and mounts PFS" ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "no binary for $(uname -m); build from source with 'go build ./cmd/ps2hdd'" ;;
  esac

  # PS2HDD_BASE_URL points the download somewhere else: a mirror, or a local
  # directory for testing the checksum and retry paths without a network.
  if [ -n "${PS2HDD_BASE_URL:-}" ]; then
    base=$PS2HDD_BASE_URL
  elif [ "$VERSION" = latest ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  asset="ps2hdd-linux-$arch"

  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT

  # fetch retries a 404 as well as a transient failure, because during the window
  # between a release appearing and its binaries being attached the answer really
  # is "not yet".
  fetch() {
    local url=$1 dest=$2 attempt=1
    while :; do
      # Quiet while there are attempts left: curl's "404" on a release whose
      # binaries have not landed yet is expected, and the note below says so in
      # words. The last attempt keeps curl's own message, which is the one worth
      # reading when it really has failed.
      if [ "$attempt" -ge "$RETRIES" ]; then
        curl -fsSL --retry 3 --retry-delay 2 -o "$dest" "$url" && return 0
        die "could not download ${url##*/} after $attempt attempts"
      fi
      if curl -fsL --retry 3 --retry-delay 2 -o "$dest" "$url"; then
        return 0
      fi
      note "${url##*/} is not there yet (attempt $attempt of $RETRIES); waiting 15s"
      attempt=$((attempt + 1))
      sleep 15
    done
  }

  printf 'Installing ps2hdd (%s, %s)\n' "$VERSION" "$arch"
  fetch "$base/$asset" "$work/$asset"
  fetch "$base/SHA256SUMS" "$work/SHA256SUMS"

  want=$(awk -v f="$asset" '$2 == f || $2 == "*"f {print $1}' "$work/SHA256SUMS" | head -1)
  [ -n "$want" ] || die "SHA256SUMS does not list $asset"
  got=$(sha256_of "$work/$asset")
  [ "$want" = "$got" ] || die "checksum mismatch for $asset
    expected $want
    got      $got
  Nothing was installed."
  note "checksum ok"

  chmod +x "$work/$asset"
  reported=$("$work/$asset" --version 2>/dev/null) || die "the downloaded binary does not run"
  note "reports: $reported"

  # sudo only when the destination really cannot be written, so a user with their
  # own PREFIX is never asked for a password they do not need.
  #
  # A directory that does not exist yet is not unwritable: what decides is whether
  # the nearest existing ancestor can be written to. Testing PREFIX itself asks
  # for a password to create ~/bin.
  writable() {
    local d=$1
    while [ ! -e "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do
      d=$(dirname "$d")
    done
    [ -w "$d" ]
  }
  sudo=""
  if ! writable "$PREFIX"; then
    command -v sudo >/dev/null || die "$PREFIX is not writable and sudo is not available"
    sudo=sudo
  fi
  $sudo install -d "$PREFIX"
  $sudo install -m0755 "$work/$asset" "$PREFIX/ps2hdd"
  note "installed to $PREFIX/ps2hdd"

  # /usr/local/bin is not incidental: ps2hdd needs raw block device access, so it
  # is normally run under sudo, and sudo's secure_path includes neither
  # ~/.local/bin nor Homebrew.
  case ":$PATH:" in
    *":$PREFIX:"*) ;;
    *) note "note: $PREFIX is not on your PATH" ;;
  esac
  printf 'Done. Run `ps2hdd doctor` to check what else is needed.\n'
}

main "$@"
