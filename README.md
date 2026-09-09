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

> **Note on language:** the bot talks **English by default**. Send `/lang vi`
> for Vietnamese (per chat), or set `"language": "vi"` in the config to make it
> the default for every chat. The bot's own log stays English regardless, so one
> machine only ever produces one kind of log.

**Contents**

- [Install](#install)
- [Configuration](#configuration)
- [User guide](#user-guide)
  - [Two modes](#two-modes-shell-and-claude)
  - [Shell mode](#shell-mode)
  - [Claude mode](#claude-mode)
  - [Reading the footer](#reading-the-footer)
  - [Sending photos & files](#sending-photos--files)
  - [Permissions: `/perm`](#permissions-perm)
  - [Language: `/lang`](#language-lang)
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
telegram-terminal started on host "server-01" — 1 allowed user(s), claude=true, lang=en, attachments in /tmp/telegram-terminal
```

## 5. Smoke test

In Telegram, message the bot:

| Send | Expect |
|------|--------|
| `/help` | The command list, including `Claude: ✅ enabled` |
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
| `image_default_prompt` | Prompt used when a photo arrives without a caption | the localised default (`Take a look at the attached file.`) |
| `language` | Default interface language for every chat: `en` or `vi`. Each chat can override it with `/lang` | `en` |

Environment variables override the config file (handy for systemd / secret
managers): `TT_BOT_TOKEN`, `TT_ALLOWED_USER_IDS` (comma-separated), `TT_SHELL`,
`TT_START_DIR`, `TT_CLAUDE_BIN`, `TT_CLAUDE_ENABLED=1`,
`TT_CLAUDE_PERMISSION_MODE`, `TT_IMAGE_DIR`, `TT_IMAGE_MAX_BYTES`, `TT_LANG`.

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
`⏳ Busy running something else. Send /cancel to abort.`

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
bot:  ✓ (no output) · 📁 /var/log
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
  `… (output too long, truncated)`. Keep noisy
  commands bounded: `journalctl -u nginx -n 50`, `... | tail -100`.
- When the command finishes the bot appends a summary: `exit <code>` if non-zero,
  `✓ (no output)` if it printed nothing, and `📁 <dir>` if the directory
  changed.
- `/cancel` **kills the whole process group**, so children die with it.
- With `command_timeout_seconds` > 0, a command that overruns is cancelled with
  `⏱️ timed out, cancelled`.

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
      — 7.4s · $0.0231 · 3 steps
```

You will see four kinds of message:

| Shape | Meaning |
|-------|---------|
| Streaming text | Claude's answer, collected into **one** message that is edited as it grows (~1.5 s per edit). Past 3500 characters it starts a new message |
| `💻 Bash` / `✍️ Write` / `📖 Read` / `🔍 Grep` / `🌐 WebFetch` / `🤖 Task` … | Claude just called that tool, with the most useful part of the input (command, path, pattern…) |
| `⚠️ tool error:` | The tool ran and failed. Successful tools stay silent to avoid noise |
| `— 7.4s · $0.0231 · 3 steps` | End of turn: see [Reading the footer](#reading-the-footer) |

### Reading the footer

```text
— 16m41s · $0.87 · session $32.31 · 5 steps
   │         │       │               └ model turns inside this one prompt (num_turns)
   │         │       └ running total for the whole claude process
   │         └ what this prompt cost on its own
   └ wall-clock time of the turn
```

Two things are easy to misread, so the bot spells them out:

- **Cost.** Claude Code's `result` event reports `total_cost_usd` as a *running
  total*: "cumulative across turns in streaming-input sessions — each result
  carries the running total so far". Since the bot keeps one long-lived `claude`
  process per chat, printing that number raw makes every prompt look
  progressively more expensive. The bot subtracts the previous total to get the
  per-prompt cost and labels the running total separately. Resuming a session or
  `/clear` resets the total; the bot notices the drop and treats the new total as
  the turn's cost. It is Anthropic's own estimate at list prices, not a billing
  statement.
- **Duration.** This is the turn's wall clock, so it **includes the time the turn
  spent waiting for you to tap Allow/Deny** — the permission request happens
  mid-turn. A 16-minute turn usually means the buttons sat unanswered, not that
  the model was thinking. (`duration_api_ms` exists in the event but sums
  parallel requests, so it can exceed the wall clock and is not shown.)

The session total and the step count are omitted when they add nothing — the
first turn of a session, and prompts that took a single model turn.

### Sending photos & files

In Claude mode, just send the photo into the chat — **the caption is the
prompt**:

```text
you:  [screenshot of an error]  caption: where does this come from?
bot:  ⏳ The stack trace in the image points at a nil pointer in sessions.go:132…
      — 5.1s · $0.0184 · 2 steps
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
🔐 Claude asks to use ✍️ Write
hello.txt
┌ /home/user/hello.txt
│ ---
│ hello
└
auto-denied in 5 minutes

[✅ Allow]  [❌ Deny]
[⏩ Always allow (this session)]
```

| Button | Effect |
|--------|--------|
| ✅ **Allow** | This time only |
| ⏩ **Always allow (this session)** | Accepts Claude Code's own permission suggestion (e.g. switch to `acceptEdits`), so similar work stops asking. Ends when the session closes |
| ❌ **Deny** | Claude receives `deny` and works around it or reports back |

- Once pressed, that message **loses its buttons** and records the outcome
  (`✅ Allowed.`).
- Nobody presses within `claude_ask_timeout_seconds` (default 5 minutes) → it is
  **auto-denied** and the message reads `❌ Denied — timed out.`
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

🔧 Permission mode: manual (in effect for the open session)

✅ manual — asks about everything that needs permission (default)
•  acceptEdits — file edits auto-approved, other tools still ask
•  auto — let the model decide allow/deny
•  dontAsk — never asks; anything not pre-approved is denied
•  plan — planning only, no changes
•  bypassPermissions — skip every check

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

## Language: `/lang`

`/lang` with no argument lists the languages with buttons; `/lang vi` switches
immediately. The choice is **per chat** and survives `/reset` — it is your
preference, not session state.

```text
/lang

🌐 Language: <b>English</b>

✅ <code>en</code> — English
• <code>vi</code> — Tiếng Việt

Tap a button or type /lang <code>.

[✅ English]  [Tiếng Việt]
```

- `language` in the config sets the default for chats that never ran `/lang`.
- The bot's **log is always English**, whatever the chat language, so one machine
  produces one kind of log.
- Adding a language means adding one map to `i18n.go`; a test
  (`TestLangCatalogParity`) fails if it is missing keys or if its `%s`/`%d`
  placeholders do not line up with English.

## Sessions: `/session`

Claude Code stores each session as a single file
`~/.claude/projects/<cwd-slug>/<session-id>.jsonl` (`cwd-slug` = the path with
every non-alphanumeric character replaced by `-`). The bot reads that directory
directly, so it also sees sessions you ran **in a terminal**.

```text
/session

📚 Claude sessions in 📁 /home/user/project/telegram-terminal

1. a1b2c3d4 · 16:00 · fix the config parser ▶️ open
2. e5f6a7b8 · 15:13 · add tests for permissions
3. 9c8d7e6f · 14:42 · clean up noisy logs
…

/session <n|id> to resume · /session new for a new session
```

| Send | Effect |
|------|--------|
| `/session` | List up to 12 sessions for the **current directory**, newest first; the label is the session's first prompt; the open one is marked `▶️ open` |
| `/session 2` | Resume by list position |
| `/session e5f6a7b8` | Resume by id — needs **at least 3 leading characters** (1–2 digits are read as a list position) |
| `/session new` | Close the open session; the next prompt opens a fresh one in the current directory |

- Resuming runs `claude --resume <id>` in the same directory → **the old context
  is intact**.
- A session **keeps the directory it was opened in**. `cd` in shell mode does not
  move it; `/status` warns when the two drift apart, and `/session new` reopens
  in the current directory.
- A brand-new session has **no id yet** (Claude Code only issues one on the first
  turn) — `/status` shows `new (no id yet)`.
- If that session is **currently running in another terminal**, `claude` opens a
  **copy**: from then on the two diverge instead of merging.
- `/session` only lists sessions for the current directory. To see another
  project's sessions, `cd` there and run it again.

## `/status` — reading the state

```text
/status

🖥️ server-01 · mode: claude
📁 /home/user/project/telegram-terminal
🤖 Session: a1b2c3d4 · claude-opus-5 · manual permissions

/perm permissions · /session sessions · /session new for a new session
```

| Line | Meaning |
|------|---------|
| `🖥️ <host> · mode:` | Which machine you are typing into, and whether you are in shell or Claude mode |
| `📁` | The **chat's** current directory (where shell commands run) |
| `🤖 Session:` | short id · model · the session's **actual** permission mode |
| `⚠️ The session is in 📁 …` | `cd` has moved the chat's directory away from the session's |

With no session open: `🤖 Session: none open · manual permissions will apply when one opens`.

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
| `/lang [code]` | Change the interface language: `en`, `vi` (with buttons) |
| `/status` | Show mode, directory, Claude session & permissions |
| `/reset` | Back to the default directory, shell mode, config permissions & close the session (deletes nothing on disk) |
| `/help` | Help |

Aliases, kept for muscle memory:

```text
/shell = /sh          /claude = /c          /stop = /cancel
/permission = /perm   /language = /lang   /mode, /st, /pwd = /status
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

The left column quotes what the bot or systemd actually prints (with the default
English interface).

| Symptom | Cause & fix |
|---------|-------------|
| `This bot has no allowlist configured. Your User ID: 123…` | `allowed_user_ids` is empty. Add the ID and restart |
| `⛔ You are not allowed to use this bot.` | Your User ID is not in the allowlist |
| `⚠️ claude: … (check: is claude installed and logged in for this user?)` | Run `claude -p "hello"` **as the bot's user** once. If `claude` lives in `~/.local/bin` and systemd cannot see it, set `claude_bin` to an absolute path |
| `Claude is not enabled in the config` | Missing `"claude_enabled": true` |
| `claude did not answer initialize` | `claude` took over 60 s to start or is blocked (e.g. waiting on login). Try running it by hand as that user |
| `⏳ Busy running something else` | One job per chat. `/cancel` or wait |
| Bot silent, nothing in the log | Two instances share one token (e.g. the service plus a manual run) → Telegram splits updates between them at random. Keep exactly one |
| `… (output too long, truncated)` | Over ~100 KB for that command. Add `tail`, `-n`, `--no-pager` |
| Command hangs forever | It wants input or never exits — see "What does not work" above. `/cancel` stops it |
| `sudo: a password is required` | `sudo` needs a TTY and cannot work through the bot. Use a real terminal |
| Claude cannot see the file you just `cd`'d to | The session keeps its original directory. `/session new` |
| A button does nothing and shows "That request is no longer waiting for an answer." | That request timed out, was cancelled, or its session closed |
| Config change has no effect | You have not run `sudo systemctl restart telegram-terminal` |
| `Failed to execute …: Exec format error`, `status=203/EXEC`, service restarting forever | Wrong CPU architecture. Compare `uname -m` with `file /usr/local/bin/telegram-terminal`, then rebuild for the right `GOARCH` (see Build) |
| `status=203/EXEC` with the right architecture | The file was truncated/corrupted in transit. Compare `sha256sum` on both ends |
| `⚠️ Sending … to Claude is not supported yet` | Video, animated GIF, voice, audio. Only photos and documents are supported |
| `⚠️ could not download the file: the file is larger than 20.0 MB` | A Bot API limit, not this bot's. Shrink the file, or `scp` it over and point Claude at the path |
| `📎 Saved … You are in shell mode so it was not sent to Claude` | You sent a photo while in shell mode. Type `/c` and send it again |
| `📎 … is over the embed limit` | The image exceeds `image_max_bytes`; Claude will read it with the `Read` tool (needs permission). Raise `image_max_bytes` to embed bigger images — up to ~3.7 MB, since the Claude API caps at 5 MB after base64 |

Reading the log:

```bash
journalctl -u telegram-terminal -f              # follow live
journalctl -u telegram-terminal --since "1h ago" --no-pager
```

The bot logs every session open/resume and every permission button press:

```text
claude: new session for chat 111111111 (cwd=/home/user)
claude: resumed session e5f6a7b8-… for chat 111111111 (cwd=/home/user)
permission: chat 111111111, user 111111111 -> allow (always=true)
language: chat 111111111 -> vi
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
  albums), `i18n.go` (interface strings per language).

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
