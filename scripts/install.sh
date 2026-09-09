#!/usr/bin/env bash
# CLI only: never installs Skills or changes shell/SSH configuration.
set -euo pipefail

fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
version=${1:-latest}
[[ $# -le 1 ]] || fail 'usage: bash install.sh [latest|vX.Y.Z]'
[[ "$version" = latest || "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || fail 'version must be latest or vX.Y.Z'
case "$(uname -s)" in
  Darwin) os=darwin;;
  Linux) os=linux;;
  *) fail 'supported systems: macOS and Linux';;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64;;
  x86_64|amd64) arch=amd64;;
  *) fail 'supported architectures: arm64 and amd64';;
esac
command -v curl >/dev/null || fail 'curl is required'
if command -v sha256sum >/dev/null; then
  hash() { sha256sum "$1"; }
elif command -v shasum >/dev/null; then
  hash() { shasum -a 256 "$1"; }
else
  fail 'sha256sum or shasum is required'
fi
install_dir=${VPSCTL_INSTALL_DIR:-"$HOME/.local/bin"}
[[ "$install_dir" = /* ]] || fail 'VPSCTL_INSTALL_DIR must be absolute'
base=https://github.com/Xichun123/vpsctl/releases
fetch() { curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 "$@"; }
if [[ "$version" = latest ]]; then
  # Resolve once so a concurrent release cannot mix binary/checksum versions.
  resolved=$(fetch --output /dev/null --write-out '%{url_effective}' "$base/latest")
  resolved=${resolved%%\?*}
  resolved=${resolved%/}
  [[ "$resolved" = "$base/tag/"* ]] || fail 'cannot resolve latest release'
  version=${resolved#"$base/tag/"}
  [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || fail 'latest release has an unsupported tag'
fi
asset=vpsctl_${os}_${arch}
url=$base/download/$version
work=$(mktemp -d)
staged=''
cleanup() { rm -rf -- "$work"; if [[ -n "$staged" ]]; then rm -f -- "$staged"; fi; }
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
fetch --output "$work/$asset" "$url/$asset"
fetch --output "$work/checksums.txt" "$url/checksums.txt"
expected=$(awk -v name="$asset" '$2 == name {print $1; n++} END {if (n != 1) exit 1}' "$work/checksums.txt") || fail 'missing or duplicate checksum'
[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || fail 'invalid SHA-256 checksum'
actual=$(hash "$work/$asset")
[[ "${actual%% *}" = "$expected" ]] || fail 'SHA-256 mismatch; existing CLI was not changed'
mkdir -p -- "$install_dir"
target=$install_dir/vpsctl
[[ ! -L "$target" && ( ! -e "$target" || -f "$target" ) ]] || fail 'destination must be a regular file, not a directory or symlink'
# Stage on the destination filesystem so a failed download never damages the CLI.
staged=$(mktemp "$install_dir/.vpsctl-install.XXXXXXXX")
cp -- "$work/$asset" "$staged"
chmod 755 "$staged"
# mv must not interpret a directory as a container; user owns this install dir.
if [[ "$os" = linux ]]; then
  mv -fT -- "$staged" "$target"
else
  [[ ! -d "$target" ]] || fail 'destination became a directory'
  mv -fh -- "$staged" "$target"
fi
staged=''
printf 'Installed vpsctl %s to %s\n' "$version" "$target"
printf 'Ensure %s is in the PATH used by your terminal and Agent.\n' "$install_dir"
printf 'Skill installation is separate; no Skill or shell configuration was changed.\n'
