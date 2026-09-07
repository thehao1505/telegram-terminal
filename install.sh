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

echo "==> User chạy bot: $RUN_USER"

# 1) Binary: dùng bản đã build sẵn nếu có, không thì build bằng go.
if [[ -x "$BIN_SRC_DIR/telegram-terminal" ]]; then
  echo "==> Dùng binary có sẵn: $BIN_SRC_DIR/telegram-terminal"
  install -m 0755 "$BIN_SRC_DIR/telegram-terminal" "$BIN_DST"
elif command -v go >/dev/null 2>&1; then
  echo "==> Build bằng Go…"
  ( cd "$BIN_SRC_DIR" && CGO_ENABLED=0 go build -ldflags "-s -w" -o telegram-terminal . )
  install -m 0755 "$BIN_SRC_DIR/telegram-terminal" "$BIN_DST"
else
  echo "Không tìm thấy binary 'telegram-terminal' và cũng chưa cài Go." >&2
  echo "Hãy build ở máy khác rồi đặt file 'telegram-terminal' cạnh install.sh." >&2
  exit 1
fi
echo "   -> $BIN_DST"

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
