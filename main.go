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

	// Ngôn ngữ giao diện mặc định: "en" (mặc định) hoặc "vi". Mỗi chat đổi
	// riêng được bằng /lang.
	Language string `json:"language"`

	// Nhắc khi có bản mới trên GitHub Releases (xem update.go).
	UpdateRepo       string `json:"update_repo"`        // "" = thehao1505/telegram-terminal
	UpdateCheckHours int    `json:"update_check_hours"` // 0 = 24h, số âm = tắt
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

// imageDefaultPrompt: rỗng = dùng chuỗi theo ngôn ngữ của chat.
func (c *Config) imageDefaultPrompt() string {
	return strings.TrimSpace(c.ImageDefaultPrompt)
}

// language: ngôn ngữ mặc định cho mọi chat.
func (c *Config) language() *language {
	if l := languageByCode(c.Language); l != nil {
		return l
	}
	return langEN
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
	if v := os.Getenv("TT_LANG"); v != "" {
		c.Language = v
	}
	if v := os.Getenv("TT_UPDATE_REPO"); v != "" {
		c.UpdateRepo = v
	}
	if v := os.Getenv("TT_UPDATE_CHECK_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.UpdateCheckHours = n
		}
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
	ghBase     string // tiền tố URL GitHub API (test thay bằng server giả)
	pollClient *http.Client
	sendClient *http.Client
	sessions   map[int64]*Session
	langs      map[int64]string // chatID -> mã ngôn ngữ, "" = theo config (giữ bởi smu)
	smu        sync.Mutex
	allowed    map[int64]bool
	hostname   string
	upd        updateState // bản mới nhất biết được (xem update.go)

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
		ghBase:     defaultGitHubAPI,
		pollClient: &http.Client{Timeout: 70 * time.Second},
		sendClient: &http.Client{Timeout: 30 * time.Second},
		sessions:   map[int64]*Session{},
		langs:      map[int64]string{},
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

// lang trả về ngôn ngữ của chat: /lang đã chọn, không thì theo config.
func (b *Bot) lang(chatID int64) *language {
	b.smu.Lock()
	code := b.langs[chatID]
	b.smu.Unlock()
	if l := languageByCode(code); l != nil {
		return l
	}
	return b.cfg.language()
}

// t: chuỗi giao diện cho chat này. Dùng ở mọi nơi bot nói với người dùng.
func (b *Bot) t(chatID int64, key string, args ...any) string {
	return b.lang(chatID).t(key, args...)
}

// setLang ghi nhớ ngôn ngữ cho chat.
func (b *Bot) setLang(chatID int64, code string) {
	b.smu.Lock()
	if b.langs == nil {
		b.langs = map[int64]string{}
	}
	b.langs[chatID] = code
	b.smu.Unlock()
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
		f.b.send(f.chatID, f.b.t(f.chatID, "output.truncated"), false)
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
		parts = append(parts, b.t(chatID, "shell.timeout"))
	case ctx.Err() == context.Canceled:
		parts = append(parts, b.t(chatID, "shell.cancelled"))
	default:
		if !haveMeta && waitErr != nil {
			parts = append(parts, "⚠️ "+waitErr.Error())
		}
		if f.sent == 0 && haveMeta && exitCode == 0 {
			parts = append(parts, b.t(chatID, "shell.nooutput"))
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
		b.send(chatID, b.t(chatID, "busy"), false)
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
			b.send(chatID, b.t(chatID, "no.allowlist", userID), true)
		} else {
			log.Printf("rejected user %d (@%s)", userID, msg.From.Username)
			b.send(chatID, b.t(chatID, "denied"), false)
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
		b.send(chatID, b.helpText(chatID), true)
		return

	// Mọi thứ "xem trạng thái" gom về /status (/pwd là tên gọi quen tay).
	case "/status", "/mode", "/st", "/pwd":
		b.send(chatID, b.statusText(chatID, sess), true)
		return

	case "/cancel", "/stop":
		sess.mu.Lock()
		c := sess.cancel
		sess.mu.Unlock()
		if c != nil {
			c()
			b.send(chatID, b.t(chatID, "cancelling"), false)
		} else {
			b.send(chatID, b.t(chatID, "nothing.running"), false)
		}
		return

	case "/reset":
		b.stopClaude(sess)
		sess.mu.Lock()
		sess.cwd = b.cfg.StartDir
		sess.claudeMode = false
		sess.permMode = ""
		sess.mu.Unlock()
		b.send(chatID, b.t(chatID, "reset.done", b.cfg.StartDir), false)
		return

	// /sh <lệnh> chạy shell; /sh không tham số = chuyển sang chế độ shell.
	case "/sh", "/shell":
		if arg == "" {
			sess.mu.Lock()
			sess.claudeMode = false
			sess.mu.Unlock()
			b.send(chatID, b.t(chatID, "mode.shell"), false)
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
			b.send(chatID, b.t(chatID, "mode.claude"), false)
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

	// /update là tên gọi quen tay: bot không tự cài, chỉ cho biết bản nào mới.
	case "/version", "/ver", "/update":
		b.send(chatID, b.versionText(chatID), true)
		return

	case "/lang", "/language":
		if arg == "" {
			text, kb := b.langStatus(chatID)
			b.sendText(chatID, text, true, kb)
			return
		}
		b.applyLang(chatID, arg)
		return

	case "/perm", "/permission":
		if !b.requireClaude(chatID) {
			return
		}
		if arg == "" {
			text, kb := b.permStatus(chatID, sess)
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
	b.send(chatID, b.t(chatID, "claude.disabled"), false)
	return false
}

// statusText: một chỗ xem toàn bộ trạng thái — chế độ shell/claude, thư mục,
// và phiên Claude đang mở (kèm chế độ quyền của chính phiên đó).
func (b *Bot) statusText(chatID int64, sess *Session) string {
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
	sb.WriteString(b.t(chatID, "status.head", htmlEscape(b.hostname), m, htmlEscape(cwd)))
	if rel := b.updateAvailable(); rel != nil {
		sb.WriteString(b.t(chatID, "status.update", htmlEscape(rel.TagName)))
	}
	if !b.cfg.ClaudeEnabled {
		sb.WriteString(b.t(chatID, "status.claude.off"))
		return sb.String()
	}
	if cs != nil && cs.alive() {
		sb.WriteString(b.t(chatID, "status.session", htmlEscape(cs.describe(b.lang(chatID)))))
		if cs.cwd != cwd {
			sb.WriteString(b.t(chatID, "status.cwd.drift", htmlEscape(cs.cwd)))
		}
	} else {
		sb.WriteString(b.t(chatID, "status.no.session", htmlEscape(normPermMode(def))))
	}
	sb.WriteString(b.t(chatID, "status.footer"))
	return sb.String()
}

// handleCallback xử lý nút bấm: trả lời yêu cầu quyền của Claude
// (callback_data: "ca|<a|A|d>|<request_id>"), chọn đáp án cho câu hỏi của
// Claude ("q|…", xem askq.go), đổi chế độ quyền ("pm|…") và đổi ngôn ngữ ("lg|…").
func (b *Bot) handleCallback(q *callbackQuery) {
	if q.From == nil || q.Message == nil || q.Message.Chat == nil {
		return
	}
	if !b.allowed[q.From.ID] {
		b.answerCallback(q.ID, b.t(q.Message.Chat.ID, "denied"))
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
	// lg|<code>: đổi ngôn ngữ.
	if len(parts) == 2 && parts[0] == "lg" {
		b.answerCallback(q.ID, parts[1])
		b.applyLang(chatID, parts[1])
		return
	}
	// q|<ask>|…: trả lời câu hỏi AskUserQuestion (xem askq.go).
	if len(parts) >= 3 && parts[0] == "q" {
		b.handleQuestionCallback(q, chatID, parts[1:])
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
		b.answerCallback(q.ID, b.t(chatID, "cb.stale"))
		return
	}
	log.Printf("permission: chat %d, user %d -> %s (always=%v)", chatID, q.From.ID, dec.behavior, dec.always)
	b.answerCallback(q.ID, verdictLine(b.lang(chatID), dec))
}

func (b *Bot) helpText(chatID int64) string {
	claudeLine := b.t(chatID, "help.claude.off")
	if b.cfg.ClaudeEnabled {
		claudeLine = b.t(chatID, "help.claude.on")
	}
	return b.t(chatID, "help.text", b.hostname, claudeLine, languageCodes(), b.cfg.claudeAskTimeout())
}

// langStatus dựng nội dung + nút cho lệnh /lang.
func (b *Bot) langStatus(chatID int64) (string, []byte) {
	cur := b.lang(chatID)
	var sb strings.Builder
	sb.WriteString(b.t(chatID, "lang.head", htmlEscape(cur.Native)))
	sb.WriteString("\n\n")
	var row []ikButton
	for _, l := range languages {
		mark, label := "•", l.Native
		if l.Code == cur.Code {
			mark, label = "✅", "✅ "+label
		}
		fmt.Fprintf(&sb, "%s <code>%s</code> — %s\n", mark, l.Code, htmlEscape(l.Native))
		row = append(row, ikButton{Text: label, Data: "lg|" + l.Code})
	}
	sb.WriteString(b.t(chatID, "lang.hint"))
	return sb.String(), inlineKeyboard(row)
}

// applyLang đổi ngôn ngữ của chat. Giữ qua /reset vì đây là lựa chọn của người dùng.
func (b *Bot) applyLang(chatID int64, code string) {
	l := languageByCode(code)
	if l == nil {
		b.send(chatID, b.t(chatID, "lang.invalid", htmlEscape(code), languageCodes()), true)
		return
	}
	b.setLang(chatID, l.Code)
	log.Printf("language: chat %d -> %s", chatID, l.Code)
	b.send(chatID, b.t(chatID, "lang.changed", htmlEscape(l.Native)), true)
}

// -------------------------------- Loop --------------------------------

func (b *Bot) run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go b.watchUpdates(ctx)
	var offset int64
	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down…")
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
	cfgPath := flag.String("config", "", "path to the JSON config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	// Trước cả việc đọc config: install.sh gọi cái này để in bản vừa cài.
	if *showVersion {
		fmt.Println(versionLine())
		return
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.BotToken == "" {
		log.Fatal("bot_token is not configured (use the config file or the TT_BOT_TOKEN env var)")
	}

	cleanupAttachments(cfg.imageDir(), cfg.imageKeepHours())

	b := newBot(cfg)
	log.Printf("%s started on host %q — %d allowed user(s), claude=%v, lang=%s, attachments in %s",
		versionLine(), b.hostname, len(cfg.AllowedUserIDs), cfg.ClaudeEnabled, cfg.language().Code, cfg.imageDir())
	if len(cfg.AllowedUserIDs) == 0 {
		log.Println("WARNING: the allowlist is empty — the bot replies with the sender's User ID but will NOT run anything until you add an ID.")
	}
	b.run()
}
