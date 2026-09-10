// Chuỗi giao diện theo ngôn ngữ.
//
// Mặc định là English; "vi" cho tiếng Việt. Mỗi chat chọn riêng bằng /lang, giá
// trị mặc định lấy từ config ("language"). Log của bot luôn là English để một
// máy chỉ có một dạng log bất kể người dùng chọn gì.
//
// Thiếu key ở ngôn ngữ nào thì tự lùi về English (xem language.t). Test
// TestLangCatalogParity giữ cho hai bộ không lệch nhau.
package main

import (
	"fmt"
	"sort"
	"strings"
)

type language struct {
	Code   string // "en"
	Native string // tên gọi trong chính ngôn ngữ đó
	s      map[string]string
}

// t lấy chuỗi theo key (thiếu thì lùi về English), Sprintf nếu có args.
func (l *language) t(key string, args ...any) string {
	s, ok := l.s[key]
	if !ok {
		if s, ok = langEN.s[key]; !ok {
			return "!" + key // lỗi lập trình, hiện ra để thấy ngay
		}
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

var languages = []*language{langEN, langVI}

func languageByCode(code string) *language {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, l := range languages {
		if l.Code == code {
			return l
		}
	}
	return nil
}

// languageCodes: danh sách code để hiện trong thông báo lỗi.
func languageCodes() string {
	var out []string
	for _, l := range languages {
		out = append(out, l.Code)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

var langEN = &language{Code: "en", Native: "English", s: map[string]string{
	// ---- chung / shell ----
	"output.truncated": "… (output too long, truncated)",
	"shell.timeout":    "⏱️ timed out, cancelled",
	"shell.cancelled":  "🛑 cancelled",
	"shell.nooutput":   "✓ (no output)",
	"busy":             "⏳ Busy running something else. Send /cancel to abort.",
	"cancelling":       "🛑 Cancelling…",
	"nothing.running":  "Nothing is running.",
	"no.allowlist":     "This bot has no allowlist configured.\nYour User ID: <b>%d</b>\nAdd it to \"allowed_user_ids\" and restart the bot.",
	"denied":           "⛔ You are not allowed to use this bot.",
	"reset.done":       "🔄 Closed the Claude session, back to shell mode and the permissions from the config.\nWorking directory reset to 📁 %s (nothing on disk was deleted).",
	"mode.shell":       "🖥️ Shell mode — send any command. /c switches to Claude.",
	"mode.claude":      "🤖 Claude mode — send any prompt.\n/sh for shell · /session for sessions · /perm for permissions.",
	"claude.disabled":  "Claude is not enabled in the config (claude_enabled=false).",

	// ---- /status ----
	"status.head":        "🖥️ <b>%s</b> · mode: <b>%s</b>\n📁 <code>%s</code>\n",
	"status.claude.off":  "🤖 Claude: ❌ not enabled in the config",
	"status.session":     "🤖 Session: %s",
	"status.cwd.drift":   "\n⚠️ The session is in 📁 <code>%s</code> — use /session new to reopen it in the current directory.",
	"status.no.session":  "🤖 Session: none open · <b>%s</b> permissions will apply when one opens",
	"status.update":      "🆙 New version: <b>%s</b> — see /version\n",
	"status.footer":      "\n\n/perm permissions · /session sessions · /session new for a new session",
	"session.no.id":      "new (no id yet)",
	"session.permission": "%s permissions",

	// ---- nút bấm / callback ----
	"cb.stale": "That request is no longer waiting for an answer.",

	// ---- /help ----
	"help.claude.on":  "✅ enabled",
	"help.claude.off": "❌ not enabled",
	"help.text": `<b>telegram-terminal</b> @ <b>%s</b>

Any plain message runs according to the current mode (shell or Claude).
Claude: %s

<b>Run things</b>
/sh &lt;command&gt; — run a shell command; bare <code>/sh</code> switches to shell mode
/c &lt;prompt&gt; — ask Claude; bare <code>/c</code> switches to Claude mode
/cancel — abort the running command / Claude turn

<b>Photos &amp; files</b>
In Claude mode just send a photo — the caption is the prompt (several at once
works too, the bot groups them into one turn). Images are embedded directly in
the turn so Claude sees them without asking permission. Non-image files (or
oversized images) are saved to disk and Claude reads them with the Read tool.

<b>Claude sessions</b>
/session — list saved sessions for the current directory
/session &lt;n|id&gt; — resume one of them
/session new — close the current session, open a new one here

<b>Settings</b>
/perm [mode] — change permissions: manual, acceptEdits, auto, dontAsk, plan…
/lang [code] — change the bot's language (%s)
/status — mode, directory, Claude session &amp; permissions
/version — the running build, and whether a newer release exists
/reset — back to the default directory, shell mode, config permissions &amp; close the session
/help — this help

Claude's answer streams back in real time. When it needs to run a tool without
permission, the bot sends a message with <b>Allow</b> / <b>Deny</b> / <b>Always
allow</b> buttons (auto-denied after %d seconds if nobody taps).`,

	// ---- xin quyền ----
	"ask.head":         "🔐 Claude asks to use %s <b>%s</b>",
	"ask.timeout.note": "\n<i>auto-denied in %.0f minutes</i>",
	"btn.allow":        "✅ Allow",
	"btn.deny":         "❌ Deny",
	"btn.always":       "⏩ Always allow (this session)",
	"ask.withdrawn":    "\n\n🔕 Request withdrawn.",
	"deny.reason":      "The user denied it via Telegram",
	"verdict.always":   "⏩ Allowed (remembered for this session).",
	"verdict.allow":    "✅ Allowed.",
	"verdict.deny":     "❌ Denied.",
	"verdict.deny.why": "❌ Denied — %s.",
	"note.timeout":     "timed out",
	"note.cancelled":   "cancelled",
	"note.exited":      "claude exited",

	// ---- Claude hỏi lại (AskUserQuestion) ----
	"askq.title":      "❓ <b>%s</b>",
	"askq.title.n":    "❓ <b>%s</b> · question %d/%d",
	"askq.header":     "Claude asks",
	"askq.multi.hint": "\n\n<i>Pick one or more, then tap “Send answers”.</i>",
	"askq.chosen":     "\n\n✅ <b>%s</b>",
	"askq.unanswered": "\n\n➖ Not answered.",
	"askq.declined":   "\n\n❌ Not answered — %s.",
	"askq.pick.first": "Pick at least one answer first.",
	"askq.sent":       "📨 Answers sent to Claude.",
	"askq.canceled":   "🚫 Questions cancelled.",
	"btn.askq.send":   "📨 Send answers",
	"btn.askq.cancel": "❌ Cancel",

	// ---- lượt Claude ----
	"claude.start.error":  "⚠️ claude: %s\n(check: is `claude` installed and logged in for this user?)",
	"claude.timeout":      "⏱️ Claude timed out, cancelled.",
	"claude.cancelled":    "🛑 Cancelled.",
	"claude.no.content":   "✓ (Claude returned no content)",
	"claude.error":        "⚠️ claude: %s",
	"claude.tool.error":   "⚠️ tool error:",
	"result.session.cost": "session $%s",
	"result.steps":        "%d steps",
	"err.busy.turn":       "the claude session is busy with another turn",
	"err.exited":          "claude exited: %s",
	"err.no.reply":        "claude did not answer %s",
	"err.no.stderr":       "no stderr output",
	"err.ctl.failed":      "%s failed",
	"err.unsupported":     "telegram-terminal does not support %s",

	// ---- /perm ----
	"perm.desc.manual":            "asks about everything that needs permission (default)",
	"perm.desc.acceptEdits":       "file edits auto-approved, other tools still ask",
	"perm.desc.auto":              "let the model decide allow/deny",
	"perm.desc.dontAsk":           "never asks; anything not pre-approved is denied",
	"perm.desc.plan":              "planning only, no changes",
	"perm.desc.bypassPermissions": "skip every check (needs claude_args [\"--dangerously-skip-permissions\"])",
	"perm.head":                   "🔧 Permission mode: <b>%s</b>",
	"perm.live":                   " (in effect for the open session)",
	"perm.next":                   " (applies to the next session)",
	"perm.hint":                   "\nTap a button or type <code>/perm &lt;name&gt;</code>.",
	"perm.invalid":                "⚠️ invalid mode: %s\nPick one of: <code>%s</code>",
	"perm.failed":                 "⚠️ could not change the mode: %s",
	"perm.bypass.hint":            "\n\nTo use this mode, put <code>\"claude_args\": [\"--dangerously-skip-permissions\"]</code> in the config, then /session new.",
	"perm.changed":                "🔧 Permission mode is now <b>%s</b> — %s.",
	"perm.changed.next":           " (applies to the next Claude session)",

	// ---- /lang ----
	"lang.head":    "🌐 Language: <b>%s</b>",
	"lang.hint":    "\nTap a button or type <code>/lang &lt;code&gt;</code>.",
	"lang.invalid": "⚠️ unknown language: %s\nAvailable: <code>%s</code>",
	"lang.changed": "🌐 Language set to <b>%s</b>.",

	// ---- /version ----
	"ver.line":         "📦 <b>telegram-terminal</b> <code>%s</code> · %s",
	"ver.newer":        "\n\n🆙 New version: <b>%s</b>\n%s",
	"ver.uptodate":     "\n\n✅ Up to date (latest release: <b>%s</b>).",
	"ver.dev":          "\n\nSelf-built binary — no version to compare against.\nLatest release: <b>%s</b>\n%s",
	"ver.no.release":   "\n\n<i>%s has not published a release yet.</i>",
	"ver.check.failed": "\n\n⚠️ Could not reach GitHub: %s",
	"update.alert":     "🆙 <b>telegram-terminal %s</b> is out — this machine runs <b>%s</b>\n%s",
	"update.howto":     "\n\nUpdate (your config is kept):\n<pre>%s</pre>",

	// ---- /session ----
	"sess.head":          "📚 Claude sessions in 📁 <code>%s</code>\n\n",
	"sess.none":          "No saved sessions for this directory.\n",
	"sess.unknown":       "(unknown)",
	"sess.open.mark":     " ▶️ open",
	"sess.open.other":    "\n▶️ Open session <code>%s</code> in 📁 <code>%s</code>\n",
	"sess.hint":          "\n/session &lt;n|id&gt; to resume · /session new for a new session.",
	"sess.err.number":    "there is no session number %d (listing %d)",
	"sess.err.not.found": "no session matching %q",
	"sess.err.ambiguous": "%q matches %d sessions, type a few more characters",
	"sess.err.missing":   "missing argument",
	"sess.usage":         "Usage: /session &lt;n|id&gt; — type /session to see the list.",
	"sess.new":           "🆕 The next prompt opens a new Claude session in 📁 %s",
	"sess.new.closed":    "🆕 Closed the previous Claude session. ",
	"sess.busy":          "⏳ Something else is running. Send /cancel and try again.",
	"sess.resolve.error": "⚠️ %s\nType /session to see the list.",
	"sess.resume.error":  "⚠️ could not resume the session: %s",
	"sess.resumed":       "↩️ Resumed session <code>%s</code> · 📁 <code>%s</code>\n%s permissions · send a prompt to continue.",

	// ---- ảnh & file ----
	"media.inline":         "- %s — embedded above, original: %s",
	"media.too.big":        "- %s — too large to embed, use the Read tool to view it: %s",
	"media.prompt.header":  "The user attached this via Telegram:",
	"media.default.prompt": "Take a look at the attached file.",
	"media.unsupported":    "⚠️ Sending %s to Claude is not supported yet. Photos and files are.",
	"media.download.error": "⚠️ could not download the file: %s",
	"media.shell.saved":    "📎 Saved <code>%s</code> (%s)\nYou are in shell mode so it was not sent to Claude — type /c and send it again.",
	"media.note.too.big":   "📎 %s (%s) is over the embed limit of %s — Claude will read the file with the Read tool.",
	"media.note.not.image": "📎 %s (%s) is not an image — saved; Claude will read it if needed.",
	"media.type.video":     "a video",
	"media.type.animation": "an animated GIF",
	"media.type.voice":     "a voice message",
	"media.type.audio":     "an audio file",
	"media.err.file.path":  "getFile returned no file_path",
	"media.err.http":       "download: HTTP %d",
	"media.err.too.big":    "the file is larger than %s",
}}

var langVI = &language{Code: "vi", Native: "Tiếng Việt", s: map[string]string{
	// ---- chung / shell ----
	"output.truncated": "… (output quá dài, đã cắt bớt)",
	"shell.timeout":    "⏱️ hết thời gian, đã hủy",
	"shell.cancelled":  "🛑 đã hủy",
	"shell.nooutput":   "✓ (không có output)",
	"busy":             "⏳ Đang bận chạy lệnh khác. Gõ /cancel để hủy.",
	"cancelling":       "🛑 Đang hủy…",
	"nothing.running":  "Không có lệnh nào đang chạy.",
	"no.allowlist":     "Bot chưa cấu hình allowlist.\nUser ID của bạn: <b>%d</b>\nThêm ID này vào \"allowed_user_ids\" rồi khởi động lại bot.",
	"denied":           "⛔ Bạn không có quyền dùng bot này.",
	"reset.done":       "🔄 Đã đóng phiên Claude, về chế độ shell và quyền theo config.\nThư mục làm việc trở về 📁 %s (không xóa gì trên đĩa).",
	"mode.shell":       "🖥️ Chế độ shell — gửi lệnh bất kỳ. /c để sang Claude.",
	"mode.claude":      "🤖 Chế độ Claude — gửi prompt bất kỳ.\n/sh để sang shell · /session để xem phiên · /perm để đổi quyền.",
	"claude.disabled":  "Claude chưa được bật trong cấu hình (claude_enabled=false).",

	// ---- /status ----
	"status.head":        "🖥️ <b>%s</b> · chế độ: <b>%s</b>\n📁 <code>%s</code>\n",
	"status.claude.off":  "🤖 Claude: ❌ chưa bật trong cấu hình",
	"status.session":     "🤖 Phiên: %s",
	"status.cwd.drift":   "\n⚠️ Phiên đang ở 📁 <code>%s</code> — /session new để mở lại ở thư mục hiện tại.",
	"status.no.session":  "🤖 Phiên: chưa mở · quyền <b>%s</b> sẽ áp khi mở",
	"status.update":      "🆙 Có bản mới: <b>%s</b> — xem /version\n",
	"status.footer":      "\n\n/perm đổi quyền · /session đổi phiên · /session new mở phiên mới",
	"session.no.id":      "mới (chưa có id)",
	"session.permission": "quyền %s",

	// ---- nút bấm / callback ----
	"cb.stale": "Yêu cầu này không còn chờ trả lời nữa.",

	// ---- /help ----
	"help.claude.on":  "✅ đã bật",
	"help.claude.off": "❌ chưa bật",
	"help.text": `<b>telegram-terminal</b> @ <b>%s</b>

Gửi bất kỳ dòng nào = chạy theo chế độ hiện tại (shell hoặc Claude).
Claude: %s

<b>Chạy việc</b>
/sh &lt;lệnh&gt; — chạy shell; <code>/sh</code> trống = chuyển sang chế độ shell
/c &lt;prompt&gt; — hỏi Claude; <code>/c</code> trống = chuyển sang chế độ Claude
/cancel — hủy lệnh / lượt Claude đang chạy

<b>Ảnh &amp; file</b>
Đang ở chế độ Claude thì gửi thẳng ảnh vào chat — caption chính là prompt (gửi
nhiều ảnh một lần cũng được, bot gom thành một lượt). Ảnh được nhúng trực tiếp
vào lượt nên Claude thấy ngay, không cần xin quyền. File không phải ảnh (hoặc
ảnh quá lớn) thì bot lưu ra đĩa và đưa đường dẫn để Claude tự đọc.

<b>Phiên Claude</b>
/session — liệt kê phiên đã lưu ở thư mục hiện tại
/session &lt;số|id&gt; — mở lại một phiên
/session new — đóng phiên hiện tại, mở phiên mới ở thư mục hiện tại

<b>Cấu hình</b>
/perm [chế độ] — đổi quyền: manual, acceptEdits, auto, dontAsk, plan…
/lang [mã] — đổi ngôn ngữ của bot (%s)
/status — xem chế độ, thư mục, phiên Claude &amp; quyền
/version — bản đang chạy, và có bản mới hơn hay không
/reset — về thư mục mặc định, chế độ shell, quyền theo config &amp; đóng phiên
/help — trợ giúp

Câu trả lời của Claude được stream về theo thời gian thực. Khi cần chạy tool mà
chưa có quyền, bot gửi tin kèm nút <b>Cho phép</b> / <b>Từ chối</b> / <b>Cho
phép luôn</b> (tự động từ chối sau %d giây nếu không ai bấm).`,

	// ---- xin quyền ----
	"ask.head":         "🔐 Claude xin phép dùng %s <b>%s</b>",
	"ask.timeout.note": "\n<i>tự động từ chối sau %.0f phút</i>",
	"btn.allow":        "✅ Cho phép",
	"btn.deny":         "❌ Từ chối",
	"btn.always":       "⏩ Cho phép luôn (phiên này)",
	"ask.withdrawn":    "\n\n🔕 Yêu cầu đã được rút lại.",
	"deny.reason":      "Người dùng từ chối qua Telegram",
	"verdict.always":   "⏩ Đã cho phép (ghi nhớ cho phiên này).",
	"verdict.allow":    "✅ Đã cho phép.",
	"verdict.deny":     "❌ Đã từ chối.",
	"verdict.deny.why": "❌ Đã từ chối — %s.",
	"note.timeout":     "hết thời gian chờ",
	"note.cancelled":   "đã hủy",
	"note.exited":      "claude đã thoát",

	// ---- Claude hỏi lại (AskUserQuestion) ----
	"askq.title":      "❓ <b>%s</b>",
	"askq.title.n":    "❓ <b>%s</b> · câu %d/%d",
	"askq.header":     "Claude hỏi",
	"askq.multi.hint": "\n\n<i>Chọn một hoặc nhiều đáp án, xong bấm “Gửi đáp án”.</i>",
	"askq.chosen":     "\n\n✅ <b>%s</b>",
	"askq.unanswered": "\n\n➖ Chưa trả lời.",
	"askq.declined":   "\n\n❌ Chưa trả lời — %s.",
	"askq.pick.first": "Hãy chọn ít nhất một đáp án đã.",
	"askq.sent":       "📨 Đã gửi đáp án cho Claude.",
	"askq.canceled":   "🚫 Đã hủy bộ câu hỏi.",
	"btn.askq.send":   "📨 Gửi đáp án",
	"btn.askq.cancel": "❌ Hủy",

	// ---- lượt Claude ----
	"claude.start.error":  "⚠️ claude: %s\n(kiểm tra: đã cài `claude` và đăng nhập cho user này chưa?)",
	"claude.timeout":      "⏱️ Claude hết thời gian, đã hủy.",
	"claude.cancelled":    "🛑 Đã hủy.",
	"claude.no.content":   "✓ (Claude không trả về nội dung)",
	"claude.error":        "⚠️ claude: %s",
	"claude.tool.error":   "⚠️ tool lỗi:",
	"result.session.cost": "phiên $%s",
	"result.steps":        "%d bước",
	"err.busy.turn":       "phiên claude đang chạy lượt khác",
	"err.exited":          "claude đã thoát: %s",
	"err.no.reply":        "claude không phản hồi %s",
	"err.no.stderr":       "không có stderr",
	"err.ctl.failed":      "%s thất bại",
	"err.unsupported":     "telegram-terminal không hỗ trợ %s",

	// ---- /perm ----
	"perm.desc.manual":            "hỏi mọi thứ cần quyền (mặc định)",
	"perm.desc.acceptEdits":       "tự cho sửa file, tool khác vẫn hỏi",
	"perm.desc.auto":              "để model tự phán cho phép/từ chối",
	"perm.desc.dontAsk":           "không hỏi; cái chưa được cho phép trước thì từ chối",
	"perm.desc.plan":              "chỉ lập kế hoạch, không thao tác",
	"perm.desc.bypassPermissions": "bỏ qua mọi kiểm tra (cần claude_args [\"--dangerously-skip-permissions\"])",
	"perm.head":                   "🔧 Chế độ quyền: <b>%s</b>",
	"perm.live":                   " (đang áp cho phiên đang mở)",
	"perm.next":                   " (sẽ áp cho phiên mở tiếp theo)",
	"perm.hint":                   "\nBấm nút hoặc gõ <code>/perm &lt;tên&gt;</code>.",
	"perm.invalid":                "⚠️ chế độ không hợp lệ: %s\nChọn một trong: <code>%s</code>",
	"perm.failed":                 "⚠️ không đổi được chế độ: %s",
	"perm.bypass.hint":            "\n\nMuốn dùng chế độ này thì đặt <code>\"claude_args\": [\"--dangerously-skip-permissions\"]</code> trong config rồi /session new.",
	"perm.changed":                "🔧 Chế độ quyền giờ là <b>%s</b> — %s.",
	"perm.changed.next":           " (áp dụng cho phiên Claude tiếp theo)",

	// ---- /lang ----
	"lang.head":    "🌐 Ngôn ngữ: <b>%s</b>",
	"lang.hint":    "\nBấm nút hoặc gõ <code>/lang &lt;mã&gt;</code>.",
	"lang.invalid": "⚠️ không có ngôn ngữ: %s\nĐang hỗ trợ: <code>%s</code>",
	"lang.changed": "🌐 Đã đổi ngôn ngữ sang <b>%s</b>.",

	// ---- /version ----
	"ver.line":         "📦 <b>telegram-terminal</b> <code>%s</code> · %s",
	"ver.newer":        "\n\n🆙 Có bản mới: <b>%s</b>\n%s",
	"ver.uptodate":     "\n\n✅ Đang chạy bản mới nhất (release: <b>%s</b>).",
	"ver.dev":          "\n\nBinary tự build — không có số version để đối chiếu.\nRelease mới nhất: <b>%s</b>\n%s",
	"ver.no.release":   "\n\n<i>%s chưa phát hành bản nào.</i>",
	"ver.check.failed": "\n\n⚠️ Không gọi được GitHub: %s",
	"update.alert":     "🆙 <b>telegram-terminal %s</b> đã ra — máy này đang chạy <b>%s</b>\n%s",
	"update.howto":     "\n\nCập nhật (config giữ nguyên):\n<pre>%s</pre>",

	// ---- /session ----
	"sess.head":          "📚 Phiên Claude tại 📁 <code>%s</code>\n\n",
	"sess.none":          "Chưa có phiên nào được lưu cho thư mục này.\n",
	"sess.unknown":       "(không rõ)",
	"sess.open.mark":     " ▶️ đang mở",
	"sess.open.other":    "\n▶️ Đang mở phiên <code>%s</code> ở 📁 <code>%s</code>\n",
	"sess.hint":          "\n/session &lt;số|id&gt; để mở lại · /session new để mở phiên mới.",
	"sess.err.number":    "không có phiên số %d (đang liệt kê %d phiên)",
	"sess.err.not.found": "không tìm thấy phiên %q",
	"sess.err.ambiguous": "%q khớp %d phiên, gõ thêm ký tự",
	"sess.err.missing":   "thiếu tham số",
	"sess.usage":         "Dùng: /session &lt;số|id&gt; — gõ /session để xem danh sách.",
	"sess.new":           "🆕 Prompt tiếp theo sẽ mở phiên Claude mới tại 📁 %s",
	"sess.new.closed":    "🆕 Đã đóng phiên Claude cũ. ",
	"sess.busy":          "⏳ Đang chạy lệnh khác. Gõ /cancel rồi thử lại.",
	"sess.resolve.error": "⚠️ %s\nGõ /session để xem danh sách.",
	"sess.resume.error":  "⚠️ không mở lại được phiên: %s",
	"sess.resumed":       "↩️ Đã mở lại phiên <code>%s</code> · 📁 <code>%s</code>\n%s · gửi prompt để tiếp tục.",

	// ---- ảnh & file ----
	"media.inline":         "- %s — đã nhúng ở trên, bản gốc: %s",
	"media.too.big":        "- %s — ảnh quá lớn để nhúng, dùng tool Read để xem: %s",
	"media.prompt.header":  "Người dùng gửi kèm qua Telegram:",
	"media.default.prompt": "Xem file đính kèm.",
	"media.unsupported":    "⚠️ Chưa hỗ trợ gửi %s cho Claude. Ảnh và file thì được.",
	"media.download.error": "⚠️ không tải được file: %s",
	"media.shell.saved":    "📎 Đã lưu <code>%s</code> (%s)\nĐang ở chế độ shell nên chưa gửi cho Claude — /c rồi gửi lại là Claude xem được ảnh.",
	"media.note.too.big":   "📎 %s (%s) lớn hơn giới hạn nhúng %s — Claude sẽ tự đọc file bằng tool Read.",
	"media.note.not.image": "📎 %s (%s) không phải ảnh — đã lưu, Claude sẽ tự đọc file nếu cần.",
	"media.type.video":     "video",
	"media.type.animation": "GIF động",
	"media.type.voice":     "tin nhắn thoại",
	"media.type.audio":     "audio",
	"media.err.file.path":  "getFile không trả về file_path",
	"media.err.http":       "tải file: HTTP %d",
	"media.err.too.big":    "file lớn hơn %s",
}}
