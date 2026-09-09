# telegram-terminal

> 🇬🇧 English version (bản chính): **[README.md](README.md)**. Bản tiếng Việt này
> được giữ đồng bộ với bản tiếng Anh.
>
> ⚠️ **Bot mặc định nói tiếng Anh.** Gõ `/lang vi` để đổi sang tiếng Việt cho
> chat đó, hoặc đặt `"language": "vi"` trong config để mọi chat đều tiếng Việt.
> Mọi ví dụ dưới đây là giao diện tiếng Việt. Log của bot luôn là tiếng Anh.

Biến một bot Telegram thành "terminal" cho máy Ubuntu của bạn:

- Gửi lệnh shell → nhận output stream về, **giữ thư mục** giữa các lệnh.
- Gửi prompt → chạy **Claude Code** ngay trên máy đó, câu trả lời **stream theo
  thời gian thực**, và khi Claude cần chạy tool thì **xin phép bằng nút bấm**
  trong Telegram.
- Gửi **ảnh** kèm caption → ảnh đi thẳng vào lượt Claude (screenshot lỗi, ảnh
  thiết kế, biểu đồ…).

Viết bằng Go thuần (chỉ standard library) → build ra **1 binary tĩnh**, copy sang
máy nào cũng chạy, không cần cài runtime.

> ⚠️ Đây là công cụ remote-admin cho **máy của chính bạn**. Ai vào được bot là có
> quyền chạy lệnh tùy ý dưới user chạy bot. Bảo vệ bot token như mật khẩu và LUÔN
> giới hạn `allowed_user_ids`.

**Mục lục**

- [Cài đặt](#cài-đặt)
- [Cấu hình](#cấu-hình)
- [Hướng dẫn sử dụng](#hướng-dẫn-sử-dụng)
  - [Hai chế độ](#hai-chế-độ-shell-và-claude)
  - [Chế độ shell](#chế-độ-shell)
  - [Chế độ Claude](#chế-độ-claude)
  - [Đọc dòng footer](#đọc-dòng-footer)
  - [Gửi ảnh & file](#gửi-ảnh--file)
  - [Quyền: `/perm`](#quyền-perm)
  - [Ngôn ngữ: `/lang`](#ngôn-ngữ-lang)
  - [Phiên: `/session`](#phiên-session)
  - [`/status`](#status--đọc-trạng-thái)
  - [Bảng lệnh đầy đủ](#bảng-lệnh-đầy-đủ)
  - [Kịch bản thường dùng](#kịch-bản-thường-dùng)
  - [Giới hạn & lỗi thường gặp](#giới-hạn--lỗi-thường-gặp)
- [Bên trong: bot nói gì với Claude Code](#bên-trong-bot-nói-gì-với-claude-code)
- [Chạy test](#chạy-test)
- [Bảo mật](#bảo-mật--nên-làm)
- [Gỡ cài](#gỡ-cài)

---

# Cài đặt

## 1. Tạo bot Telegram

1. Nhắn [@BotFather](https://t.me/BotFather) → `/newbot` → lấy **token**.
2. Chưa biết User ID của mình? Cứ cài & chạy bot với `allowed_user_ids` để trống,
   nhắn cho bot — nó trả về User ID của bạn (và từ chối chạy mọi lệnh). Điền ID
   đó vào config rồi khởi động lại.

## 2. Build

Trên một máy có Go (build 1 lần, dùng cho mọi máy Ubuntu amd64):

```bash
cd telegram-terminal
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o telegram-terminal .
```

`-trimpath` để binary không nhúng đường dẫn tuyệt đối của máy build (nếu bạn
commit binary lên repo công khai).

Máy ARM (VD Raspberry Pi, AWS Graviton, Oracle Ampere): đổi `GOARCH=arm64`.
Kiểm tra kiến trúc máy đích bằng `uname -m` (`x86_64` → `amd64`,
`aarch64` → `arm64`). Build sẵn cả hai:

```bash
for a in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" \
    -o dist/telegram-terminal-linux-$a .
done
```

> Binary **không** chạy chéo kiến trúc. Copy bản `amd64` lên máy ARM sẽ khiến
> systemd báo `Exec format error` và restart vô hạn. `install.sh` tự đối chiếu
> kiến trúc của binary với `uname -m`, lệch thì nó build lại (nếu máy có Go)
> hoặc báo lỗi rõ ràng thay vì cài file không chạy được.

Binary không phụ thuộc gì → chỉ cần `scp telegram-terminal user@may:/tmp/`.

## 3. Cài lên máy Ubuntu

Copy cả thư mục (hoặc chỉ `telegram-terminal`, `config.example.json`,
`telegram-terminal.service`, `install.sh`) lên máy rồi:

```bash
sudo ./install.sh            # tự dùng user hiện tại làm user chạy bot
# hoặc chỉ định user:  sudo ./install.sh tenuser
```

`install.sh` đặt binary vào `/usr/local/bin/`, config vào
`/etc/telegram-terminal/config.json` (quyền `0600`, thuộc user chạy bot), và cài
systemd unit chạy dưới **user thường** (không phải root).

## 4. Điền config

```bash
sudo nano /etc/telegram-terminal/config.json   # bot_token + allowed_user_ids
sudo systemctl enable --now telegram-terminal
journalctl -u telegram-terminal -f
```

Log khởi động phải như thế này:

```text
telegram-terminal started on host "server-01" — 1 allowed user(s), claude=true, lang=en, attachments in /tmp/telegram-terminal
```

## 5. Kiểm tra hoạt động

Trong Telegram, nhắn cho bot:

| Gõ | Mong đợi |
|----|----------|
| `/help` | Bảng lệnh, có dòng `Claude: ✅ enabled` (hoặc `✅ đã bật` nếu đã `/lang vi`) |
| `whoami` | Tên user đang chạy bot |
| `/status` | Hostname, chế độ, thư mục, trạng thái phiên Claude |
| `/c chào bạn` | Chữ chảy dần về trong một tin nhắn |

Nếu chế độ Claude báo lỗi, xem
[Giới hạn & lỗi thường gặp](#giới-hạn--lỗi-thường-gặp).

## Cập nhật khi có bản mới

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o telegram-terminal .
sudo install -m 755 ./telegram-terminal /usr/local/bin/telegram-terminal
sudo systemctl restart telegram-terminal
```

> `sudo` cần TTY để nhập mật khẩu — **đừng** gửi lệnh `sudo` qua chính con bot
> (hoặc qua bất kỳ kênh không có terminal), nó sẽ chết ở chỗ hỏi mật khẩu.
> Kiểm tra đã cài đúng bản chưa:
> `sha256sum ./telegram-terminal /usr/local/bin/telegram-terminal`.

---

# Cấu hình

File `/etc/telegram-terminal/config.json`:

| Khóa | Ý nghĩa | Mặc định |
|------|---------|----------|
| `bot_token` | Token từ BotFather (**bắt buộc**) | — |
| `allowed_user_ids` | Danh sách Telegram User ID được phép. Để trống = bot chỉ trả về ID của người nhắn và **không chạy gì** | `[]` |
| `shell` | Shell dùng để chạy lệnh | `/bin/bash` |
| `start_dir` | Thư mục khởi đầu của mỗi chat | HOME của user chạy bot |
| `command_timeout_seconds` | Timeout cho mỗi lệnh shell **và** mỗi lượt Claude; `0` = không giới hạn | `0` |
| `claude_enabled` | Bật chế độ Claude | `false` |
| `claude_bin` | Đường dẫn tới `claude` | `claude` (theo `PATH`) |
| `claude_args` | Args thêm cho `claude`, VD `["--model","opus"]` | `[]` |
| `claude_permission_mode` | Chế độ quyền lúc mở phiên: `manual`, `acceptEdits`, `auto`, `dontAsk`, `plan`, `bypassPermissions` | `manual` |
| `claude_ask_timeout_seconds` | Chờ người dùng bấm nút cho phép bao lâu trước khi **tự động từ chối** | `300` |
| `image_dir` | Nơi lưu ảnh/file gửi từ Telegram | `<temp>/telegram-terminal` |
| `image_max_bytes` | Ảnh nhỏ hơn mức này được **nhúng thẳng** vào lượt Claude; lớn hơn thì chỉ đưa đường dẫn | `3670016` (3,5 MB) |
| `image_keep_hours` | Dọn file đính kèm cũ hơn mức này lúc khởi động; số âm = giữ mãi | `24` |
| `image_default_prompt` | Prompt dùng khi gửi ảnh mà không có caption | theo ngôn ngữ của chat (`Xem file đính kèm.`) |
| `language` | Ngôn ngữ mặc định cho mọi chat: `en` hoặc `vi`. Mỗi chat đổi riêng bằng `/lang` | `en` |

Biến môi trường ghi đè config (tiện cho systemd / secret manager):
`TT_BOT_TOKEN`, `TT_ALLOWED_USER_IDS` (phân tách bằng dấu phẩy), `TT_SHELL`,
`TT_START_DIR`, `TT_CLAUDE_BIN`, `TT_CLAUDE_ENABLED=1`,
`TT_CLAUDE_PERMISSION_MODE`, `TT_IMAGE_DIR`, `TT_IMAGE_MAX_BYTES`, `TT_LANG`.

> `image_max_bytes` mặc định 3,5 MB vì base64 làm dữ liệu nở 4/3 lần, còn API
> Claude chỉ nhận ảnh tối đa 5 MB sau khi mã hóa.

Ba cấu hình mẫu:

```jsonc
// A. Mặc định — an toàn nhất: Claude phải xin phép từng tool
{ "claude_enabled": true, "claude_permission_mode": "manual" }

// B. Đỡ bị hỏi khi sửa code: tự cho sửa file, tool khác vẫn hỏi
{ "claude_enabled": true, "claude_permission_mode": "acceptEdits" }

// C. Không hỏi gì — CHỈ dùng nếu bạn hiểu rủi ro
{ "claude_enabled": true,
  "claude_permission_mode": "bypassPermissions",
  "claude_args": ["--dangerously-skip-permissions"] }
```

Sửa config xong phải `sudo systemctl restart telegram-terminal`.

---

# Hướng dẫn sử dụng

## Hai chế độ: shell và Claude

Mỗi **chat** với bot có một trạng thái riêng gồm: thư mục hiện tại, chế độ đang
dùng, chế độ quyền, và phiên Claude đang mở.

```text
Chế độ shell  (mặc định)     gõ gì cũng thành lệnh shell
Chế độ Claude                 gõ gì cũng thành prompt cho Claude
```

Chuyển qua lại:

```text
/sh        → sang chế độ shell
/c         → sang chế độ Claude
/status    → đang ở chế độ nào
```

Không cần chuyển chế độ vẫn dùng chéo được: `/sh <lệnh>` chạy shell dù đang ở chế
độ Claude, và `/c <prompt>` hỏi Claude dù đang ở chế độ shell.

Mỗi chat chỉ chạy **một việc tại một thời điểm**. Gửi tiếp khi chưa xong sẽ nhận
`⏳ Đang bận chạy lệnh khác. Gõ /cancel để hủy.`

> Nhóm chat: trạng thái tính theo **chat**, không theo người. Cả nhóm dùng chung
> một thư mục, một phiên Claude — ai trong `allowed_user_ids` cũng gõ được.
> Bot đọc tin nhắn **text** và **ảnh/file đính kèm**; video và voice bị bỏ qua.

## Chế độ shell

```text
bạn:  ls -la
bot:  ┌ (khối <pre>)
      total 6276
      drwxr-xr-x  3 user user  4096 Sep  7 14:38 .
      …
      └
      📁 /home/user/project/telegram-terminal
```

**Thư mục được giữ giữa các lệnh** — `cd` hoạt động như terminal thật:

```text
bạn:  cd /var/log
bot:  ✓ (không có output) · 📁 /var/log
bạn:  pwd
bot:  /var/log
```

Những điều cần biết:

- **Chỉ thư mục được giữ, biến môi trường thì không.** Mỗi lệnh chạy trong một
  tiến trình shell mới (`bash -c`), bot chỉ khôi phục `cwd`. `export FOO=1` rồi
  lệnh sau `echo $FOO` sẽ ra rỗng — muốn dùng chung thì viết một dòng:
  `export FOO=1 && echo $FOO`.
- **stdout và stderr trộn chung**, gửi dần về mỗi ~1,2 giây; tin dài tự cắt theo
  giới hạn 4096 ký tự của Telegram, cắt tại đầu dòng cho dễ đọc.
- **Tối đa ~100 KB output mỗi lệnh**, quá thì cắt và báo
  `… (output quá dài, đã cắt bớt)`. Lệnh nào ồn quá thì tự giới hạn:
  `journalctl -u nginx -n 50`, `... | tail -100`.
- Kết thúc, bot ghi phần "tổng kết": `exit <mã>` nếu khác 0,
  `✓ (không có output)` nếu lệnh im lặng, và `📁 <thư mục>` nếu thư mục đã đổi.
- `/cancel` **kill cả process group**, nên tiến trình con cũng chết theo.
- `command_timeout_seconds` > 0 thì lệnh quá lâu sẽ bị hủy với
  `⏱️ hết thời gian, đã hủy`.

**Không chạy được** (vì không có terminal thật):

| Loại | Ví dụ | Làm sao |
|------|-------|---------|
| Lệnh cần nhập liệu | `sudo`, `passwd`, `ssh` hỏi mật khẩu | Cấu hình NOPASSWD hoặc chạy ở terminal thật |
| Trình soạn thảo / TUI | `vim`, `htop`, `less` | `cat`, `ps aux`, `sed -i` |
| Lệnh chạy vô hạn | `tail -f`, `journalctl -f` | Thêm `-n 50`, hoặc `timeout 10 tail -f …` |

## Chế độ Claude

```text
bạn:  /c đọc main.go và nói xem hàm nào dài nhất
bot:  📖 Read
      /home/user/project/telegram-terminal/main.go
      ⏳ (chữ chảy dần vào một tin nhắn, sửa mỗi ~1,5s)
      Hàm dài nhất là handle() — khoảng 90 dòng…
      — 7.4s · $0.0231 · 3 bước
```

Bạn sẽ thấy 4 loại tin nhắn:

| Dạng | Nghĩa |
|------|-------|
| Chữ chảy dần | Câu trả lời của Claude, gom vào **một** tin nhắn và sửa dần (~1,5s/lần). Dài quá 3500 ký tự thì tự mở tin mới |
| `💻 Bash` / `✍️ Write` / `📖 Read` / `🔍 Grep` / `🌐 WebFetch` / `🤖 Task` … | Claude vừa gọi tool đó, kèm phần đáng đọc nhất (lệnh, đường dẫn, pattern…) |
| `⚠️ tool lỗi:` | Tool chạy nhưng lỗi — tool thành công thì bot im lặng cho khỏi ồn |
| `— 7.4s · $0.0231 · 3 bước` | Kết thúc lượt: xem [Đọc dòng footer](#đọc-dòng-footer) |

### Đọc dòng footer

```text
— 16m41s · $0.87 · phiên $32.31 · 5 bước
   │         │       │              └ số lượt model bên trong một prompt (num_turns)
   │         │       └ tổng cộng dồn của cả tiến trình claude
   │         └ chi phí của riêng prompt này
   └ thời gian treo tường của lượt
```

Hai con số dễ đọc sai, nên bot tách rõ:

- **Chi phí.** Event `result` của Claude Code trả `total_cost_usd` là **tổng cộng
  dồn**: "cumulative across turns in streaming-input sessions — each result
  carries the running total so far". Bot giữ một tiến trình `claude` sống lâu
  cho mỗi chat, nên in thẳng con số đó thì prompt nào cũng trông như đắt dần.
  Bot trừ tổng của lượt trước để ra chi phí từng lượt, và ghi tổng phiên riêng
  kèm nhãn. Resume phiên hoặc `/clear` làm tổng reset — bot thấy tổng nhỏ lại
  thì coi tổng mới là chi phí của lượt đó. Đây là số **ước tính** theo giá niêm
  yết, không phải hóa đơn.
- **Thời gian.** Là đồng hồ treo tường của lượt, nên **gồm cả thời gian chờ bạn
  bấm Cho phép/Từ chối** — yêu cầu quyền xảy ra giữa lượt. Lượt 16 phút thường
  nghĩa là nút bấm nằm đó không ai trả lời, chứ không phải model nghĩ lâu.
  (Event có `duration_api_ms` nhưng nó cộng cả request song song nên có thể lớn
  hơn wall-clock, bot không hiển thị.)

Tổng phiên và số bước được bỏ đi khi chúng không nói thêm gì — tức lượt đầu của
phiên, và prompt chỉ mất một lượt model.

### Gửi ảnh & file

Đang ở chế độ Claude thì cứ gửi thẳng ảnh vào chat — **caption chính là prompt**:

```text
bạn:  [ảnh screenshot lỗi]  caption: lỗi này do đâu?
bot:  ⏳ Cái stack trace trong ảnh cho thấy nil pointer ở sessions.go:132…
      — 5.1s · $0.0184 · 2 bước
```

- Ảnh được **nhúng trực tiếp** vào lượt (base64) nên Claude thấy ngay, không
  phải xin quyền tool nào. Bot tự chọn bản kích cỡ nét nhất mà Telegram gửi kèm
  còn vừa `image_max_bytes`.
- Gửi **nhiều ảnh một lần** (album) cũng được: bot chờ ~1,4s gom hết rồi chạy
  **một** lượt duy nhất. Caption của ảnh nào trong album cũng được tính.
- Không caption → dùng `image_default_prompt`.
- Ảnh gốc luôn được lưu vào `image_dir/<chat_id>/` và đường dẫn được nêu trong
  prompt, nên có thể nhắc Claude quay lại đọc/crop ảnh đó ở các lượt sau.
- **File không phải ảnh** (`.csv`, `.log`, `.pdf`…) và **ảnh quá lớn**: bot lưu
  ra đĩa rồi đưa đường dẫn để Claude tự đọc bằng tool `Read` — bước này sẽ xin
  quyền như mọi tool khác.
- Đang ở **chế độ shell** thì bot chỉ lưu file và trả lời đường dẫn, không gọi
  Claude. Gõ `/c` rồi gửi lại nếu muốn Claude xem.
- Video, GIF động, tin nhắn thoại và audio chưa hỗ trợ — bot báo lại chứ không
  im lặng.

### Xin quyền bằng nút bấm

Khi Claude cần chạy tool mà chưa có quyền, bot gửi:

```text
🔐 Claude xin phép dùng ✍️ Write
hello.txt
┌ /home/user/hello.txt
│ ---
│ xin chao
└
tự động từ chối sau 5 phút

[✅ Cho phép]  [❌ Từ chối]
[⏩ Cho phép luôn (phiên này)]
```

| Nút | Tác dụng |
|-----|----------|
| ✅ **Cho phép** | Chỉ lần này |
| ⏩ **Cho phép luôn (phiên này)** | Nhận luôn gợi ý quyền của Claude Code (VD chuyển sang `acceptEdits`), nên việc tương tự sau đó không hỏi lại. Hết hiệu lực khi đóng phiên |
| ❌ **Từ chối** | Claude nhận `deny` và tự tìm cách khác / báo lại |

- Bấm xong, tin nhắn đó **mất nút** và ghi kết quả (`✅ Đã cho phép.`).
- Không ai bấm trong `claude_ask_timeout_seconds` (mặc định 5 phút) → **tự động
  từ chối**, tin nhắn ghi `❌ Đã từ chối — hết thời gian chờ.`
- Trong lúc chờ bấm, chat ở trạng thái "đang chạy" nên không gửi prompt mới
  được — nhưng **bấm nút thì vẫn được**.
- `/cancel` lúc đang chờ: bot gửi `interrupt` cho Claude và từ chối các yêu cầu
  đang treo; chờ 10 giây không dừng thì kill tiến trình.
- Nội dung hiển thị được rút gọn: nội dung file xem trước 400 ký tự, phần chi
  tiết tool tối đa 800 ký tự.

### Claude hỏi lại bằng câu hỏi thường

Nếu Claude hỏi "dùng phương án A hay B?" (câu hỏi thường, không phải xin quyền),
cứ **nhắn tiếp** — tin nhắn sau đi vào đúng phiên đang mở, đúng ngữ cảnh. Không
cần `/c` lại nếu đang ở chế độ Claude.

## Quyền: `/perm`

`/perm` (không tham số) hiện chế độ hiện tại kèm nút bấm; `/perm auto` đổi luôn.
Đổi có **hiệu lực ngay với phiên đang mở** (bot gửi `set_permission_mode`, không
phải mở lại phiên), và thành mặc định cho các phiên sau của chat đó.

```text
/perm

🔧 Chế độ quyền: manual (đang áp cho phiên đang mở)

✅ manual — hỏi mọi thứ cần quyền (mặc định)
•  acceptEdits — tự cho sửa file, tool khác vẫn hỏi
•  auto — để model tự phán cho phép/từ chối
•  dontAsk — không hỏi; cái chưa được cho phép trước thì từ chối
•  plan — chỉ lập kế hoạch, không thao tác
•  bypassPermissions — bỏ qua mọi kiểm tra

[✅ manual]  [acceptEdits]
[auto]       [dontAsk]
[plan]       [bypassPermissions]
```

| Chế độ | Nên dùng khi |
|--------|--------------|
| `manual` | Mặc định. Bạn muốn thấy từng thao tác trước khi nó xảy ra |
| `acceptEdits` | Đang nhờ Claude sửa code trong repo — khỏi bấm cho từng file |
| `auto` | Việc dài nhiều bước, tin vào phán đoán của model |
| `dontAsk` | Chạy tự động, thà thất bại còn hơn chờ người bấm |
| `plan` | Chỉ muốn bàn phương án, không cho sửa gì |
| `bypassPermissions` | Máy dùng riêng, hiểu rằng Claude có toàn quyền của user chạy bot |

Hai điểm dễ vướng:

- `bypassPermissions` **chỉ đặt được** nếu phiên khởi động kèm cờ nguy hiểm, tức
  `"claude_args": ["--dangerously-skip-permissions"]` trong config. Không có thì
  `claude` trả lỗi, bot báo lại kèm gợi ý và **mặc định của chat không bị đổi**.
- `/reset` đưa chế độ quyền về giá trị trong config — quyền rộng bạn vừa đặt
  không sống sót qua `/reset`.

## Ngôn ngữ: `/lang`

`/lang` không tham số hiện danh sách kèm nút bấm; `/lang vi` đổi luôn. Lựa chọn
tính **theo từng chat** và **sống qua `/reset`** — đó là sở thích của bạn, không
phải trạng thái phiên.

```text
/lang

🌐 Ngôn ngữ: <b>Tiếng Việt</b>

• <code>en</code> — English
✅ <code>vi</code> — Tiếng Việt

Bấm nút hoặc gõ /lang <mã>.

[English]  [✅ Tiếng Việt]
```

- `language` trong config là mặc định cho chat chưa từng gõ `/lang`.
- **Log của bot luôn là tiếng Anh** bất kể chat chọn gì, để một máy chỉ có một
  dạng log.
- Thêm ngôn ngữ = thêm một map trong `i18n.go`; test `TestLangCatalogParity` sẽ
  fail nếu thiếu key hoặc `%s`/`%d` không khớp với bản tiếng Anh.

## Phiên: `/session`

Claude Code lưu mỗi phiên thành một file
`~/.claude/projects/<slug-cwd>/<session-id>.jsonl` (`slug-cwd` = đường dẫn với
mọi ký tự không phải chữ/số đổi thành `-`). Bot đọc thẳng chỗ đó, nên thấy được
cả những phiên bạn đã chạy **ở terminal**.

```text
/session

📚 Phiên Claude tại 📁 /home/user/project/telegram-terminal

1. a1b2c3d4 · 16:00 · sửa lại hàm parse config ▶️ đang mở
2. e5f6a7b8 · 15:13 · thêm test cho phần quyền
3. 9c8d7e6f · 14:42 · dọn bớt log thừa
…

/session <số|id> để mở lại · /session new để mở phiên mới
```

| Gõ | Việc |
|----|------|
| `/session` | Liệt kê tối đa 12 phiên của **thư mục hiện tại**, mới nhất trước; nhãn là prompt đầu tiên của phiên; phiên đang mở có `▶️ đang mở` |
| `/session 2` | Mở lại theo số thứ tự |
| `/session e5f6a7b8` | Mở lại theo id — cần **từ 3 ký tự đầu** (1–2 chữ số bị hiểu là số thứ tự) |
| `/session new` | Đóng phiên đang mở; prompt kế tiếp mở phiên mới ở thư mục hiện tại |

- Mở lại = `claude --resume <id>` ở cùng thư mục → **ngữ cảnh cũ còn nguyên**.
- Phiên **giữ thư mục lúc mở**. `cd` ở chế độ shell không đổi thư mục của phiên;
  `/status` sẽ cảnh báo khi hai bên lệch nhau, `/session new` để mở lại ở thư
  mục mới.
- Phiên mới chưa chạy prompt nào thì **chưa có id** (Claude Code chỉ cấp id ở
  lượt đầu) — `/status` ghi `mới (chưa có id)`.
- Nếu phiên đó **đang chạy ở terminal khác**, `claude` mở một **bản copy**: từ
  lúc đó hai bên đi tách nhau, không nhập chung.
- `/session` chỉ liệt kê phiên của thư mục hiện tại. Muốn thấy phiên của thư mục
  khác thì `cd` sang đó rồi gõ lại.

## `/status` — đọc trạng thái

```text
/status

🖥️ server-01 · chế độ: claude
📁 /home/user/project/telegram-terminal
🤖 Phiên: a1b2c3d4 · claude-opus-5 · quyền manual

/perm đổi quyền · /session đổi phiên · /session new mở phiên mới
```

| Dòng | Nghĩa |
|------|-------|
| `🖥️ <host> · chế độ:` | Đang gõ vào máy nào, đang ở chế độ shell hay claude |
| `📁` | Thư mục hiện tại của **chat** (nơi lệnh shell sẽ chạy) |
| `🤖 Phiên:` | id ngắn · model · chế độ quyền **thật** của phiên đó |
| `⚠️ Phiên đang ở 📁 …` | `cd` đã làm lệch thư mục chat so với phiên Claude |

Nếu chưa mở phiên nào: `🤖 Phiên: chưa mở · quyền manual sẽ áp khi mở`.

## Bảng lệnh đầy đủ

| Lệnh | Việc |
|------|------|
| *(gõ trực tiếp)* | Chạy theo chế độ hiện tại (shell hoặc Claude) |
| *(gửi ảnh/file kèm caption)* | Chế độ Claude: ảnh vào thẳng lượt Claude, caption làm prompt. Chế độ shell: chỉ lưu và báo đường dẫn |
| `/sh [lệnh]` | Chạy shell; `/sh` trống = chuyển sang chế độ shell |
| `/c [prompt]` | Hỏi Claude; `/c` trống = chuyển sang chế độ Claude |
| `/cancel` | Hủy lệnh shell / lượt Claude đang chạy |
| `/session` | Liệt kê phiên Claude đã lưu ở thư mục hiện tại |
| `/session <số\|id>` | Mở lại một phiên trong danh sách |
| `/session new` | Đóng phiên hiện tại, mở phiên mới ở thư mục hiện tại |
| `/perm [chế độ]` | Đổi quyền (có nút bấm) |
| `/lang [mã]` | Đổi ngôn ngữ giao diện: `en`, `vi` (có nút bấm) |
| `/status` | Xem chế độ, thư mục, phiên Claude & quyền |
| `/reset` | Về thư mục mặc định, chế độ shell, quyền theo config & đóng phiên (không xóa gì trên đĩa) |
| `/help` | Trợ giúp |

Tên gọi khác, giữ cho quen tay:

```text
/shell = /sh          /claude = /c          /stop = /cancel
/permission = /perm   /language = /lang   /mode, /st, /pwd = /status
/sessions, /ss, /newchat, /resume, /r = /session
```

Trong nhóm chat, Telegram tự thêm `@tenbot` vào lệnh (`/status@mybot`) — bot tự
bỏ phần đó.

## Kịch bản thường dùng

**Xem máy có ổn không**

```text
/status
uptime && free -h && df -h /
journalctl -u nginx -n 30 --no-pager
```

**Nhờ Claude sửa một lỗi trong repo, đỡ phải bấm nhiều**

```text
cd /home/user/project/telegram-terminal
/c
/perm acceptEdits          → khỏi bấm cho từng lần sửa file
sửa hàm resolveSession cho nhận cả id toàn chữ số, rồi chạy go test ./...
                           → Bash vẫn hỏi quyền, bấm ✅
/perm manual               → trả lại mặc định khi xong
```

**Bàn phương án trước, chưa cho sửa gì**

```text
/perm plan
/c nên tách file main.go thế nào? chỉ nêu phương án
```

**Tiếp tục việc đang làm dở ở terminal**

```text
cd /duong/dan/du-an
/session                   → tìm phiên theo nhãn (prompt đầu tiên)
/session 3
tiếp tục phần còn lại giúp tôi
```

**Gửi screenshot lỗi để Claude đọc giúp**

```text
/c                          → chắc chắn đang ở chế độ Claude
[gửi ảnh]  caption: lỗi này ở đâu ra, sửa thế nào?
                            → ảnh nhúng thẳng vào lượt, không cần cho quyền tool
[gửi 3 ảnh một lần]  caption: 3 màn hình này khác nhau chỗ nào?
                            → bot gom ~1,4s thành một lượt duy nhất
```

**Việc dài, chạy rồi đi làm việc khác**

```text
/perm auto
/c chạy full test suite rồi tóm tắt cái nào fail
```

## Giới hạn & lỗi thường gặp

| Hiện tượng | Nguyên nhân & cách xử lý |
|------------|--------------------------|
| `Bot chưa cấu hình allowlist. User ID của bạn: 123…` | `allowed_user_ids` đang trống. Điền ID rồi restart |
| `⛔ Bạn không có quyền dùng bot này.` | User ID không có trong allowlist |
| `⚠️ claude: … (kiểm tra: đã cài claude và đăng nhập cho user này chưa?)` | Chạy `claude -p "hello"` **bằng chính user chạy bot** một lần. Nếu `claude` nằm ở `~/.local/bin` mà systemd không thấy, đặt `claude_bin` bằng đường dẫn tuyệt đối |
| `Claude chưa được bật trong cấu hình` | Thiếu `"claude_enabled": true` |
| `claude không phản hồi bắt tay initialize` | `claude` khởi động > 60s hoặc bị chặn (VD chờ đăng nhập). Thử chạy tay bằng user đó |
| `⏳ Đang bận chạy lệnh khác` | Mỗi chat một việc một lúc. `/cancel` hoặc chờ |
| Bot im lặng, log không có gì | Đang chạy hai instance cùng token (VD service + chạy tay) → Telegram chia updates ngẫu nhiên. Chỉ để một instance |
| `… (output quá dài, đã cắt bớt)` | Vượt ~100 KB/lệnh. Thêm `tail`, `-n`, `--no-pager` |
| Lệnh treo mãi | Lệnh cần nhập liệu hoặc chạy vô hạn — xem bảng "Không chạy được" ở trên. `/cancel` để dừng |
| `sudo: a password is required` | `sudo` cần TTY, không dùng được qua bot. Chạy ở terminal thật |
| Claude không thấy file bạn vừa `cd` tới | Phiên giữ thư mục lúc mở. `/session new` |
| Nút bấm không phản hồi, hiện "Yêu cầu này không còn chờ trả lời nữa" | Yêu cầu đã hết thời gian, đã bị hủy, hoặc phiên đã đóng |
| Đổi config mà không thấy khác gì | Chưa `sudo systemctl restart telegram-terminal` |
| `Failed to execute …: Exec format error`, `status=203/EXEC`, service restart liên tục | Binary sai kiến trúc CPU. So `uname -m` với `file /usr/local/bin/telegram-terminal`, rồi build lại đúng `GOARCH` (xem mục Build) |
| `status=203/EXEC` nhưng đúng kiến trúc | File bị hỏng/thiếu khi truyền. Đối chiếu `sha256sum` hai đầu |
| `⚠️ Chưa hỗ trợ gửi … cho Claude` | Video, GIF động, voice, audio. Chỉ ảnh và document được hỗ trợ |
| `⚠️ không tải được file: file lớn hơn 20.0 MB` | Giới hạn của Bot API, không phải của bot này. Nén nhỏ lại, hoặc `scp` lên máy rồi nhờ Claude đọc theo đường dẫn |
| `📎 Đã lưu … Đang ở chế độ shell nên chưa gửi cho Claude` | Gửi ảnh khi đang ở chế độ shell. Gõ `/c` rồi gửi lại |
| `📎 … lớn hơn giới hạn nhúng` | Ảnh vượt `image_max_bytes`; Claude sẽ đọc bằng tool `Read` (phải cho quyền). Tăng `image_max_bytes` nếu muốn nhúng ảnh to hơn — tối đa ~3,7 MB vì API Claude chặn ở 5 MB sau khi base64 |

Xem log:

```bash
journalctl -u telegram-terminal -f              # theo dõi trực tiếp
journalctl -u telegram-terminal --since "1h ago" --no-pager
```

Bot ghi log mỗi lần mở/resume phiên và mỗi lần bấm nút quyền:

```text
claude: new session for chat 111111111 (cwd=/home/user)
claude: resumed session e5f6a7b8-… for chat 111111111 (cwd=/home/user)
permission: chat 111111111, user 111111111 -> allow (always=true)
language: chat 111111111 -> vi
```

---

# Bên trong: bot nói gì với Claude Code

Mỗi chat giữ **một tiến trình `claude` sống lâu**; bot đóng vai "SDK host" và nói
chuyện với nó bằng JSON-lines trên stdin/stdout:

```text
claude -p --input-format stream-json --output-format stream-json --verbose \
  --include-partial-messages --permission-prompt-tool stdio \
  --permission-mode <chế độ> [--resume <id>]
```

```text
bot    -> claude : {"type":"control_request","request":{"subtype":"initialize"}}
bot    -> claude : {"type":"user","message":{...}}                     prompt
claude -> bot    : stream_event / assistant / user / result            nội dung
claude -> bot    : control_request{subtype:"can_use_tool", ...}        xin quyền
bot    -> claude : control_response{subtype:"success", ...}            quyết định
bot    -> claude : control_request{subtype:"set_permission_mode"}      đổi quyền
bot    -> claude : control_request{subtype:"interrupt"}                /cancel
```

Vài chi tiết đáng lưu:

- Chữ lấy từ `stream_event` → `content_block_delta` → `text_delta`; tool lấy từ
  event `assistant` (input đầy đủ, không phải ghép JSON dở).
- Prompt chỉ có chữ thì `content` là string thuần; có ảnh thì `content` là mảng
  content block — một block `text` rồi lần lượt các block
  `image{source:{type:"base64",media_type,data}}`.
- Trả lời `initialize` có `current_permission_mode` (bot lấy làm nguồn tin cậy)
  nhưng **không có** `session_id` — id chỉ về ở `system/init` của lượt đầu tiên.
- "Cho phép luôn" trả kèm `updatedPermissions` lấy nguyên từ
  `permission_suggestions` của chính yêu cầu đó.
- Trên dây, chế độ mặc định tên là `default`, còn cờ CLI gọi là `manual` — bot
  quy đổi hai chiều.
- Các file: `main.go` (Telegram + shell), `claude.go` (phiên Claude, quyền),
  `sessions.go` (liệt kê/resume phiên), `media.go` (tải ảnh/file, gom album),
  `i18n.go` (chuỗi giao diện theo ngôn ngữ).

# Chạy test

```bash
go test ./...        # dùng một `claude` giả nói đúng giao thức — không tốn API
```

Các test chạy với `claude` thật (có tốn API), bật bằng biến môi trường:

```bash
TT_E2E_CLAUDE=1 go test -run 'TestReal' -v -timeout 15m
```

`testdata/fakeclaude/` là bản `claude` giả: bắt tay initialize, stream chữ, gọi
tool, xin quyền, nhận `set_permission_mode`, ghi lại args, quyết định và cả
message nhận được (để test đối chiếu content block ảnh).

# Bảo mật — nên làm

- Luôn set `allowed_user_ids`. Không để trống ở môi trường thật.
- Chạy bot dưới **user thường**, không phải root (unit mẫu đã theo hướng này).
- Cân nhắc tạo user riêng quyền hạn hẹp nếu chỉ cần một số việc.
- Giữ `config.json` ở quyền `0600`.
- **Đừng commit `config.json`** — nó chứa token. Thêm vào `.gitignore`.
- Token lộ = mất máy: thu hồi/tạo lại token qua BotFather nếu nghi ngờ.
- `bypassPermissions` / `--dangerously-skip-permissions` = Claude có toàn quyền
  của user chạy bot. Chỉ dùng khi bạn hiểu rõ điều đó.

# Gỡ cài

```bash
sudo systemctl disable --now telegram-terminal
sudo rm /etc/systemd/system/telegram-terminal.service /usr/local/bin/telegram-terminal
sudo rm -rf /etc/telegram-terminal
sudo systemctl daemon-reload
```
