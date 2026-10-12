#!/usr/bin/env bash
# Fill sebab-bin's sha256sums from a release's signed checksums.txt.
# Usage: packaging/aur/update-checksums.sh 0.1.0
# Requires the maintainer's public key in your GPG keyring.
set -euo pipefail
ver="${1:?usage: $0 VERSION}"
base="https://github.com/yoockh/sebab/releases/download/v${ver}"
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
curl -fsSL -o "$dir/checksums.txt" "$base/checksums.txt"
curl -fsSL -o "$dir/checksums.txt.sig" "$base/checksums.txt.sig"
gpg --verify "$dir/checksums.txt.sig" "$dir/checksums.txt"
amd64=$(awk -v f="sebab_${ver}_linux_amd64.tar.gz" '$2==f{print $1}' "$dir/checksums.txt")
arm64=$(awk -v f="sebab_${ver}_linux_arm64.tar.gz" '$2==f{print $1}' "$dir/checksums.txt")
[[ -n "$amd64" && -n "$arm64" ]] || { echo "checksums not found for $ver" >&2; exit 1; }
pkgbuild="$(dirname "$0")/sebab-bin/PKGBUILD"
sed -i -e "s/^pkgver=.*/pkgver=${ver}/" -e "s/^pkgrel=.*/pkgrel=1/" \
  -e "s/^sha256sums_x86_64=.*/sha256sums_x86_64=('${amd64}')/" \
  -e "s/^sha256sums_aarch64=.*/sha256sums_aarch64=('${arm64}')/" "$pkgbuild"
(cd "$(dirname "$pkgbuild")" && makepkg --printsrcinfo > .SRCINFO)
echo "Updated $pkgbuild for $ver. Review, then publish to the AUR by hand."
