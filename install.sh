#!/usr/bin/env bash
# Cài telegram-terminal lên một máy Ubuntu.
# Chạy: sudo ./install.sh [user_chạy_bot]
# Mặc định user = người gọi sudo (SUDO_USER).
set -euo pipefail

RUN_USER="${1:-${SUDO_USER:-$USER}}"
BIN_SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DST="/usr/local/bin/telegram-terminal"
CFG_DIR="/etc/telegram-terminal"
CFG_FILE="$CFG_DIR/config.json"
UNIT_DST="/etc/systemd/system/telegram-terminal.service"

if [[ $EUID -ne 0 ]]; then
  echo "Cần chạy bằng sudo/root." >&2
  exit 1
fi

# host_arch: kiến trúc của máy này, theo cách gọi của Go (GOARCH).
host_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo amd64 ;;
    aarch64|arm64)  echo arm64 ;;
    armv7l|armv6l)  echo arm ;;
    riscv64)        echo riscv64 ;;
    *)              echo "?" ;;
  esac
}

# elf_arch <file>: đọc e_machine trong ELF header (offset 18, 2 byte little-endian).
# Dùng để chặn trường hợp copy binary sai kiến trúc -> systemd báo
# "Exec format error" (status 203/EXEC) và restart vô hạn.
elf_arch() {
  local m
  m=$(od -An -tx1 -j18 -N2 "$1" 2>/dev/null | tr -d ' \n')
  case "$m" in
    3e00) echo amd64 ;;
    b700) echo arm64 ;;
    2800) echo arm ;;
    f300) echo riscv64 ;;
    *)    echo "?" ;;
  esac
}

ARCH="$(host_arch)"
echo "==> Kiến trúc máy này: $(uname -m) ($ARCH)"

echo "==> User chạy bot: $RUN_USER"

# 1) Binary: dùng bản build sẵn nếu ĐÚNG kiến trúc, không thì build bằng go.
SRC_BIN="$BIN_SRC_DIR/telegram-terminal"
SRC_ARCH="?"
[[ -f "$SRC_BIN" ]] && SRC_ARCH="$(elf_arch "$SRC_BIN")"

if [[ -f "$SRC_BIN" && "$SRC_ARCH" == "$ARCH" ]]; then
  echo "==> Dùng binary có sẵn ($SRC_ARCH): $SRC_BIN"
  install -m 0755 "$SRC_BIN" "$BIN_DST"
elif command -v go >/dev/null 2>&1; then
  if [[ -f "$SRC_BIN" ]]; then
    echo "==> Bỏ qua binary có sẵn: nó là $SRC_ARCH, máy này cần $ARCH"
  fi
  echo "==> Build bằng Go cho $ARCH…"
  ( cd "$BIN_SRC_DIR" && CGO_ENABLED=0 GOARCH="$ARCH" go build -trimpath -ldflags "-s -w" -o "telegram-terminal.$ARCH" . )
  install -m 0755 "$BIN_SRC_DIR/telegram-terminal.$ARCH" "$BIN_DST"
else
  if [[ -f "$SRC_BIN" ]]; then
    echo "Binary có sẵn là $SRC_ARCH nhưng máy này là $ARCH, và máy chưa cài Go." >&2
    echo "Build ở máy khác rồi copy sang:" >&2
    echo "  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath -ldflags \"-s -w\" -o telegram-terminal ." >&2
  else
    echo "Không tìm thấy binary 'telegram-terminal' và cũng chưa cài Go." >&2
    echo "Hãy build ở máy khác rồi đặt file 'telegram-terminal' cạnh install.sh." >&2
  fi
  exit 1
fi
echo "   -> $BIN_DST ($(elf_arch "$BIN_DST"))"

# 2) Config (không ghi đè nếu đã có).
mkdir -p "$CFG_DIR"
if [[ -f "$CFG_FILE" ]]; then
  echo "==> Giữ nguyên config đã tồn tại: $CFG_FILE"
else
  cp "$BIN_SRC_DIR/config.example.json" "$CFG_FILE"
  chown "$RUN_USER":"$RUN_USER" "$CFG_FILE"
  chmod 0600 "$CFG_FILE"
  echo "==> Đã tạo config mẫu: $CFG_FILE (nhớ sửa bot_token & allowed_user_ids)"
fi

# 3) systemd unit (thay REPLACE_USER).
sed "s/REPLACE_USER/$RUN_USER/g" "$BIN_SRC_DIR/telegram-terminal.service" > "$UNIT_DST"
echo "==> Đã cài unit: $UNIT_DST"

systemctl daemon-reload
echo
echo "Xong. Các bước tiếp theo:"
echo "  1. sudo nano $CFG_FILE   # điền bot_token và allowed_user_ids"
echo "  2. sudo systemctl enable --now telegram-terminal"
echo "  3. journalctl -u telegram-terminal -f   # xem log"
echo
echo "Chưa biết User ID? Cứ start bot rồi nhắn cho bot; nó sẽ trả về ID của bạn."
