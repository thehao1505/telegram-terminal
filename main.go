// telegram-terminal: điều khiển máy Ubuntu qua một bot Telegram.
//
// Gửi lệnh shell -> nhận output (streaming, giữ cwd giữa các lệnh).
// Gửi prompt cho Claude Code (chế độ claude) -> chạy `claude` headless dưới
// dạng SDK host (stream-json), stream chữ về Telegram và xin quyền bằng nút
// bấm. Xem claude.go.
//
// Không phụ thuộc thư viện ngoài: chỉ dùng standard library -> build ra 1
// binary tĩnh, copy sang máy Ubuntu nào cũng chạy.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tgHardLimit = 4096                    // giới hạn cứng 1 tin nhắn Telegram
	chunkBudget = 3000                    // ép flush khi buffer vượt mức này mà chưa có newline
	maxOutput   = 100_000                 // tối đa số byte output mỗi lệnh, tránh spam
	flushEvery  = 1200 * time.Millisecond // nhịp gửi output ra Telegram khi đang stream
)

// ------------------------------- Config -------------------------------

type Config struct {
	BotToken       string   `json:"bot_token"`
	AllowedUserIDs []int64  `json:"allowed_user_ids"`
	Shell          string   `json:"shell"`
	StartDir       string   `json:"start_dir"`
	ClaudeEnabled  bool     `json:"claude_enabled"`
	ClaudeBin      string   `json:"claude_bin"`
	ClaudeArgs     []string `json:"claude_args"`
	CommandTimeout int      `json:"command_timeout_seconds"` // 0 = không timeout

	// manual (mặc định) | acceptEdits | auto | dontAsk | plan | bypassPermissions
	ClaudePermissionMode string `json:"claude_permission_mode"`
	// Thời gian chờ người dùng bấm nút cho phép (0 = mặc định 300s).
	ClaudeAskTimeout int `json:"claude_ask_timeout_seconds"`

	// Ảnh/file gửi từ Telegram.
	ImageDir           string `json:"image_dir"`            // "" = <temp>/telegram-terminal
	ImageMaxBytes      int    `json:"image_max_bytes"`      // trên mức này chỉ đưa đường dẫn
	ImageKeepHours     int    `json:"image_keep_hours"`     // dọn file cũ lúc khởi động (mặc định 24, số âm = giữ mãi)
	ImageDefaultPrompt string `json:"image_default_prompt"` // prompt khi gửi ảnh không caption
}

func (c *Config) claudePermissionMode() string {
	if c.ClaudePermissionMode == "" {
		return "manual"
	}
	return c.ClaudePermissionMode
}

func (c *Config) claudeAskTimeout() int {
	if c.ClaudeAskTimeout <= 0 {
		return 300
	}
	return c.ClaudeAskTimeout
}

func (c *Config) imageDir() string {
	if c.ImageDir == "" {
		return filepath.Join(os.TempDir(), "telegram-terminal")
	}
	return c.ImageDir
}

// imageMaxBytes: ngưỡng nhúng base64. Mặc định 3.5MB vì base64 nở 4/3 lần, còn
// API Claude chỉ nhận ảnh tối đa 5MB sau khi mã hóa.
func (c *Config) imageMaxBytes() int {
	if c.ImageMaxBytes <= 0 {
		return 3_670_016
	}
	return c.ImageMaxBytes
}

func (c *Config) imageKeepHours() int {
	if c.ImageKeepHours == 0 {
		return 24
	}
	if c.ImageKeepHours < 0 {
		return 0 // giữ mãi
	}
	return c.ImageKeepHours
}

func (c *Config) imageDefaultPrompt() string {
	if strings.TrimSpace(c.ImageDefaultPrompt) == "" {
		return "Xem file đính kèm."
	}
	return c.ImageDefaultPrompt
}

func (c *Config) applyDefaults() {
	if c.Shell == "" {
		c.Shell = "/bin/bash"
	}
	if c.StartDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			c.StartDir = h
		} else {
			c.StartDir = "/"
		}
	}
	if c.ClaudeBin == "" {
		c.ClaudeBin = "claude"
	}
}

func loadConfig(path string) (Config, error) {
	var c Config
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
	}
	// Biến môi trường ghi đè config file (tiện cho systemd / secret).
	if v := os.Getenv("TT_BOT_TOKEN"); v != "" {
		c.BotToken = v
	}
	if v := os.Getenv("TT_ALLOWED_USER_IDS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if id, err := strconv.ParseInt(p, 10, 64); err == nil {
				c.AllowedUserIDs = append(c.AllowedUserIDs, id)
			}
		}
	}
	if v := os.Getenv("TT_SHELL"); v != "" {
		c.Shell = v
	}
	if v := os.Getenv("TT_START_DIR"); v != "" {
		c.StartDir = v
	}
	if v := os.Getenv("TT_CLAUDE_BIN"); v != "" {
		c.ClaudeBin = v
		c.ClaudeEnabled = true
	}
	if v := os.Getenv("TT_CLAUDE_ENABLED"); v == "1" || v == "true" {
		c.ClaudeEnabled = true
	}
	if v := os.Getenv("TT_CLAUDE_PERMISSION_MODE"); v != "" {
		c.ClaudePermissionMode = v
	}
	if v := os.Getenv("TT_IMAGE_DIR"); v != "" {
		c.ImageDir = v
	}
	if v := os.Getenv("TT_IMAGE_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ImageMaxBytes = n
		}
	}
	c.applyDefaults()
	return c, nil
}

// ------------------------------- Session ------------------------------

type Session struct {
	mu         sync.Mutex
	cwd        string
	claudeMode bool
	claude     *claudeSession // tiến trình claude đang sống của chat (nil = chưa mở)
	permMode   string         // chế độ quyền của chat (rỗng = theo config)
	running    bool
	cancel     context.CancelFunc
}

// --------------------------------- Bot --------------------------------

type Bot struct {
	cfg        Config
	apiBase    string // tiền tố URL Bot API (test thay bằng server giả)
	fileBase   string // tiền tố URL tải file đính kèm
	pollClient *http.Client
	sendClient *http.Client
	sessions   map[int64]*Session
	smu        sync.Mutex
	allowed    map[int64]bool
	hostname   string

	amu    sync.Mutex
	albums map[string]*albumBuf // ảnh cùng album đang chờ gom, theo media_group_id
}

func newBot(cfg Config) *Bot {
	allowed := map[int64]bool{}
	for _, id := range cfg.AllowedUserIDs {
		allowed[id] = true
	}
	host, _ := os.Hostname()
	return &Bot{
		cfg:        cfg,
		apiBase:    "https://api.telegram.org/bot" + cfg.BotToken + "/",
		fileBase:   "https://api.telegram.org/file/bot" + cfg.BotToken + "/",
		pollClient: &http.Client{Timeout: 70 * time.Second},
		sendClient: &http.Client{Timeout: 30 * time.Second},
		sessions:   map[int64]*Session{},
		allowed:    allowed,
		hostname:   host,
		albums:     map[string]*albumBuf{},
	}
}

func (b *Bot) session(chatID int64) *Session {
	b.smu.Lock()
	defer b.smu.Unlock()
	s := b.sessions[chatID]
	if s == nil {
		s = &Session{cwd: b.cfg.StartDir}
		b.sessions[chatID] = s
	}
	return s
}

// --------------------------- Telegram API -----------------------------

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *tgMessage     `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type tgMessage struct {
	MessageID int64 `json:"message_id"`
	From      *struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Chat *struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`

	// File đính kèm. Caption là phần chữ đi kèm ảnh/file; MediaGroupID khác
	// rỗng khi người dùng gửi nhiều ảnh một lần (album).
	Caption      string        `json:"caption"`
	MediaGroupID string        `json:"media_group_id"`
	Photo        []tgPhotoSize `json:"photo"`
	Document     *tgFile       `json:"document"`
	Sticker      *tgFile       `json:"sticker"`
	Video        *tgFile       `json:"video"`
	Animation    *tgFile       `json:"animation"`
	Voice        *tgFile       `json:"voice"`
	Audio        *tgFile       `json:"audio"`
}

// tgPhotoSize: một trong nhiều bản kích cỡ khác nhau của cùng một ảnh.
type tgPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size"`
}

// tgFile: phần chung của document/sticker/video/… — đủ để tải về.
type tgFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
}

// callbackQuery: người dùng bấm 1 nút inline (nút cho phép/từ chối của Claude).
type callbackQuery struct {
	ID   string `json:"id"`
	From *struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Message *struct {
		MessageID int64 `json:"message_id"`
		Chat      *struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	Data string `json:"data"`
}

func (b *Bot) api(method string) string {
	return b.apiBase + method
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]Update, error) {
	v := url.Values{}
	v.Set("timeout", "50")
	v.Set("offset", strconv.FormatInt(offset, 10))
	v.Set("allowed_updates", `["message","callback_query"]`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.api("getUpdates")+"?"+v.Encode(), nil)
	resp, err := b.pollClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool     `json:"ok"`
		Result      []Update `json:"result"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, fmt.Errorf("telegram: %s", r.Description)
	}
	return r.Result, nil
}

// send gửi 1 tin nhắn, bỏ qua message_id. html=true để dùng parse_mode HTML.
func (b *Bot) send(chatID int64, text string, html bool) {
	b.sendText(chatID, text, html, nil)
}

// sendText gửi 1 tin nhắn và trả về message_id (0 nếu thất bại).
// markup != nil -> gắn inline keyboard.
func (b *Bot) sendText(chatID int64, text string, html bool, markup []byte) int64 {
	if text == "" {
		return 0
	}
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("text", text)
	if html {
		v.Set("parse_mode", "HTML")
	}
	v.Set("disable_web_page_preview", "true")
	if markup != nil {
		v.Set("reply_markup", string(markup))
	}
	id, _ := b.apiCall("sendMessage", v, html)
	return id
}

// editText sửa 1 tin nhắn đã gửi: dùng để stream chữ dần và để bỏ nút bấm.
func (b *Bot) editText(chatID, msgID int64, text string, html bool, markup []byte) bool {
	if msgID == 0 || text == "" {
		return false
	}
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("message_id", strconv.FormatInt(msgID, 10))
	v.Set("text", text)
	if html {
		v.Set("parse_mode", "HTML")
	}
	v.Set("disable_web_page_preview", "true")
	if markup != nil {
		v.Set("reply_markup", string(markup))
	}
	_, ok := b.apiCall("editMessageText", v, html)
	return ok
}

// answerCallback tắt vòng xoay trên nút vừa bấm (kèm toast tùy chọn).
func (b *Bot) answerCallback(id, text string) {
	v := url.Values{}
	v.Set("callback_query_id", id)
	if text != "" {
		v.Set("text", text)
	}
	b.apiCall("answerCallbackQuery", v, false)
}

// apiCall gọi Bot API: retry khi 429, thử lại dạng plain text nếu HTML lỗi.
// Trả về message_id (nếu API trả về 1 Message) và trạng thái thành công.
func (b *Bot) apiCall(method string, v url.Values, html bool) (int64, bool) {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := b.sendClient.PostForm(b.api(method), v)
		if err != nil {
			log.Printf("%s: %v", method, err)
			time.Sleep(time.Second)
			continue
		}
		if resp.StatusCode == 429 {
			var r struct {
				Parameters struct {
					RetryAfter int `json:"retry_after"`
				} `json:"parameters"`
			}
			json.NewDecoder(resp.Body).Decode(&r)
			resp.Body.Close()
			d := r.Parameters.RetryAfter
			if d <= 0 {
				d = 1
			}
			time.Sleep(time.Duration(d) * time.Second)
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s := string(body)
			// Sửa tin nhắn nhưng nội dung không đổi -> coi như xong.
			if strings.Contains(s, "message is not modified") {
				return 0, true
			}
			log.Printf("%s status %d: %s", method, resp.StatusCode, s)
			// Nếu lỗi do parse HTML thì thử lại dạng plain text.
			if html {
				html = false
				v.Del("parse_mode")
				continue
			}
			return 0, false
		}
		var r struct {
			Result struct {
				MessageID int64 `json:"message_id"`
			} `json:"result"`
		}
		json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		return r.Result.MessageID, true
	}
	return 0, false
}

// ikButton: 1 nút inline. inlineKeyboard() dựng reply_markup từ các hàng nút.
type ikButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

func inlineKeyboard(rows ...[]ikButton) []byte {
	data, _ := json.Marshal(map[string]any{"inline_keyboard": rows})
	return data
}

// emptyKeyboard xóa toàn bộ nút của 1 tin nhắn.
func emptyKeyboard() []byte {
	return []byte(`{"inline_keyboard":[]}`)
}

func (b *Bot) typing(chatID int64) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("action", "typing")
	if resp, err := b.sendClient.PostForm(b.api("sendChatAction"), v); err == nil {
		resp.Body.Close()
	}
}

// sendChunked chia text thành nhiều tin nhắn vừa giới hạn Telegram.
// code=true -> bọc trong <pre> (monospace) cho output shell.
func (b *Bot) sendChunked(chatID int64, text string, code bool) {
	if text == "" {
		return
	}
	if code {
		for _, c := range splitByCost(text, tgHardLimit-20, runeCostEscaped) {
			b.send(chatID, "<pre>"+htmlEscape(c)+"</pre>", true)
		}
	} else {
		for _, c := range splitByCost(text, tgHardLimit-10, runeCostPlain) {
			b.send(chatID, c, false)
		}
	}
}

// ------------------------------ Flusher -------------------------------
// flusher nhận stdout+stderr của tiến trình con và đẩy dần ra Telegram.

type flusher struct {
	b         *Bot
	chatID    int64
	code      bool
	mu        sync.Mutex
	buf       bytes.Buffer
	sent      int
	truncated bool
}

func (f *flusher) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.buf.Write(p)
	f.mu.Unlock()
	return len(p), nil
}

func (f *flusher) loop(stop <-chan struct{}) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			f.drain(false)
		}
	}
}

// drain đẩy nội dung trong buffer ra Telegram. all=true -> đẩy hết (khi kết thúc).
func (f *flusher) drain(all bool) {
	f.mu.Lock()
	if f.truncated || f.buf.Len() == 0 {
		f.mu.Unlock()
		return
	}
	data := f.buf.String()
	var out string
	if all {
		out = data
		f.buf.Reset()
	} else {
		// Cắt tại newline cuối để không xé ngang dòng; giữ phần dư lại.
		i := strings.LastIndexByte(data, '\n')
		if i < 0 {
			if f.buf.Len() < chunkBudget {
				f.mu.Unlock()
				return
			}
			out = data
			f.buf.Reset()
		} else {
			out = data[:i+1]
			rem := data[i+1:]
			f.buf.Reset()
			f.buf.WriteString(rem)
		}
	}
	if f.sent+len(out) > maxOutput {
		allow := maxOutput - f.sent
		if allow < 0 {
			allow = 0
		}
		out = out[:allow]
		f.truncated = true
	}
	f.sent += len(out)
	f.mu.Unlock()

	if out != "" {
		f.b.sendChunked(f.chatID, out, f.code)
	}
	if f.truncated {
		f.b.send(f.chatID, "… (output quá dài, đã cắt bớt)", false)
	}
}

// streamCmd chạy cmd, stream stdout+stderr ra Telegram, kill cả process group
// khi ctx bị hủy/timeout. Trả về flusher (để đọc f.sent) và lỗi Wait().
func (b *Bot) streamCmd(ctx context.Context, chatID int64, cmd *exec.Cmd, code bool) (*flusher, error) {
	f := &flusher{b: b, chatID: chatID, code: code}
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return f, err
	}
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			// Kill cả nhóm tiến trình để diệt luôn tiến trình con.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	stop := make(chan struct{})
	go f.loop(stop)
	err := cmd.Wait()
	close(stop)
	f.drain(true)
	return f, err
}

// ------------------------------ Runners -------------------------------

func (b *Bot) runShell(ctx context.Context, chatID int64, sess *Session, cmdline string) {
	sess.mu.Lock()
	oldCwd := sess.cwd
	sess.mu.Unlock()

	meta, err := os.CreateTemp("", "tt-meta-*")
	if err != nil {
		b.send(chatID, "⚠️ temp: "+err.Error(), false)
		return
	}
	metaPath := meta.Name()
	meta.Close()
	defer os.Remove(metaPath)

	// Chạy trong 1 shell process duy nhất: cd vào cwd đã lưu, chạy lệnh (cd của
	// user sẽ có hiệu lực), rồi ghi PWD + exit code ra file meta để lấy lại.
	script := fmt.Sprintf("cd %s 2>/dev/null\n%s\n__tt_ec=$?\nprintf '%%s\\n%%d\\n' \"$PWD\" \"$__tt_ec\" > %s 2>/dev/null\n",
		shellQuote(oldCwd), cmdline, shellQuote(metaPath))
	cmd := exec.CommandContext(ctx, b.cfg.Shell, "-c", script)
	f, waitErr := b.streamCmd(ctx, chatID, cmd, true)

	newCwd := oldCwd
	exitCode := 0
	haveMeta := false
	if data, e := os.ReadFile(metaPath); e == nil {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) >= 2 {
			if strings.TrimSpace(lines[0]) != "" {
				newCwd = lines[0]
			}
			if ec, e2 := strconv.Atoi(strings.TrimSpace(lines[1])); e2 == nil {
				exitCode = ec
				haveMeta = true
			}
		}
	}
	if newCwd != oldCwd {
		sess.mu.Lock()
		sess.cwd = newCwd
		sess.mu.Unlock()
	}

	var parts []string
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		parts = append(parts, "⏱️ hết thời gian, đã hủy")
	case ctx.Err() == context.Canceled:
		parts = append(parts, "🛑 đã hủy")
	default:
		if !haveMeta && waitErr != nil {
			parts = append(parts, "⚠️ "+waitErr.Error())
		}
		if f.sent == 0 && haveMeta && exitCode == 0 {
			parts = append(parts, "✓ (không có output)")
		}
		if exitCode != 0 {
			parts = append(parts, fmt.Sprintf("exit %d", exitCode))
		}
	}
	if newCwd != oldCwd {
		parts = append(parts, "📁 "+newCwd)
	}
	if len(parts) > 0 {
		b.send(chatID, strings.Join(parts, " · "), false)
	}
}

// ------------------------------ Dispatch ------------------------------

// execGuarded đảm bảo mỗi chat chỉ chạy 1 lệnh tại một thời điểm, gắn ctx có
// thể hủy qua /cancel và timeout (nếu cấu hình).
func (b *Bot) execGuarded(chatID int64, sess *Session, fn func(context.Context)) {
	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		b.send(chatID, "⏳ Đang bận chạy lệnh khác. Gõ /cancel để hủy.", false)
		return
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if to := b.cfg.CommandTimeout; to > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(to)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	sess.running = true
	sess.cancel = cancel
	sess.mu.Unlock()

	defer func() {
		cancel()
		sess.mu.Lock()
		sess.running = false
		sess.cancel = nil
		sess.mu.Unlock()
	}()

	b.typing(chatID)
	fn(ctx)
}

func (b *Bot) handle(u Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(u.CallbackQuery)
		return
	}
	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return
	}
	userID := msg.From.ID
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption) // ảnh/file dùng caption làm prompt
	}
	if text == "" && !msg.hasMedia() {
		return
	}

	// Kiểm soát quyền.
	if !b.allowed[userID] {
		if len(b.cfg.AllowedUserIDs) == 0 {
			b.send(chatID, fmt.Sprintf("Bot chưa cấu hình allowlist.\nUser ID của bạn: <b>%d</b>\nThêm ID này vào \"allowed_user_ids\" rồi khởi động lại bot.", userID), true)
		} else {
			log.Printf("từ chối user %d (@%s)", userID, msg.From.Username)
			b.send(chatID, "⛔ Bạn không có quyền dùng bot này.", false)
		}
		return
	}

	sess := b.session(chatID)

	// Có file đính kèm -> đi đường media (caption làm prompt, không coi là lệnh).
	if msg.hasMedia() {
		b.handleMedia(chatID, sess, msg, text)
		return
	}

	cmd, arg := splitCmd(text)
	arg = strings.TrimSpace(arg)

	switch cmd {
	case "/start", "/help":
		b.send(chatID, b.helpText(), true)
		return

	// Mọi thứ "xem trạng thái" gom về /status (/pwd là tên gọi quen tay).
	case "/status", "/mode", "/st", "/pwd":
		b.send(chatID, b.statusText(sess), true)
		return

	case "/cancel", "/stop":
		sess.mu.Lock()
		c := sess.cancel
		sess.mu.Unlock()
		if c != nil {
			c()
			b.send(chatID, "🛑 Đang hủy…", false)
		} else {
			b.send(chatID, "Không có lệnh nào đang chạy.", false)
		}
		return

	case "/reset":
		b.stopClaude(sess)
		sess.mu.Lock()
		sess.cwd = b.cfg.StartDir
		sess.claudeMode = false
		sess.permMode = ""
		sess.mu.Unlock()
		b.send(chatID, "🔄 Đã đóng phiên Claude, về chế độ shell và quyền theo config.\nThư mục làm việc trở về 📁 "+b.cfg.StartDir+" (không xóa gì trên đĩa).", false)
		return

	// /sh <lệnh> chạy shell; /sh không tham số = chuyển sang chế độ shell.
	case "/sh", "/shell":
		if arg == "" {
			sess.mu.Lock()
			sess.claudeMode = false
			sess.mu.Unlock()
			b.send(chatID, "🖥️ Chế độ shell — gửi lệnh bất kỳ. /c để sang Claude.", false)
			return
		}
		b.execGuarded(chatID, sess, func(ctx context.Context) { b.runShell(ctx, chatID, sess, arg) })
		return

	// /c <prompt> hỏi Claude; /c không tham số = chuyển sang chế độ Claude.
	case "/claude", "/c":
		if !b.requireClaude(chatID) {
			return
		}
		sess.mu.Lock()
		sess.claudeMode = true
		sess.mu.Unlock()
		if arg == "" {
			b.send(chatID, "🤖 Chế độ Claude — gửi prompt bất kỳ.\n/sh để sang shell · /session để xem phiên · /perm để đổi quyền.", false)
			return
		}
		b.execGuarded(chatID, sess, func(ctx context.Context) { b.runClaude(ctx, chatID, sess, textPrompt(arg)) })
		return

	// Mọi việc về phiên gom về /session (xem sessionCmd).
	case "/session", "/sessions", "/ss", "/newchat", "/resume", "/r":
		if !b.requireClaude(chatID) {
			return
		}
		b.sessionCmd(chatID, sess, cmd, arg)
		return

	case "/perm", "/permission":
		if !b.requireClaude(chatID) {
			return
		}
		if arg == "" {
			text, kb := b.permStatus(sess)
			b.sendText(chatID, text, true, kb)
			return
		}
		b.setPermMode(chatID, sess, arg)
		return
	}

	// Không phải lệnh của bot -> chạy theo chế độ hiện tại.
	sess.mu.Lock()
	claudeMode := sess.claudeMode
	sess.mu.Unlock()
	if claudeMode && b.cfg.ClaudeEnabled {
		b.execGuarded(chatID, sess, func(ctx context.Context) { b.runClaude(ctx, chatID, sess, textPrompt(text)) })
	} else {
		b.execGuarded(chatID, sess, func(ctx context.Context) { b.runShell(ctx, chatID, sess, text) })
	}
}

// requireClaude báo lỗi và trả false nếu chế độ Claude chưa được bật.
func (b *Bot) requireClaude(chatID int64) bool {
	if b.cfg.ClaudeEnabled {
		return true
	}
	b.send(chatID, "Claude chưa được bật trong cấu hình (claude_enabled=false).", false)
	return false
}

// statusText: một chỗ xem toàn bộ trạng thái — chế độ shell/claude, thư mục,
// và phiên Claude đang mở (kèm chế độ quyền của chính phiên đó).
func (b *Bot) statusText(sess *Session) string {
	sess.mu.Lock()
	cwd, claudeMode, cs, def := sess.cwd, sess.claudeMode, sess.claude, sess.permMode
	sess.mu.Unlock()
	if def == "" {
		def = b.cfg.claudePermissionMode()
	}
	m := "shell"
	if claudeMode {
		m = "claude"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥️ <b>%s</b> · chế độ: <b>%s</b>\n📁 <code>%s</code>\n",
		htmlEscape(b.hostname), m, htmlEscape(cwd))
	if !b.cfg.ClaudeEnabled {
		sb.WriteString("🤖 Claude: ❌ chưa bật trong cấu hình")
		return sb.String()
	}
	if cs != nil && cs.alive() {
		fmt.Fprintf(&sb, "🤖 Phiên: %s", htmlEscape(cs.describe()))
		if cs.cwd != cwd {
			fmt.Fprintf(&sb, "\n⚠️ Phiên đang ở 📁 <code>%s</code> — /newchat để mở lại ở thư mục hiện tại.",
				htmlEscape(cs.cwd))
		}
	} else {
		fmt.Fprintf(&sb, "🤖 Phiên: chưa mở · quyền <b>%s</b> sẽ áp khi mở",
			htmlEscape(normPermMode(def)))
	}
	sb.WriteString("\n\n/perm đổi quyền · /session đổi phiên · /session new mở phiên mới")
	return sb.String()
}

// handleCallback xử lý nút bấm — hiện chỉ có nút trả lời yêu cầu quyền của Claude
// (callback_data: "ca|<a|A|d>|<request_id>").
func (b *Bot) handleCallback(q *callbackQuery) {
	if q.From == nil || q.Message == nil || q.Message.Chat == nil {
		return
	}
	if !b.allowed[q.From.ID] {
		b.answerCallback(q.ID, "⛔ Bạn không có quyền dùng bot này.")
		return
	}
	chatID := q.Message.Chat.ID
	parts := strings.Split(q.Data, "|")

	// pm|<mode>: đổi chế độ quyền.
	if len(parts) == 2 && parts[0] == "pm" {
		b.answerCallback(q.ID, parts[1])
		b.setPermMode(chatID, b.session(chatID), parts[1])
		return
	}
	if len(parts) != 3 || parts[0] != "ca" {
		b.answerCallback(q.ID, "")
		return
	}
	var dec permDecision
	switch parts[1] {
	case "a":
		dec = permDecision{behavior: "allow"}
	case "A":
		dec = permDecision{behavior: "allow", always: true}
	default:
		dec = permDecision{behavior: "deny"}
	}

	sess := b.session(chatID)
	sess.mu.Lock()
	cs := sess.claude
	sess.mu.Unlock()
	if cs == nil || !cs.decide(parts[2], dec) {
		b.answerCallback(q.ID, "Yêu cầu này không còn chờ trả lời nữa.")
		return
	}
	log.Printf("quyền: chat %d, user %d -> %s (always=%v)", chatID, q.From.ID, dec.behavior, dec.always)
	b.answerCallback(q.ID, verdictLine(dec))
}

func (b *Bot) helpText() string {
	claudeLine := "❌ chưa bật"
	if b.cfg.ClaudeEnabled {
		claudeLine = "✅ đã bật"
	}
	return fmt.Sprintf(`<b>telegram-terminal</b> @ <b>%s</b>

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
/session &lt;số|id&gt; — mở lại một phiên trong danh sách
/session new — đóng phiên hiện tại, mở phiên mới ở thư mục hiện tại

<b>Cấu hình phiên</b>
/perm [chế độ] — đổi quyền: manual, acceptEdits, auto, dontAsk, plan…
/status — xem chế độ, thư mục, phiên Claude &amp; quyền
/reset — về thư mục mặc định, chế độ shell, quyền theo config &amp; đóng phiên
/help — trợ giúp

Câu trả lời của Claude được stream về theo thời gian thực. Khi cần chạy tool mà
chưa có quyền, bot gửi tin kèm nút <b>Cho phép</b> / <b>Từ chối</b> / <b>Cho
phép luôn</b> (tự động từ chối sau %d giây nếu không ai bấm).

<i>Tên gọi khác:</i> /shell=/sh · /claude=/c · /sessions,/ss,/newchat,/resume,/r=/session · /mode,/st,/pwd=/status · /stop=/cancel · /permission=/perm`,
		b.hostname, claudeLine, b.cfg.claudeAskTimeout())
}

// -------------------------------- Loop --------------------------------

func (b *Bot) run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var offset int64
	for {
		select {
		case <-ctx.Done():
			log.Println("đang tắt…")
			return
		default:
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("getUpdates: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			go b.handle(u)
		}
	}
}

// ------------------------------- Helpers ------------------------------

// splitCmd tách token lệnh đầu (nếu bắt đầu bằng "/") khỏi phần còn lại.
func splitCmd(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	i := strings.IndexAny(text, " \t\n")
	if i < 0 {
		return stripBotName(text), ""
	}
	return stripBotName(text[:i]), strings.TrimSpace(text[i+1:])
}

// stripBotName bỏ hậu tố @tenbot mà Telegram thêm trong group chat.
func stripBotName(c string) string {
	if j := strings.IndexByte(c, '@'); j >= 0 {
		return c[:j]
	}
	return c
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func runeCostEscaped(r rune) int {
	switch r {
	case '&':
		return 5 // &amp;
	case '<', '>':
		return 4 // &lt; / &gt;
	default:
		return len(string(r))
	}
}

func runeCostPlain(r rune) int { return len(string(r)) }

// splitByCost chia s thành các đoạn sao cho tổng "chi phí" mỗi đoạn <= max.
func splitByCost(s string, max int, cost func(rune) int) []string {
	var res []string
	var b strings.Builder
	cur := 0
	for _, r := range s {
		c := cost(r)
		if cur+c > max && b.Len() > 0 {
			res = append(res, b.String())
			b.Reset()
			cur = 0
		}
		b.WriteRune(r)
		cur += c
	}
	if b.Len() > 0 {
		res = append(res, b.String())
	}
	return res
}

// -------------------------------- main --------------------------------

func main() {
	cfgPath := flag.String("config", "", "đường dẫn file config JSON")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.BotToken == "" {
		log.Fatal("bot_token chưa được cấu hình (dùng file config hoặc biến môi trường TT_BOT_TOKEN)")
	}

	cleanupAttachments(cfg.imageDir(), cfg.imageKeepHours())

	b := newBot(cfg)
	log.Printf("telegram-terminal khởi động trên host %q — %d user được phép, claude=%v, ảnh lưu ở %s",
		b.hostname, len(cfg.AllowedUserIDs), cfg.ClaudeEnabled, cfg.imageDir())
	if len(cfg.AllowedUserIDs) == 0 {
		log.Println("CẢNH BÁO: allowlist trống — bot sẽ trả về User ID cho bất kỳ ai nhắn, nhưng KHÔNG chạy lệnh cho tới khi bạn thêm ID.")
	}
	b.run()
}
