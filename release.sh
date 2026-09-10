#!/usr/bin/env bash
# Đóng gói một bản phát hành: build cả amd64 + arm64, nhúng số version vào
# binary, gói tar.gz kèm SHA256SUMS trong dist/.
#
# Chạy:  ./release.sh v0.1.0
#        ./release.sh            # lấy tag hiện tại của git
#
# Số version nhúng vào binary chính là mốc để bot so với GitHub Releases.
# Tag trên GitHub phải trùng đúng chuỗi này, không thì bot không so được và
# sẽ im lặng (xem parseVersion trong update.go).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
VER="${1:-$(git describe --tags --exact-match 2>/dev/null || true)}"

if [[ -z "$VER" ]]; then
  echo "Chưa có version. Truyền vào: ./release.sh v0.1.0" >&2
  echo "(hoặc git tag -a v0.1.0 -m … rồi chạy lại)" >&2
  exit 1
fi
if [[ ! "$VER" =~ ^v?[0-9]+(\.[0-9]+){0,2}(-[0-9A-Za-z.]+)?$ ]]; then
  echo "Version '$VER' không đúng dạng vX.Y.Z — bot sẽ không so sánh được." >&2
  exit 1
fi

echo "==> Phát hành $VER"
rm -rf dist
for a in amd64 arm64; do
  d="dist/tt-$a"
  mkdir -p "$d"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" go build -trimpath \
    -ldflags "-s -w -X main.version=$VER" -o "$d/telegram-terminal" .
  cp install.sh config.example.json telegram-terminal.service "$d/"
  tar -C dist -czf "dist/tt-$a.tar.gz" "tt-$a"
  echo "   -> dist/tt-$a.tar.gz"
done
( cd dist && sha256sum tt-*.tar.gz > SHA256SUMS )

echo
echo "Kiểm tra: dist/tt-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')/telegram-terminal -version"
echo
echo "Đưa lên GitHub (tag phải trùng $VER):"
echo "  git tag -a $VER -m \"$VER\" && git push origin $VER"
echo "  gh release create $VER dist/tt-*.tar.gz dist/SHA256SUMS -t $VER --generate-notes"
