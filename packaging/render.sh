#!/bin/sh
# Fill in package templates for a release.
#   packaging/render.sh 0.1.0 dist/SHA256SUMS
set -eu
version=$1
sums=$2
here=$(cd "$(dirname "$0")" && pwd)
sha() { awk -v f="germ-linux-$1.tar.gz" '$2 == f || $2 == "*"f { print $1 }' "$sums"; }
x86=$(sha x86_64)
arm=$(sha aarch64)
[ -n "$x86" ] && [ -n "$arm" ] || { echo "render.sh: checksums missing from $sums" >&2; exit 1; }
for t in aur/germ-bin/PKGBUILD homebrew/germ.rb; do
  sed -e "s/@VERSION@/$version/g" -e "s/@SHA_X86_64@/$x86/g" -e "s/@SHA_AARCH64@/$arm/g" \
    "$here/$t.in" > "$here/$t"
done
sed -i "s/^pkgver=.*/pkgver=$version/" "$here/aur/germ/PKGBUILD"
echo "rendered packages for $version"
