// Chế độ Claude: chạy `claude` headless nhưng bot đóng vai "SDK host", nói
// chuyện với nó bằng JSON-lines (stream-json) trên stdin/stdout.
//
// Mỗi chat giữ MỘT tiến trình `claude` sống lâu (nhờ vậy ngữ cảnh hội thoại còn
// nguyên, không cần --continue). Giao thức:
//
//	bot   -> claude : {"type":"user","message":{...}}                  prompt
//	claude -> bot   : stream_event / assistant / user / result         nội dung
//	claude -> bot   : control_request{subtype:"can_use_tool", ...}     xin quyền
//	bot   -> claude : control_response{subtype:"success", ...}         quyết định
//
// Nhờ control_request/can_use_tool, khi Claude cần quyền chạy tool thì bot đẩy
// ra Telegram kèm nút Cho phép / Từ chối và chờ người dùng bấm.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	liveMsgMax    = 3500                    // số rune tối đa cho 1 tin nhắn đang stream
	liveEditEvery = 1500 * time.Millisecond // nhịp editMessageText khi stream chữ
	initTimeout   = 60 * time.Second        // chờ bắt tay initialize
	interruptWait = 10 * time.Second        // chờ claude dừng sau khi gửi interrupt
	ctlTimeout    = 15 * time.Second        // chờ trả lời 1 control_request thường
	toolDetailMax = 800                     // độ dài tối đa phần chi tiết tool
)

// --------------------------- Tin nhắn stream --------------------------
// liveMsg: một tin nhắn Telegram được sửa dần (editMessageText) khi chữ chảy về.
// Chỉ được dùng từ readLoop (một goroutine) nên không cần lock.

type liveMsg struct {
	b      *Bot
	chatID int64
	msgID  int64
	text   string // nội dung hiện tại
	shown  string // nội dung đã đẩy lên Telegram
	last   time.Time
}

func (m *liveMsg) append(s string) {
	if s == "" {
		return
	}
	m.text += s
	for utf8.RuneCountInString(m.text) > liveMsgMax {
		head, rest := splitRunes(m.text, liveMsgMax)
		m.text = head
		m.push()
		m.msgID, m.text, m.shown = 0, rest, ""
	}
	if time.Since(m.last) >= liveEditEvery {
		m.push()
	}
}

func (m *liveMsg) push() {
	t := strings.TrimRight(m.text, " \t\n")
	if t == "" || t == m.shown {
		return
	}
	if m.msgID == 0 {
		id := m.b.sendText(m.chatID, t, false, nil)
		if id == 0 {
			return // gửi lỗi -> giữ buffer, thử lại lần sau
		}
		m.msgID = id
	} else if !m.b.editText(m.chatID, m.msgID, t, false, nil) {
		return
	}
	m.shown = t
	m.last = time.Now()
}

// close chốt tin nhắn hiện tại; chữ tiếp theo sẽ mở tin nhắn mới.
func (m *liveMsg) close() {
	m.push()
	m.msgID, m.text, m.shown = 0, "", ""
}

// splitRunes cắt s tại tối đa n rune, ưu tiên cắt ở newline/space gần cuối.
func splitRunes(s string, n int) (string, string) {
	cut, i := len(s), 0
	for idx := range s {
		if i == n {
			cut = idx
			break
		}
		i++
	}
	head, rest := s[:cut], s[cut:]
	if j := strings.LastIndexAny(head, "\n "); j > len(head)*3/4 {
		head, rest = s[:j+1], s[j+1:]
	}
	return head, rest
}

// ------------------------------ Xin quyền -----------------------------

type permDecision struct {
	behavior string // "allow" | "deny"
	always   bool   // cho phép luôn cho phiên này (updatedPermissions)
	note     string
	canceled bool // claude đã rút lại yêu cầu
}

type pendingAsk struct {
	ch chan permDecision
}

// ---------------------------- Phiên Claude ----------------------------

type claudeSession struct {
	b      *Bot
	chatID int64
	cwd    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuf

	wmu sync.Mutex // ghi stdin

	mu       sync.Mutex
	pending  map[string]*pendingAsk    // yêu cầu quyền đang chờ người bấm nút
	ctl      map[string]chan ctlResult // control_request của bot đang chờ trả lời
	turn     chan struct{}             // đóng khi lượt hiện tại kết thúc
	sessID   string
	model    string
	permMode string // chế độ quyền phiên này đang chạy
	waitErr  error
	exited   chan struct{}

	live     liveMsg
	turnSent bool // đã đẩy nội dung gì ra Telegram trong lượt này chưa
}

// markSent/sentAnything: turnSent bị chạm từ readLoop, từ goroutine xin quyền
// và từ sendPrompt, nên luôn đi qua lock.
func (s *claudeSession) markSent() {
	s.mu.Lock()
	s.turnSent = true
	s.mu.Unlock()
}

func (s *claudeSession) sentAnything() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnSent
}

// startClaude mở 1 tiến trình claude mới cho chat. resumeID != "" -> mở lại
// phiên đã lưu đó (--resume) thay vì bắt đầu hội thoại mới.
func (b *Bot) startClaude(chatID int64, cwd, resumeID, mode string) (*claudeSession, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", flagPermMode(mode),
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	args = append(args, b.cfg.ClaudeArgs...)

	cmd := exec.Command(b.cfg.ClaudeBin, args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuf{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &claudeSession{
		b: b, chatID: chatID, cwd: cwd, cmd: cmd, stdin: stdin, stderr: tail,
		pending:  map[string]*pendingAsk{},
		ctl:      map[string]chan ctlResult{},
		exited:   make(chan struct{}),
		permMode: normPermMode(mode),
	}
	s.live = liveMsg{b: b, chatID: chatID}

	go func() {
		werr := cmd.Wait()
		s.mu.Lock()
		s.waitErr = werr
		s.mu.Unlock()
		close(s.exited)
		s.endTurn()
	}()
	go s.readLoop(stdout)

	// Bắt tay: phải initialize trước khi gửi prompt đầu tiên. Trả lời có
	// current_permission_mode (chế độ đang thực sự hiệu lực) nhưng KHÔNG có
	// session_id — id chỉ về ở system/init của lượt đầu tiên.
	payload, err := s.sendControl("initialize", nil, initTimeout)
	if err != nil {
		s.kill()
		return nil, err
	}
	var initInfo struct {
		Mode string `json:"current_permission_mode"`
	}
	json.Unmarshal(payload, &initInfo)
	s.mu.Lock()
	if initInfo.Mode != "" {
		s.permMode = normPermMode(initInfo.Mode)
	}
	if resumeID != "" {
		s.sessID = resumeID // system/init sẽ xác nhận lại ở lượt đầu
	}
	s.mu.Unlock()
	if resumeID != "" {
		log.Printf("claude: mở lại phiên %s cho chat %d (cwd=%s)", resumeID, chatID, cwd)
	} else {
		log.Printf("claude: phiên mới cho chat %d (cwd=%s)", chatID, cwd)
	}
	return s, nil
}

// claudeFor trả về phiên Claude của chat, khởi động nếu chưa có / đã chết.
func (b *Bot) claudeFor(chatID int64, sess *Session) (*claudeSession, error) {
	sess.mu.Lock()
	cs, cwd, mode := sess.claude, sess.cwd, sess.permMode
	sess.mu.Unlock()
	if cs != nil && cs.alive() {
		return cs, nil
	}
	if mode == "" {
		mode = b.cfg.claudePermissionMode()
	}
	cs, err := b.startClaude(chatID, cwd, "", mode)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	sess.claude = cs
	sess.mu.Unlock()
	return cs, nil
}

// stopClaude kết thúc phiên Claude của chat (nếu có). Trả về true nếu có phiên.
func (b *Bot) stopClaude(sess *Session) bool {
	sess.mu.Lock()
	cs := sess.claude
	sess.claude = nil
	sess.mu.Unlock()
	if cs == nil {
		return false
	}
	cs.kill()
	return true
}

func (s *claudeSession) alive() bool {
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

func (s *claudeSession) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *claudeSession) kill() {
	if s.cmd.Process != nil {
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	s.stdin.Close()
}

func (s *claudeSession) stderrTail() string {
	t := strings.TrimSpace(s.stderr.String())
	if t == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.waitErr != nil {
			return s.waitErr.Error()
		}
		return "không có stderr"
	}
	return t
}

func (s *claudeSession) endTurn() {
	s.mu.Lock()
	t := s.turn
	s.turn = nil
	s.mu.Unlock()
	if t != nil {
		close(t)
	}
}

// ctlResult: trả lời cho 1 control_request do bot gửi đi.
type ctlResult struct {
	subtype string // "success" | "error"
	errText string
	payload json.RawMessage
}

// sendControl gửi 1 control_request và chờ claude trả lời.
func (s *claudeSession) sendControl(subtype string, extra map[string]any, timeout time.Duration) (json.RawMessage, error) {
	req := map[string]any{"subtype": subtype}
	for k, v := range extra {
		req[k] = v
	}
	reqID := fmt.Sprintf("%s-%d", subtype, time.Now().UnixNano())
	ch := make(chan ctlResult, 1)
	s.mu.Lock()
	s.ctl[reqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.ctl, reqID)
		s.mu.Unlock()
	}()

	if err := s.writeJSON(map[string]any{"type": "control_request", "request_id": reqID, "request": req}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.subtype == "error" {
			if r.errText == "" {
				r.errText = subtype + " thất bại"
			}
			return nil, fmt.Errorf("%s", r.errText)
		}
		return r.payload, nil
	case <-s.exited:
		return nil, fmt.Errorf("claude đã thoát: %s", s.stderrTail())
	case <-time.After(timeout):
		return nil, fmt.Errorf("claude không phản hồi %s", subtype)
	}
}

// setPermMode đổi chế độ quyền của phiên đang chạy.
func (s *claudeSession) setPermMode(mode string) error {
	payload, err := s.sendControl("set_permission_mode",
		map[string]any{"mode": ctlPermMode(mode)}, ctlTimeout)
	if err != nil {
		return err
	}
	got := mode
	var echo struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(payload, &echo) == nil && echo.Mode != "" {
		got = normPermMode(echo.Mode) // headless CLI echo lại chế độ đang hiệu lực
	}
	s.mu.Lock()
	s.permMode = got
	s.mu.Unlock()
	return nil
}

// describe: mô tả ngắn phiên (id · model · quyền) cho /status. Phiên mới chưa
// chạy lượt nào thì chưa có id — claude chỉ cấp id ở system/init.
func (s *claudeSession) describe() string {
	s.mu.Lock()
	sid, model, mode := s.sessID, s.model, s.permMode
	s.mu.Unlock()
	if sid == "" {
		sid = "mới (chưa có id)"
	} else {
		sid = shortID(sid)
	}
	if model != "" {
		sid += " · " + model
	}
	return sid + " · quyền " + mode
}

// curPermMode: chế độ quyền phiên đang chạy.
func (s *claudeSession) curPermMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permMode
}

// sendPrompt gửi 1 prompt và chờ hết lượt (event "result"). Hủy ctx -> gửi
// interrupt cho claude, hết kiên nhẫn thì kill.
func (s *claudeSession) sendPrompt(ctx context.Context, p prompt) error {
	turn := make(chan struct{})
	s.mu.Lock()
	if s.turn != nil {
		s.mu.Unlock()
		return fmt.Errorf("phiên claude đang chạy lượt khác")
	}
	s.turn = turn
	s.turnSent = false
	s.mu.Unlock()

	msg := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": p.claudeContent()},
	}
	if err := s.writeJSON(msg); err != nil {
		s.endTurn()
		return fmt.Errorf("gửi prompt: %w", err)
	}

	select {
	case <-turn:
		if !s.alive() {
			return fmt.Errorf("claude đã thoát: %s", s.stderrTail())
		}
		return nil
	case <-ctx.Done():
		s.writeJSON(map[string]any{
			"type":       "control_request",
			"request_id": fmt.Sprintf("int-%d", time.Now().UnixNano()),
			"request":    map[string]any{"subtype": "interrupt", "cancel_queued": true},
		})
		s.denyAllPending("đã hủy")
		select {
		case <-turn:
		case <-s.exited:
		case <-time.After(interruptWait):
			s.kill()
		}
		return ctx.Err()
	}
}

// -------------------------- Đọc & render event ------------------------

type ccEnvelope struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Message   json.RawMessage `json:"message"`
	Event     json.RawMessage `json:"event"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	PermMode  string          `json:"permissionMode"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	Cost      float64         `json:"total_cost_usd"`
	Duration  int64           `json:"duration_ms"`
}

type ccBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (s *claudeSession) readLoop(r io.ReadCloser) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readJSONLine(br)
		if len(line) > 0 {
			s.dispatch(line)
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("claude stdout (chat %d): %v", s.chatID, err)
			}
			s.live.close()
			s.denyAllPending("claude đã thoát")
			return
		}
	}
}

// readJSONLine đọc 1 dòng dài bao nhiêu cũng được (event có thể rất lớn).
func readJSONLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

func (s *claudeSession) dispatch(line []byte) {
	var e ccEnvelope
	if err := json.Unmarshal(line, &e); err != nil {
		log.Printf("claude: dòng không phải JSON: %s", truncStr(string(line), 200))
		return
	}
	switch e.Type {
	case "system":
		s.onSystem(e)
	case "stream_event":
		s.onStreamEvent(e)
	case "assistant":
		s.onAssistant(e)
	case "user":
		s.onToolResult(e)
	case "result":
		s.onResult(e)
	case "control_request":
		s.onControlRequest(e)
	case "control_response":
		s.onControlResponse(e)
	case "control_cancel_request":
		s.decide(e.RequestID, permDecision{canceled: true})
	}
}

func (s *claudeSession) onSystem(e ccEnvelope) {
	switch e.Subtype {
	case "init":
		s.mu.Lock()
		s.sessID, s.model = e.SessionID, e.Model
		if e.PermMode != "" {
			s.permMode = normPermMode(e.PermMode)
		}
		s.mu.Unlock()
	case "permission_denied", "error":
		if m := rawString(e.Message); m != "" {
			s.live.close()
			s.markSent()
			s.b.send(s.chatID, "⛔ "+truncStr(m, 500), false)
		}
	}
}

func (s *claudeSession) onStreamEvent(e ccEnvelope) {
	var ev struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(e.Event, &ev) != nil {
		return
	}
	if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" {
		s.markSent()
		s.live.append(ev.Delta.Text)
	}
}

// onAssistant chỉ lo các block tool_use (phần text đã stream qua stream_event).
func (s *claudeSession) onAssistant(e ccEnvelope) {
	var m struct {
		Content []ccBlock `json:"content"`
	}
	if json.Unmarshal(e.Message, &m) != nil {
		return
	}
	for _, blk := range m.Content {
		if blk.Type != "tool_use" {
			continue
		}
		s.live.close()
		s.markSent()
		text := toolEmoji(blk.Name) + " <b>" + htmlEscape(blk.Name) + "</b>"
		if d := toolDetail(blk.Name, blk.Input); d != "" {
			text += "\n<pre>" + htmlEscape(truncStr(d, toolDetailMax)) + "</pre>"
		}
		s.b.send(s.chatID, text, true)
	}
}

// onToolResult chỉ báo khi tool lỗi — thành công thì im lặng cho khỏi spam.
func (s *claudeSession) onToolResult(e ccEnvelope) {
	var m struct {
		Content []ccBlock `json:"content"`
	}
	if json.Unmarshal(e.Message, &m) != nil {
		return
	}
	for _, blk := range m.Content {
		if blk.Type != "tool_result" || !blk.IsError {
			continue
		}
		s.live.close()
		s.markSent()
		s.b.send(s.chatID, "⚠️ tool lỗi:\n<pre>"+htmlEscape(truncStr(contentText(blk.Content), 500))+"</pre>", true)
	}
}

func (s *claudeSession) onResult(e ccEnvelope) {
	s.live.close()
	if !s.sentAnything() && strings.TrimSpace(e.Result) != "" {
		s.b.sendChunked(s.chatID, e.Result, false)
		s.markSent()
	}
	var parts []string
	if e.IsError || (e.Subtype != "" && e.Subtype != "success") {
		parts = append(parts, "⚠️ "+e.Subtype)
	} else if !s.sentAnything() {
		parts = append(parts, "✓ (Claude không trả về nội dung)")
	}
	if e.Duration > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(e.Duration)/1000))
	}
	if e.Cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", e.Cost))
	}
	if len(parts) > 0 {
		s.b.send(s.chatID, "— "+strings.Join(parts, " · "), false)
	}
	s.endTurn()
}

func (s *claudeSession) onControlResponse(e ccEnvelope) {
	var r struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error"`
		Response  json.RawMessage `json:"response"`
	}
	if json.Unmarshal(e.Response, &r) != nil {
		return
	}
	s.mu.Lock()
	ch := s.ctl[r.RequestID]
	s.mu.Unlock()
	if ch == nil {
		return // interrupt gửi kiểu bắn-và-quên, không cần trả lời
	}
	select {
	case ch <- ctlResult{subtype: r.Subtype, errText: r.Error, payload: r.Response}:
	default:
	}
}

func (s *claudeSession) onControlRequest(e ccEnvelope) {
	var req struct {
		Subtype string `json:"subtype"`
	}
	json.Unmarshal(e.Request, &req)
	if req.Subtype == "can_use_tool" {
		s.live.close()
		go s.handleAsk(e.RequestID, e.Request)
		return
	}
	// Chưa hỗ trợ (hook_callback, mcp_message…) -> trả lỗi để claude không treo.
	s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": e.RequestID,
			"error":      "telegram-terminal không hỗ trợ " + req.Subtype,
		},
	})
}

// handleAsk đẩy yêu cầu quyền ra Telegram (kèm nút) và chờ người dùng bấm.
func (s *claudeSession) handleAsk(reqID string, raw json.RawMessage) {
	var req struct {
		ToolName    string          `json:"tool_name"`
		DisplayName string          `json:"display_name"`
		Description string          `json:"description"`
		Input       json.RawMessage `json:"input"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
	}
	json.Unmarshal(raw, &req)

	ask := &pendingAsk{ch: make(chan permDecision, 1)}
	s.mu.Lock()
	s.pending[reqID] = ask
	s.mu.Unlock()

	name := req.ToolName
	if name == "" {
		name = req.DisplayName
	}
	timeout := time.Duration(s.b.cfg.claudeAskTimeout()) * time.Second
	text := "🔐 Claude xin phép dùng " + toolEmoji(name) + " <b>" + htmlEscape(name) + "</b>"
	if req.Description != "" {
		text += "\n" + htmlEscape(truncStr(req.Description, 200))
	}
	if d := toolDetail(req.ToolName, req.Input); d != "" {
		text += "\n<pre>" + htmlEscape(truncStr(d, toolDetailMax)) + "</pre>"
	}
	text += fmt.Sprintf("\n<i>tự động từ chối sau %.0f phút</i>", timeout.Minutes())

	hasSugg := len(bytes.TrimSpace(req.Suggestions)) > 2 // không phải null/[]
	rows := [][]ikButton{{
		{Text: "✅ Cho phép", Data: "ca|a|" + reqID},
		{Text: "❌ Từ chối", Data: "ca|d|" + reqID},
	}}
	if hasSugg {
		rows = append(rows, []ikButton{{Text: "⏩ Cho phép luôn (phiên này)", Data: "ca|A|" + reqID}})
	}
	msgID := s.b.sendText(s.chatID, text, true, inlineKeyboard(rows...))
	s.markSent()

	var dec permDecision
	select {
	case dec = <-ask.ch:
	case <-time.After(timeout):
		dec = permDecision{behavior: "deny", note: "hết thời gian chờ"}
	case <-s.exited:
		s.clearAsk(reqID)
		return
	}
	s.clearAsk(reqID)

	if dec.canceled {
		if msgID != 0 {
			s.b.editText(s.chatID, msgID, text+"\n\n🔕 Yêu cầu đã được rút lại.", true, emptyKeyboard())
		}
		return
	}

	resp := map[string]any{"behavior": dec.behavior}
	if dec.behavior == "deny" {
		m := "Người dùng từ chối qua Telegram"
		if dec.note != "" {
			m += " (" + dec.note + ")"
		}
		resp["message"] = m
	}
	if dec.behavior == "allow" && dec.always && hasSugg {
		resp["updatedPermissions"] = req.Suggestions
	}
	if err := s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   resp,
		},
	}); err != nil {
		log.Printf("claude: trả lời quyền lỗi: %v", err)
	}
	if msgID != 0 {
		s.b.editText(s.chatID, msgID, text+"\n\n"+verdictLine(dec), true, emptyKeyboard())
	}
}

func (s *claudeSession) clearAsk(reqID string) {
	s.mu.Lock()
	delete(s.pending, reqID)
	s.mu.Unlock()
}

// decide chuyển quyết định của người dùng cho goroutine đang chờ.
func (s *claudeSession) decide(reqID string, dec permDecision) bool {
	s.mu.Lock()
	ask := s.pending[reqID]
	s.mu.Unlock()
	if ask == nil {
		return false
	}
	select {
	case ask.ch <- dec:
		return true
	default:
		return false
	}
}

func (s *claudeSession) denyAllPending(note string) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.decide(id, permDecision{behavior: "deny", note: note})
	}
}

func verdictLine(d permDecision) string {
	switch {
	case d.behavior == "allow" && d.always:
		return "⏩ Đã cho phép (ghi nhớ cho phiên này)."
	case d.behavior == "allow":
		return "✅ Đã cho phép."
	case d.note != "":
		return "❌ Đã từ chối — " + d.note + "."
	default:
		return "❌ Đã từ chối."
	}
}

// ------------------------------ Runner --------------------------------

func (b *Bot) runClaude(ctx context.Context, chatID int64, sess *Session, p prompt) {
	cs, err := b.claudeFor(chatID, sess)
	if err != nil {
		b.send(chatID, "⚠️ claude: "+err.Error()+"\n(kiểm tra: đã cài `claude` và đăng nhập cho user này chưa?)", false)
		return
	}
	err = cs.sendPrompt(ctx, p)
	switch {
	case err == nil:
	case ctx.Err() == context.DeadlineExceeded:
		b.send(chatID, "⏱️ Claude hết thời gian, đã hủy.", false)
	case ctx.Err() == context.Canceled:
		b.send(chatID, "🛑 Đã hủy.", false)
	default:
		b.send(chatID, "⚠️ claude: "+err.Error(), false)
		b.stopClaude(sess) // dọn phiên hỏng, prompt sau sẽ khởi động lại
	}
}

// ------------------------------ Helpers -------------------------------

// tailBuf giữ tối đa 4KB cuối của stderr để báo lỗi khi claude chết.
type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func truncStr(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	head, _ := splitRunes(s, max)
	return strings.TrimRight(head, " \t\n") + " …"
}

// rawString đọc 1 field JSON có thể là string (hoặc object) thành text.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// contentText lấy text từ content của tool_result (string hoặc mảng block).
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []ccBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var out []string
		for _, blk := range blocks {
			if blk.Text != "" {
				out = append(out, blk.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return string(raw)
}

func toolEmoji(name string) string {
	switch name {
	case "Bash", "BashOutput", "KillShell":
		return "💻"
	case "Read", "NotebookRead":
		return "📖"
	case "Write":
		return "✍️"
	case "Edit", "NotebookEdit":
		return "✏️"
	case "Grep", "Glob":
		return "🔍"
	case "WebFetch", "WebSearch":
		return "🌐"
	case "Task":
		return "🤖"
	case "TodoWrite":
		return "📝"
	default:
		return "🔧"
	}
}

// toolDetail rút phần đáng đọc nhất trong input của tool.
func toolDetail(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return string(input)
	}
	get := func(k string) string {
		v, ok := m[k]
		if !ok || v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if strings.TrimSpace(p) != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, "\n")
	}
	switch name {
	case "Bash":
		return join("$ "+get("command"), get("description"))
	case "BashOutput", "KillShell":
		return get("bash_id") + get("shell_id")
	case "Read", "NotebookRead":
		return join(get("file_path")+get("notebook_path"), get("offset"), get("limit"))
	case "Write":
		return join(get("file_path"), "---", truncStr(get("content"), 400))
	case "Edit":
		return join(get("file_path"),
			"- "+truncStr(get("old_string"), 200),
			"+ "+truncStr(get("new_string"), 200))
	case "NotebookEdit":
		return join(get("notebook_path"), truncStr(get("new_source"), 300))
	case "Grep":
		return join(get("pattern"), get("path"), get("glob"))
	case "Glob":
		return join(get("pattern"), get("path"))
	case "WebFetch":
		return join(get("url"), truncStr(get("prompt"), 200))
	case "WebSearch":
		return get("query")
	case "Task":
		return join(get("subagent_type"), get("description"))
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// --------------------------- Chế độ quyền -----------------------------
// Lưu ý: cờ --permission-mode gọi chế độ mặc định là "manual", còn
// control_request set_permission_mode gọi chính nó là "default".

var permModes = []struct{ Name, Desc string }{
	{"manual", "hỏi mọi thứ cần quyền (mặc định)"},
	{"acceptEdits", "tự cho sửa file, tool khác vẫn hỏi"},
	{"auto", "để model tự phán cho phép/từ chối"},
	{"dontAsk", "không hỏi; cái chưa được cho phép trước thì từ chối"},
	{"plan", "chỉ lập kế hoạch, không thao tác"},
	{"bypassPermissions", "bỏ qua mọi kiểm tra (cần claude_args [\"--dangerously-skip-permissions\"])"},
}

func validPermMode(m string) bool {
	for _, p := range permModes {
		if p.Name == m {
			return true
		}
	}
	return m == "default"
}

func permModeDesc(m string) string {
	for _, p := range permModes {
		if p.Name == normPermMode(m) {
			return p.Desc
		}
	}
	return ""
}

// normPermMode: tên dùng để hiển thị và lưu trong bot.
func normPermMode(m string) string {
	if m == "default" || m == "" {
		return "manual"
	}
	return m
}

// flagPermMode: tên cho cờ --permission-mode.
func flagPermMode(m string) string {
	if m == "default" || m == "" {
		return "manual"
	}
	return m
}

// ctlPermMode: tên cho control_request set_permission_mode.
func ctlPermMode(m string) string {
	if m == "manual" || m == "" {
		return "default"
	}
	return m
}

// permModeFor: chế độ quyền dùng cho phiên tiếp theo của chat.
func (b *Bot) permModeFor(sess *Session) string {
	sess.mu.Lock()
	m := sess.permMode
	sess.mu.Unlock()
	if m == "" {
		return b.cfg.claudePermissionMode()
	}
	return m
}

// permStatus dựng nội dung + nút cho lệnh /perm.
func (b *Bot) permStatus(sess *Session) (string, []byte) {
	sess.mu.Lock()
	cs, def := sess.claude, sess.permMode
	sess.mu.Unlock()
	if def == "" {
		def = b.cfg.claudePermissionMode()
	}
	cur, live := normPermMode(def), false
	if cs != nil && cs.alive() {
		if m := cs.curPermMode(); m != "" {
			cur, live = m, true
		}
	}

	var sb strings.Builder
	sb.WriteString("🔧 Chế độ quyền: <b>" + htmlEscape(cur) + "</b>")
	if live {
		sb.WriteString(" (đang áp cho phiên đang mở)")
	} else {
		sb.WriteString(" (sẽ áp cho phiên mở tiếp theo)")
	}
	sb.WriteString("\n\n")
	for _, p := range permModes {
		mark := "•"
		if p.Name == cur {
			mark = "✅"
		}
		sb.WriteString(mark + " <b>" + p.Name + "</b> — " + htmlEscape(p.Desc) + "\n")
	}
	sb.WriteString("\nBấm nút hoặc gõ <code>/perm &lt;tên&gt;</code>.")

	var rows [][]ikButton
	for i := 0; i < len(permModes); i += 2 {
		var row []ikButton
		for _, p := range permModes[i:min(i+2, len(permModes))] {
			label := p.Name
			if p.Name == cur {
				label = "✅ " + label
			}
			row = append(row, ikButton{Text: label, Data: "pm|" + p.Name})
		}
		rows = append(rows, row)
	}
	return sb.String(), inlineKeyboard(rows...)
}

// setPermMode đổi chế độ quyền: áp ngay cho phiên đang mở (nếu có) rồi ghi làm
// mặc định cho các phiên sau của chat này.
func (b *Bot) setPermMode(chatID int64, sess *Session, mode string) {
	mode = strings.TrimSpace(mode)
	if !validPermMode(mode) {
		var names []string
		for _, p := range permModes {
			names = append(names, p.Name)
		}
		b.send(chatID, "⚠️ chế độ không hợp lệ: "+htmlEscape(mode)+
			"\nChọn một trong: <code>"+strings.Join(names, ", ")+"</code>", true)
		return
	}
	mode = normPermMode(mode)

	sess.mu.Lock()
	cs := sess.claude
	sess.mu.Unlock()

	if cs != nil && cs.alive() {
		if err := cs.setPermMode(mode); err != nil {
			msg := "⚠️ không đổi được chế độ: " + htmlEscape(err.Error())
			if mode == "bypassPermissions" {
				msg += "\n\nMuốn dùng chế độ này thì đặt <code>\"claude_args\": [\"--dangerously-skip-permissions\"]</code> trong config rồi /newchat."
			}
			b.send(chatID, msg, true)
			return
		}
	}
	sess.mu.Lock()
	sess.permMode = mode
	sess.mu.Unlock()

	suffix := " (áp dụng cho phiên Claude tiếp theo)"
	if cs != nil && cs.alive() {
		suffix = ""
	}
	b.send(chatID, "🔧 Chế độ quyền giờ là <b>"+htmlEscape(mode)+"</b> — "+
		htmlEscape(permModeDesc(mode))+"."+suffix, true)
}
