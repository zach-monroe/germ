#!/bin/sh
# Install germ from the latest GitHub release:
#
#   curl -fsSL https://raw.githubusercontent.com/zach-monroe/germ/main/install.sh | sh
#
# Options (environment variables):
#   GERM_VERSION=v0.1.0          a specific release (default: latest)
#   GERM_INSTALL_DIR=/some/bin   where to put the binaries (default: ~/.local/bin)
set -eu

repo="zach-monroe/germ"
dir="${GERM_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '\033[1;34m◆\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "germ runs on Linux (with X11 or XWayland)."
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 ;;
  *) die "no prebuilt germ for $(uname -m); build from source: go install github.com/$repo/cmd/germ@latest" ;;
esac
command -v curl >/dev/null || die "curl is required"
command -v tar >/dev/null || die "tar is required"

asset="germ-linux-$arch.tar.gz"
if [ -n "${GERM_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$GERM_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi

base="${GERM_BASE_URL:-$base}" # for testing
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "downloading $asset"
curl -fSL --progress-bar -o "$tmp/$asset" "$base/$asset" || die "download failed ($base/$asset)"
if curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" 2>/dev/null; then
  if command -v sha256sum >/dev/null; then
    (cd "$tmp" && grep " $asset\$" SHA256SUMS | sha256sum -c --quiet -) || die "checksum mismatch; not installing"
  fi
else
  warn "no checksums published; skipping verification"
fi
tar -C "$tmp" -xzf "$tmp/$asset"
src="$tmp/germ-linux-$arch"

mkdir -p "$dir"
install -m 755 "$src/germ" "$dir/germ"
install -m 755 "$src/germ-relay" "$dir/germ-relay"
say "installed $("$dir/germ" version) to $dir"

# App launcher entry, pointing at this install.
apps="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
mkdir -p "$apps"
sed "s|^Exec=germ\$|Exec=$dir/germ|" "$src/germ.desktop" > "$apps/germ.desktop"
# germ:// invite links open germ.
"$dir/germ" setup >/dev/null 2>&1 || true
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/germ"
[ -f "${XDG_CONFIG_HOME:-$HOME/.config}/germ/config.example.toml" ] ||
  cp "$src/config.example.toml" "${XDG_CONFIG_HOME:-$HOME/.config}/germ/config.example.toml"

# Things germ uses at runtime.
command -v tmux >/dev/null || warn "tmux isn't installed: germ works without it, but tabs-as-tmux, session sharing, and reattaching need it"
command -v fc-match >/dev/null || warn "fontconfig (fc-match) isn't installed: germ uses it to find fonts"
command -v xclip >/dev/null || command -v wl-paste >/dev/null || warn "install xclip (X11) or wl-clipboard (Wayland) for image paste and the primary selection"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) warn "$dir isn't on your PATH; add it, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
say "done. Run: germ    (or join a session: germ join '<link>')"
