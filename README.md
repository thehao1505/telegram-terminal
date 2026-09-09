# telegram-terminal

> 🇻🇳 Bản tiếng Việt: **[README.vi.md](README.vi.md)**

Turn a Telegram bot into a "terminal" for your Ubuntu box:

- Send a shell command → output streams back, and the **working directory
  persists** between commands.
- Send a prompt → **Claude Code** runs right on that machine, the answer
  **streams in real time**, and whenever Claude needs to run a tool it **asks
  for permission with inline buttons** in Telegram.
- Send a **photo** with a caption → the image goes straight into the Claude turn
  (error screenshots, design mockups, charts…).

Written in plain Go (standard library only) → builds to **a single static
binary**, copy it to any machine, no runtime to install.

> ⚠️ This is a remote-admin tool for **your own machine**. Anyone who reaches the
> bot can run arbitrary commands as the user the bot runs as. Guard the bot token
> like a password and ALWAYS set `allowed_user_ids`.

> **Note on language:** the bot's own replies and `/help` are in **Vietnamese**.
> This document explains everything in English and quotes the Vietnamese strings
> verbatim where you will actually see them (e.g. in the troubleshooting table),
> so you can match them up.

**Contents**

- [Install](#install)
- [Configuration](#configuration)
- [User guide](#user-guide)
  - [Two modes](#two-modes-shell-and-claude)
  - [Shell mode](#shell-mode)
  - [Claude mode](#claude-mode)
  - [Sending photos & files](#sending-photos--files)
  - [Permissions: `/perm`](#permissions-perm)
  - [Sessions: `/session`](#sessions-session)
  - [`/status`](#status--reading-the-state)
  - [Full command list](#full-command-list)
  - [Common recipes](#common-recipes)
  - [Limits & troubleshooting](#limits--troubleshooting)
- [Under the hood: what the bot says to Claude Code](#under-the-hood-what-the-bot-says-to-claude-code)
- [Running the tests](#running-the-tests)
- [Security](#security--do-this)
- [Uninstall](#uninstall)

---

# Install

## 1. Create the Telegram bot

1. Message [@BotFather](https://t.me/BotFather) → `/newbot` → copy the **token**.
2. Don't know your own User ID? Install and start the bot with an empty
   `allowed_user_ids`, then message it — it replies with your User ID (and
   refuses to run anything). Put that ID in the config and restart.

## 2. Build

On any machine with Go (build once, use on every Ubuntu amd64 host):

```bash
cd telegram-terminal
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o telegram-terminal .
```

`-trimpath` keeps the build machine's absolute paths out of the binary (matters
if you commit the binary to a public repo).

ARM hosts (Raspberry Pi, AWS Graviton, Oracle Ampere): use `GOARCH=arm64`. Check
the target's architecture with `uname -m` (`x86_64` → `amd64`, `aarch64` →
`arm64`). To build both up front:

```bash
for a in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" \
    -o dist/telegram-terminal-linux-$a .
done
```

> Binaries do **not** run across architectures. Copying the `amd64` build to an
> ARM host makes systemd report `Exec format error` and restart forever.
> `install.sh` compares the binary's architecture against `uname -m`; on a
> mismatch it rebuilds (if Go is present) or fails with a clear message instead
> of installing something that cannot run.

The binary has no dependencies → `scp telegram-terminal user@host:/tmp/` is enough.

## 3. Install on the Ubuntu host

Copy the whole directory (or just `telegram-terminal`, `config.example.json`,
`telegram-terminal.service`, `install.sh`) to the machine, then:

```bash
sudo ./install.sh            # runs as the invoking user by default
# or name the user:  sudo ./install.sh someuser
```

`install.sh` puts the binary in `/usr/local/bin/`, the config in
`/etc/telegram-terminal/config.json` (mode `0600`, owned by the bot user), and
installs a systemd unit that runs as a **regular user**, not root.

## 4. Fill in the config

```bash
sudo nano /etc/telegram-terminal/config.json   # bot_token + allowed_user_ids
sudo systemctl enable --now telegram-terminal
journalctl -u telegram-terminal -f
```

A healthy startup log looks like this:

```text
telegram-terminal khởi động trên host "server-01" — 1 user được phép, claude=true
```

## 5. Smoke test

In Telegram, message the bot:

| Send | Expect |
|------|--------|
| `/help` | The command list, including `Claude: ✅ đã bật` (enabled) |
| `whoami` | The user the bot runs as |
| `/status` | Hostname, current mode, directory, Claude session state |
| `/c hello` | Text streaming into a single message |

If Claude mode errors out, see [Limits & troubleshooting](#limits--troubleshooting).

## Updating to a new build

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o telegram-terminal .
sudo install -m 755 ./telegram-terminal /usr/local/bin/telegram-terminal
sudo systemctl restart telegram-terminal
```

> `sudo` needs a TTY for the password prompt — **do not** send `sudo` commands
> through the bot itself (or any channel without a terminal); it dies at the
> password prompt. Verify which build is installed with
> `sha256sum ./telegram-terminal /usr/local/bin/telegram-terminal`.

---

# Configuration

File: `/etc/telegram-terminal/config.json`

| Key | Meaning | Default |
|-----|---------|---------|
| `bot_token` | Token from BotFather (**required**) | — |
| `allowed_user_ids` | Telegram User IDs allowed to use the bot. Empty = the bot only replies with the sender's ID and **runs nothing** | `[]` |
| `shell` | Shell used to run commands | `/bin/bash` |
| `start_dir` | Starting directory for each chat | the bot user's HOME |
| `command_timeout_seconds` | Timeout for each shell command **and** each Claude turn; `0` = no limit | `0` |
| `claude_enabled` | Enable Claude mode | `false` |
| `claude_bin` | Path to `claude` | `claude` (from `PATH`) |
| `claude_args` | Extra args for `claude`, e.g. `["--model","opus"]` | `[]` |
| `claude_permission_mode` | Permission mode a session starts in: `manual`, `acceptEdits`, `auto`, `dontAsk`, `plan`, `bypassPermissions` | `manual` |
| `claude_ask_timeout_seconds` | How long to wait for a permission button press before **auto-denying** | `300` |
| `image_dir` | Where photos/files from Telegram are stored | `<temp>/telegram-terminal` |
| `image_max_bytes` | Images smaller than this are **embedded directly** into the Claude turn; larger ones only get a file path | `3670016` (3.5 MB) |
| `image_keep_hours` | Delete stored attachments older than this at startup; negative = keep forever | `24` |
| `image_default_prompt` | Prompt used when a photo arrives without a caption | `Xem file đính kèm.` |

Environment variables override the config file (handy for systemd / secret
managers): `TT_BOT_TOKEN`, `TT_ALLOWED_USER_IDS` (comma-separated), `TT_SHELL`,
`TT_START_DIR`, `TT_CLAUDE_BIN`, `TT_CLAUDE_ENABLED=1`,
`TT_CLAUDE_PERMISSION_MODE`, `TT_IMAGE_DIR`, `TT_IMAGE_MAX_BYTES`.

> `image_max_bytes` defaults to 3.5 MB because base64 inflates data by 4/3 and
> the Claude API caps images at 5 MB *after* encoding.

Three sample configs:

```jsonc
// A. Default — safest: Claude asks before every tool
{ "claude_enabled": true, "claude_permission_mode": "manual" }

// B. Fewer prompts while editing code: file edits auto-approved, other tools still ask
{ "claude_enabled": true, "claude_permission_mode": "acceptEdits" }

// C. No prompts at all — ONLY if you understand the risk
{ "claude_enabled": true,
  "claude_permission_mode": "bypassPermissions",
  "claude_args": ["--dangerously-skip-permissions"] }
```

After editing the config, run `sudo systemctl restart telegram-terminal`.

---

# User guide

## Two modes: shell and Claude

Each **chat** with the bot carries its own state: current directory, current
mode, permission mode, and the open Claude session.

```text
shell mode  (default)     anything you type becomes a shell command
Claude mode               anything you type becomes a prompt for Claude
```

Switching:

```text
/sh        → switch to shell mode
/c         → switch to Claude mode
/status    → which mode am I in
```

You can also cross over without switching: `/sh <command>` runs a shell command
while in Claude mode, and `/c <prompt>` asks Claude while in shell mode.

Each chat runs **one thing at a time**. Sending something while busy gets you
`⏳ Đang bận chạy lệnh khác. Gõ /cancel để hủy.` ("busy running something else;
type /cancel to abort").

> Group chats: state belongs to the **chat**, not the person. The whole group
> shares one directory and one Claude session — anyone in `allowed_user_ids` can
> drive it. The bot reads **text** messages and **photo/file attachments**;
> video and voice are ignored.

## Shell mode

```text
you:  ls -la
bot:  ┌ (<pre> block)
      total 6276
      drwxr-xr-x  3 user user  4096 Sep  7 14:38 .
      …
      └
      📁 /home/user/project/telegram-terminal
```

**The directory persists between commands** — `cd` behaves like a real terminal:

```text
you:  cd /var/log
bot:  ✓ (không có output) · 📁 /var/log        ← "no output"
you:  pwd
bot:  /var/log
```

Things worth knowing:

- **Only the directory persists, environment variables do not.** Every command
  runs in a fresh shell process (`bash -c`); the bot only restores `cwd`.
  `export FOO=1` followed by `echo $FOO` prints nothing — put both on one line:
  `export FOO=1 && echo $FOO`.
- **stdout and stderr are merged**, flushed to Telegram about every 1.2 s; long
  output is split to fit Telegram's 4096-character limit, cut at line boundaries.
- **~100 KB of output per command**, after which it is truncated with
  `… (output quá dài, đã cắt bớt)` ("output too long, truncated"). Keep noisy
  commands bounded: `journalctl -u nginx -n 50`, `... | tail -100`.
- When the command finishes the bot appends a summary: `exit <code>` if non-zero,
  `✓ (không có output)` if it printed nothing, and `📁 <dir>` if the directory
  changed.
- `/cancel` **kills the whole process group**, so children die with it.
- With `command_timeout_seconds` > 0, a command that overruns is cancelled with
  `⏱️ hết thời gian, đã hủy` ("timed out, cancelled").

**What does not work** (there is no real terminal):

| Kind | Examples | Do this instead |
|------|----------|-----------------|
| Anything that prompts for input | `sudo`, `passwd`, `ssh` asking for a password | Configure NOPASSWD, or use a real terminal |
| Editors / TUIs | `vim`, `htop`, `less` | `cat`, `ps aux`, `sed -i` |
| Commands that never exit | `tail -f`, `journalctl -f` | Add `-n 50`, or `timeout 10 tail -f …` |

## Claude mode

```text
you:  /c read main.go and tell me which function is longest
bot:  📖 Read
      /home/user/project/telegram-terminal/main.go
      ⏳ (text streams into one message, edited about every 1.5 s)
      The longest function is handle() — roughly 90 lines…
      — 7.4s · $0.0231
```

You will see four kinds of message:

| Shape | Meaning |
|-------|---------|
| Streaming text | Claude's answer, collected into **one** message that is edited as it grows (~1.5 s per edit). Past 3500 characters it starts a new message |
| `💻 Bash` / `✍️ Write` / `📖 Read` / `🔍 Grep` / `🌐 WebFetch` / `🤖 Task` … | Claude just called that tool, with the most useful part of the input (command, path, pattern…) |
| `⚠️ tool lỗi:` | The tool ran and failed ("tool error"). Successful tools stay silent to avoid noise |
| `— 7.4s · $0.0231` | End of turn: duration and cost |

### Sending photos & files

In Claude mode, just send the photo into the chat — **the caption is the
prompt**:

```text
you:  [screenshot of an error]  caption: where does this come from?
bot:  ⏳ The stack trace in the image points at a nil pointer in sessions.go:132…
      — 5.1s · $0.0184
```

- Images are **embedded directly** into the turn (base64), so Claude sees them
  immediately without asking for any tool permission. The bot picks the sharpest
  size Telegram offers that still fits `image_max_bytes`.
- **Multiple photos at once** (an album) work too: the bot waits ~1.4 s to
  collect them all, then runs **one** turn. A caption on any photo in the album
  counts.
- No caption → `image_default_prompt` is used.
- The original file is always saved to `image_dir/<chat_id>/` and its path is
  mentioned in the prompt, so you can ask Claude to re-read or crop it in later
  turns.
- **Non-image files** (`.csv`, `.log`, `.pdf`…) and **oversized images**: the bot
  saves them to disk and hands Claude the path to read with the `Read` tool —
  that step asks for permission like any other tool.
- In **shell mode** the bot only stores the file and replies with its path; it
  does not call Claude. Type `/c` and send it again if you want Claude to look.
- Video, animated GIFs, voice messages and audio are not supported — the bot
  says so rather than silently ignoring them.

### Permission prompts with buttons

When Claude wants to run a tool it does not have permission for, the bot sends:

```text
🔐 Claude xin phép dùng ✍️ Write        ← "Claude asks to use Write"
hello.txt
┌ /home/user/hello.txt
│ ---
│ xin chao
└
tự động từ chối sau 5 phút               ← "auto-deny in 5 minutes"

[✅ Cho phép]  [❌ Từ chối]               ← Allow / Deny
[⏩ Cho phép luôn (phiên này)]            ← Always allow (this session)
```

| Button | Effect |
|--------|--------|
| ✅ **Cho phép** (Allow) | This time only |
| ⏩ **Cho phép luôn** (Always allow, this session) | Accepts Claude Code's own permission suggestion (e.g. switch to `acceptEdits`), so similar work stops asking. Ends when the session closes |
| ❌ **Từ chối** (Deny) | Claude receives `deny` and works around it or reports back |

- Once pressed, that message **loses its buttons** and records the outcome
  (`✅ Đã cho phép.` = allowed).
- Nobody presses within `claude_ask_timeout_seconds` (default 5 minutes) → it is
  **auto-denied** and the message reads `❌ Đã từ chối — hết thời gian chờ.`
  ("denied — timed out").
- While a prompt is pending the chat counts as busy, so you cannot send a new
  prompt — but **buttons still work**.
- `/cancel` while pending: the bot sends `interrupt` to Claude and denies the
  pending requests; if it does not stop within 10 seconds the process is killed.
- Displayed content is trimmed: file previews to 400 characters, tool detail to
  800.

### When Claude asks a plain question

If Claude asks something like "should I use approach A or B?" (an ordinary
question, not a permission request), just **reply with a normal message** — it
goes into the same open session with full context. No need to type `/c` again if
you are already in Claude mode.

## Permissions: `/perm`

`/perm` with no argument shows the current mode with buttons; `/perm auto`
switches immediately. A change takes effect **on the open session right away**
(the bot sends `set_permission_mode`, no need to restart the session) and becomes
the default for that chat's later sessions.

```text
/perm

🔧 Chế độ quyền: manual (đang áp cho phiên đang mở)
   ↑ "permission mode: manual (in effect for the open session)"

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

| Mode | What it does | Use it when |
|------|--------------|-------------|
| `manual` | Asks about everything that needs permission | Default. You want to see each action before it happens |
| `acceptEdits` | File edits auto-approved, other tools still ask | Claude is editing code in a repo and you don't want to tap per file |
| `auto` | A model classifier approves/denies the prompts | Long multi-step work you trust the model to judge |
| `dontAsk` | Never asks; anything not pre-approved is denied | Unattended runs where failing beats waiting for a human |
| `plan` | Planning only, no changes | You just want to discuss an approach |
| `bypassPermissions` | Skips all permission checks | A machine you own, understanding Claude then has the full rights of the bot user |

Two gotchas:

- `bypassPermissions` **can only be set** if the session was started with the
  dangerous flag, i.e. `"claude_args": ["--dangerously-skip-permissions"]` in the
  config. Without it, `claude` returns an error, the bot relays it with a hint,
  and **the chat's default is left unchanged**.
- `/reset` returns the permission mode to the config value — a wider permission
  you just set does not survive `/reset`.

## Sessions: `/session`

Claude Code stores each session as a single file
`~/.claude/projects/<cwd-slug>/<session-id>.jsonl` (`cwd-slug` = the path with
every non-alphanumeric character replaced by `-`). The bot reads that directory
directly, so it also sees sessions you ran **in a terminal**.

```text
/session

📚 Phiên Claude tại 📁 /home/user/project/telegram-terminal
   ↑ "Claude sessions in <dir>"

1. a1b2c3d4 · 16:00 · fix the config parser ▶️ đang mở      ← "open"
2. e5f6a7b8 · 15:13 · add tests for permissions
3. 9c8d7e6f · 14:42 · clean up noisy logs
…

/session <số|id> để mở lại · /session new để mở phiên mới
```

| Send | Effect |
|------|--------|
| `/session` | List up to 12 sessions for the **current directory**, newest first; the label is the session's first prompt; the open one is marked `▶️ đang mở` |
| `/session 2` | Resume by list position |
| `/session e5f6a7b8` | Resume by id — needs **at least 3 leading characters** (1–2 digits are read as a list position) |
| `/session new` | Close the open session; the next prompt opens a fresh one in the current directory |

- Resuming runs `claude --resume <id>` in the same directory → **the old context
  is intact**.
- A session **keeps the directory it was opened in**. `cd` in shell mode does not
  move it; `/status` warns when the two drift apart, and `/session new` reopens
  in the current directory.
- A brand-new session has **no id yet** (Claude Code only issues one on the first
  turn) — `/status` shows `mới (chưa có id)` ("new, no id yet").
- If that session is **currently running in another terminal**, `claude` opens a
  **copy**: from then on the two diverge instead of merging.
- `/session` only lists sessions for the current directory. To see another
  project's sessions, `cd` there and run it again.

## `/status` — reading the state

```text
/status

🖥️ server-01 · chế độ: claude                              ← host · mode
📁 /home/user/project/telegram-terminal
🤖 Phiên: a1b2c3d4 · claude-opus-5 · quyền manual           ← session · model · permission mode

/perm đổi quyền · /session đổi phiên · /session new mở phiên mới
```

| Line | Meaning |
|------|---------|
| `🖥️ <host> · chế độ:` | Which machine you are typing into, and whether you are in shell or Claude mode |
| `📁` | The **chat's** current directory (where shell commands run) |
| `🤖 Phiên:` | short id · model · the session's **actual** permission mode |
| `⚠️ Phiên đang ở 📁 …` | `cd` has moved the chat's directory away from the session's |

With no session open: `🤖 Phiên: chưa mở · quyền manual sẽ áp khi mở`
("no session open; manual will apply when one opens").

## Full command list

| Command | Does |
|---------|------|
| *(plain message)* | Runs according to the current mode (shell or Claude) |
| *(photo/file with caption)* | Claude mode: the image goes into the Claude turn, the caption is the prompt. Shell mode: stored, path reported |
| `/sh [command]` | Run a shell command; bare `/sh` switches to shell mode |
| `/c [prompt]` | Ask Claude; bare `/c` switches to Claude mode |
| `/cancel` | Abort the running shell command / Claude turn |
| `/session` | List saved Claude sessions for the current directory |
| `/session <n\|id>` | Resume one of them |
| `/session new` | Close the current session, open a new one here |
| `/perm [mode]` | Change the permission mode (with buttons) |
| `/status` | Show mode, directory, Claude session & permissions |
| `/reset` | Back to the default directory, shell mode, config permissions & close the session (deletes nothing on disk) |
| `/help` | Help |

Aliases, kept for muscle memory:

```text
/shell = /sh          /claude = /c          /stop = /cancel
/permission = /perm   /mode, /st, /pwd = /status
/sessions, /ss, /newchat, /resume, /r = /session
```

In group chats Telegram appends `@botname` to commands (`/status@mybot`) — the
bot strips it.

## Common recipes

**Check whether the box is healthy**

```text
/status
uptime && free -h && df -h /
journalctl -u nginx -n 30 --no-pager
```

**Have Claude fix a bug in a repo without tapping constantly**

```text
cd /home/user/project/telegram-terminal
/c
/perm acceptEdits          → no tapping for each file edit
make resolveSession accept all-digit ids, then run go test ./...
                           → Bash still asks; tap ✅
/perm manual               → back to the default when done
```

**Discuss an approach before letting it touch anything**

```text
/perm plan
/c how should main.go be split up? outline options only
```

**Pick up work you started in a terminal**

```text
cd /path/to/project
/session                   → find it by label (its first prompt)
/session 3
carry on with the rest, please
```

**Send an error screenshot for Claude to read**

```text
/c                          → make sure you are in Claude mode
[send photo]  caption: where does this error come from, and how do I fix it?
                            → embedded in the turn, no tool permission needed
[send 3 photos at once]  caption: what differs between these screens?
                            → the bot groups them (~1.4 s) into a single turn
```

**Long job — start it and walk away**

```text
/perm auto
/c run the full test suite and summarise what fails
```

## Limits & troubleshooting

The left column quotes what the bot or systemd actually prints (Vietnamese where
the bot says it).

| Symptom | Cause & fix |
|---------|-------------|
| `Bot chưa cấu hình allowlist. User ID của bạn: 123…` | `allowed_user_ids` is empty. Add the ID and restart |
| `⛔ Bạn không có quyền dùng bot này.` | Your User ID is not in the allowlist |
| `⚠️ claude: … (kiểm tra: đã cài claude và đăng nhập cho user này chưa?)` | Run `claude -p "hello"` **as the bot's user** once. If `claude` lives in `~/.local/bin` and systemd cannot see it, set `claude_bin` to an absolute path |
| `Claude chưa được bật trong cấu hình` | Missing `"claude_enabled": true` |
| `claude không phản hồi bắt tay initialize` | `claude` took over 60 s to start or is blocked (e.g. waiting on login). Try running it by hand as that user |
| `⏳ Đang bận chạy lệnh khác` | One job per chat. `/cancel` or wait |
| Bot silent, nothing in the log | Two instances share one token (e.g. the service plus a manual run) → Telegram splits updates between them at random. Keep exactly one |
| `… (output quá dài, đã cắt bớt)` | Over ~100 KB for that command. Add `tail`, `-n`, `--no-pager` |
| Command hangs forever | It wants input or never exits — see "What does not work" above. `/cancel` stops it |
| `sudo: a password is required` | `sudo` needs a TTY and cannot work through the bot. Use a real terminal |
| Claude cannot see the file you just `cd`'d to | The session keeps its original directory. `/session new` |
| A button does nothing and shows "Yêu cầu này không còn chờ trả lời nữa" | That request timed out, was cancelled, or its session closed |
| Config change has no effect | You have not run `sudo systemctl restart telegram-terminal` |
| `Failed to execute …: Exec format error`, `status=203/EXEC`, service restarting forever | Wrong CPU architecture. Compare `uname -m` with `file /usr/local/bin/telegram-terminal`, then rebuild for the right `GOARCH` (see Build) |
| `status=203/EXEC` with the right architecture | The file was truncated/corrupted in transit. Compare `sha256sum` on both ends |
| `⚠️ Chưa hỗ trợ gửi … cho Claude` | Video, animated GIF, voice, audio. Only photos and documents are supported |
| `⚠️ không tải được file: file lớn hơn 20.0 MB` | A Bot API limit, not this bot's. Shrink the file, or `scp` it over and point Claude at the path |
| `📎 Đã lưu … Đang ở chế độ shell nên chưa gửi cho Claude` | You sent a photo while in shell mode. Type `/c` and send it again |
| `📎 … lớn hơn giới hạn nhúng` | The image exceeds `image_max_bytes`; Claude will read it with the `Read` tool (needs permission). Raise `image_max_bytes` to embed bigger images — up to ~3.7 MB, since the Claude API caps at 5 MB after base64 |

Reading the log:

```bash
journalctl -u telegram-terminal -f              # follow live
journalctl -u telegram-terminal --since "1h ago" --no-pager
```

The bot logs every session open/resume and every permission button press:

```text
claude: phiên mới cho chat 111111111 (cwd=/home/user)
claude: mở lại phiên e5f6a7b8-… cho chat 111111111 (cwd=/home/user)
quyền: chat 111111111, user 111111111 -> allow (always=true)
```

---

# Under the hood: what the bot says to Claude Code

Each chat keeps **one long-lived `claude` process**; the bot acts as the "SDK
host" and talks to it in JSON lines over stdin/stdout:

```text
claude -p --input-format stream-json --output-format stream-json --verbose \
  --include-partial-messages --permission-prompt-tool stdio \
  --permission-mode <mode> [--resume <id>]
```

```text
bot    -> claude : {"type":"control_request","request":{"subtype":"initialize"}}
bot    -> claude : {"type":"user","message":{...}}                     the prompt
claude -> bot    : stream_event / assistant / user / result            content
claude -> bot    : control_request{subtype:"can_use_tool", ...}        asks permission
bot    -> claude : control_response{subtype:"success", ...}            the decision
bot    -> claude : control_request{subtype:"set_permission_mode"}      /perm
bot    -> claude : control_request{subtype:"interrupt"}                /cancel
```

Details worth knowing:

- Text comes from `stream_event` → `content_block_delta` → `text_delta`; tool
  calls come from the `assistant` event (complete input, no partial JSON to
  stitch together).
- A text-only prompt sends `content` as a plain string; with images `content` is
  an array of content blocks — one `text` block followed by
  `image{source:{type:"base64",media_type,data}}` blocks.
- The `initialize` reply carries `current_permission_mode` (the bot treats it as
  the source of truth) but **no** `session_id` — the id only arrives with
  `system/init` on the first turn.
- "Always allow" replies with `updatedPermissions` taken verbatim from that
  request's own `permission_suggestions`.
- On the wire the default mode is called `default`, while the CLI flag calls it
  `manual` — the bot converts both ways.
- Files: `main.go` (Telegram + shell), `claude.go` (Claude session, permissions),
  `sessions.go` (list/resume sessions), `media.go` (download attachments, group
  albums).

# Running the tests

```bash
go test ./...        # uses a fake `claude` that speaks the real protocol — no API cost
```

Tests that drive the real `claude` (these do cost API usage) are gated behind an
environment variable:

```bash
TT_E2E_CLAUDE=1 go test -run 'TestReal' -v -timeout 15m
```

`testdata/fakeclaude/` is the stand-in `claude`: it performs the initialize
handshake, streams text, calls a tool, asks permission, accepts
`set_permission_mode`, and records the args, decisions and received messages so
tests can assert on them (including image content blocks).

# Security — do this

- Always set `allowed_user_ids`. Never leave it empty in a real deployment.
- Run the bot as a **regular user**, not root (the sample unit already does).
- Consider a dedicated, tightly-scoped user if you only need a few tasks.
- Keep `config.json` at mode `0600`.
- **Never commit `config.json`** — it holds the token. It is in `.gitignore`.
- A leaked token means a lost machine: revoke and reissue via BotFather if in doubt.
- `bypassPermissions` / `--dangerously-skip-permissions` gives Claude the full
  rights of the bot's user. Only use it knowing exactly that.

# Uninstall

```bash
sudo systemctl disable --now telegram-terminal
sudo rm /etc/systemd/system/telegram-terminal.service /usr/local/bin/telegram-terminal
sudo rm -rf /etc/telegram-terminal
sudo systemctl daemon-reload
```
