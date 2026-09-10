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

	// answers: đáp án cho tool AskUserQuestion (map câu hỏi -> nhãn đã chọn).
	// Chỉ có ở đường đi của askq.go.
	answers map[string]string
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
	asks     map[string]*questionAsk   // bộ câu hỏi AskUserQuestion đang chờ
	askSeq   int                       // đánh số bộ câu hỏi cho callback_data
	ctl      map[string]chan ctlResult // control_request của bot đang chờ trả lời
	turn     chan struct{}             // đóng khi lượt hiện tại kết thúc
	sessID   string
	model    string
	lastCost float64 // total_cost_usd của result trước (nó là tổng cộng dồn)
	permMode string  // chế độ quyền phiên này đang chạy
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
		asks:     map[string]*questionAsk{},
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
		log.Printf("claude: resumed session %s for chat %d (cwd=%s)", resumeID, chatID, cwd)
	} else {
		log.Printf("claude: new session for chat %d (cwd=%s)", chatID, cwd)
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
		return s.b.t(s.chatID, "err.no.stderr")
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
				r.errText = s.b.t(s.chatID, "err.ctl.failed", subtype)
			}
			return nil, fmt.Errorf("%s", r.errText)
		}
		return r.payload, nil
	case <-s.exited:
		return nil, fmt.Errorf("%s", s.b.t(s.chatID, "err.exited", s.stderrTail()))
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s", s.b.t(s.chatID, "err.no.reply", subtype))
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
func (s *claudeSession) describe(l *language) string {
	s.mu.Lock()
	sid, model, mode := s.sessID, s.model, s.permMode
	s.mu.Unlock()
	if sid == "" {
		sid = l.t("session.no.id")
	} else {
		sid = shortID(sid)
	}
	if model != "" {
		sid += " · " + model
	}
	return sid + " · " + l.t("session.permission", mode)
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
		return fmt.Errorf("%s", s.b.t(s.chatID, "err.busy.turn"))
	}
	s.turn = turn
	s.turnSent = false
	s.mu.Unlock()

	msg := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": p.claudeContent(s.b.lang(s.chatID))},
	}
	if err := s.writeJSON(msg); err != nil {
		s.endTurn()
		return fmt.Errorf("gửi prompt: %w", err)
	}

	select {
	case <-turn:
		if !s.alive() {
			return fmt.Errorf("%s", s.b.t(s.chatID, "err.exited", s.stderrTail()))
		}
		return nil
	case <-ctx.Done():
		s.writeJSON(map[string]any{
			"type":       "control_request",
			"request_id": fmt.Sprintf("int-%d", time.Now().UnixNano()),
			"request":    map[string]any{"subtype": "interrupt", "cancel_queued": true},
		})
		s.denyAllPending(s.b.t(s.chatID, "note.cancelled"))
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
	NumTurns  int             `json:"num_turns"`
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
			s.denyAllPending(s.b.t(s.chatID, "note.exited"))
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
		log.Printf("claude: stdout line is not JSON: %s", truncStr(string(line), 200))
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
		s.b.send(s.chatID, s.b.t(s.chatID, "claude.tool.error")+"\n<pre>"+htmlEscape(truncStr(contentText(blk.Content), 500))+"</pre>", true)
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
		parts = append(parts, s.b.t(s.chatID, "claude.no.content"))
	}
	if e.Duration > 0 {
		parts = append(parts, humanDur(e.Duration))
	}
	if e.Cost > 0 {
		turn, total := s.turnCost(e.Cost)
		parts = append(parts, fmt.Sprintf("$%.4f", turn))
		// Chỉ nêu tổng phiên khi nó đã khác chi phí lượt này.
		if total-turn > 0.00005 {
			parts = append(parts, s.b.t(s.chatID, "result.session.cost", fmt.Sprintf("%.4f", total)))
		}
	}
	if e.NumTurns > 1 {
		parts = append(parts, s.b.t(s.chatID, "result.steps", e.NumTurns))
	}
	if len(parts) > 0 {
		s.b.send(s.chatID, "— "+strings.Join(parts, " · "), false)
	}
	s.endTurn()
}

// turnCost tách chi phí của riêng lượt vừa xong ra khỏi total_cost_usd.
//
// Trong chế độ stream-json input, mỗi result mang TỔNG CỘNG DỒN của cả phiên
// ("each result carries the running total so far"), nên phải trừ lần trước.
// Phiên resume hoặc /clear giữa phiên làm tổng nhỏ lại — lúc đó tổng mới chính
// là chi phí của lượt này.
func (s *claudeSession) turnCost(total float64) (turn, running float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn = total - s.lastCost
	if turn < 0 {
		turn = total
	}
	s.lastCost = total
	return turn, total
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
			"error":      s.b.t(s.chatID, "err.unsupported", req.Subtype),
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

	// AskUserQuestion không phải xin quyền mà là Claude hỏi người dùng: trả lời
	// bằng nút chọn đáp án chứ không phải Cho phép / Từ chối.
	if req.ToolName == askQuestionTool {
		if qs, input, ok := parseAskQuestions(req.Input); ok {
			s.askQuestions(reqID, ask, qs, input)
			return
		}
	}

	name := req.ToolName
	if name == "" {
		name = req.DisplayName
	}
	timeout := time.Duration(s.b.cfg.claudeAskTimeout()) * time.Second
	text := s.b.t(s.chatID, "ask.head", toolEmoji(name), htmlEscape(name))
	if req.Description != "" {
		text += "\n" + htmlEscape(truncStr(req.Description, 200))
	}
	if d := toolDetail(req.ToolName, req.Input); d != "" {
		text += "\n<pre>" + htmlEscape(truncStr(d, toolDetailMax)) + "</pre>"
	}
	text += s.b.t(s.chatID, "ask.timeout.note", timeout.Minutes())

	hasSugg := len(bytes.TrimSpace(req.Suggestions)) > 2 // không phải null/[]
	rows := [][]ikButton{{
		{Text: s.b.t(s.chatID, "btn.allow"), Data: "ca|a|" + reqID},
		{Text: s.b.t(s.chatID, "btn.deny"), Data: "ca|d|" + reqID},
	}}
	if hasSugg {
		rows = append(rows, []ikButton{{Text: s.b.t(s.chatID, "btn.always"), Data: "ca|A|" + reqID}})
	}
	msgID := s.b.sendText(s.chatID, text, true, inlineKeyboard(rows...))
	s.markSent()

	var dec permDecision
	select {
	case dec = <-ask.ch:
	case <-time.After(timeout):
		dec = permDecision{behavior: "deny", note: s.b.t(s.chatID, "note.timeout")}
	case <-s.exited:
		s.clearAsk(reqID)
		return
	}
	s.clearAsk(reqID)

	if dec.canceled {
		if msgID != 0 {
			s.b.editText(s.chatID, msgID, text+s.b.t(s.chatID, "ask.withdrawn"), true, emptyKeyboard())
		}
		return
	}

	resp := map[string]any{"behavior": dec.behavior}
	if dec.behavior == "deny" {
		m := s.b.t(s.chatID, "deny.reason")
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
		log.Printf("claude: answering the permission request failed: %v", err)
	}
	if msgID != 0 {
		s.b.editText(s.chatID, msgID, text+"\n\n"+verdictLine(s.b.lang(s.chatID), dec), true, emptyKeyboard())
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

func verdictLine(l *language, d permDecision) string {
	switch {
	case d.behavior == "allow" && d.always:
		return l.t("verdict.always")
	case d.behavior == "allow":
		return l.t("verdict.allow")
	case d.note != "":
		return l.t("verdict.deny.why", d.note)
	default:
		return l.t("verdict.deny")
	}
}

// ------------------------------ Runner --------------------------------

func (b *Bot) runClaude(ctx context.Context, chatID int64, sess *Session, p prompt) {
	cs, err := b.claudeFor(chatID, sess)
	if err != nil {
		b.send(chatID, b.t(chatID, "claude.start.error", err.Error()), false)
		return
	}
	err = cs.sendPrompt(ctx, p)
	switch {
	case err == nil:
	case ctx.Err() == context.DeadlineExceeded:
		b.send(chatID, b.t(chatID, "claude.timeout"), false)
	case ctx.Err() == context.Canceled:
		b.send(chatID, b.t(chatID, "claude.cancelled"), false)
	default:
		b.send(chatID, b.t(chatID, "claude.error", err.Error()), false)
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

// humanDur: 12.3s · 3m07s · 1h04m — lượt chờ người bấm nút có thể dài hàng chục
// phút nên in bằng giây thì khó đọc.
func humanDur(ms int64) string {
	sec := int(ms / 1000)
	switch {
	case sec < 60:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
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
	case askQuestionTool:
		return "❓"
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
	case askQuestionTool:
		// Nội dung đầy đủ nằm ở các tin nhắn kèm nút, đây chỉ nhắc lại câu hỏi.
		if qs, _, ok := parseAskQuestions(input); ok {
			var lines []string
			for _, q := range qs {
				lines = append(lines, q.Question)
			}
			return join(lines...)
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// --------------------------- Chế độ quyền -----------------------------
// Lưu ý: cờ --permission-mode gọi chế độ mặc định là "manual", còn
// control_request set_permission_mode gọi chính nó là "default".

// permModes: tên chế độ; mô tả nằm trong catalog theo key "perm.desc.<tên>".
var permModes = []string{"manual", "acceptEdits", "auto", "dontAsk", "plan", "bypassPermissions"}

func validPermMode(m string) bool {
	for _, name := range permModes {
		if name == m {
			return true
		}
	}
	return m == "default"
}

func permModeDesc(l *language, m string) string {
	return l.t("perm.desc." + normPermMode(m))
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
func (b *Bot) permStatus(chatID int64, sess *Session) (string, []byte) {
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

	l := b.lang(chatID)
	var sb strings.Builder
	sb.WriteString(l.t("perm.head", htmlEscape(cur)))
	if live {
		sb.WriteString(l.t("perm.live"))
	} else {
		sb.WriteString(l.t("perm.next"))
	}
	sb.WriteString("\n\n")
	for _, name := range permModes {
		mark := "•"
		if name == cur {
			mark = "✅"
		}
		sb.WriteString(mark + " <b>" + name + "</b> — " + htmlEscape(permModeDesc(l, name)) + "\n")
	}
	sb.WriteString(l.t("perm.hint"))

	var rows [][]ikButton
	for i := 0; i < len(permModes); i += 2 {
		var row []ikButton
		for _, name := range permModes[i:min(i+2, len(permModes))] {
			label := name
			if name == cur {
				label = "✅ " + label
			}
			row = append(row, ikButton{Text: label, Data: "pm|" + name})
		}
		rows = append(rows, row)
	}
	return sb.String(), inlineKeyboard(rows...)
}

// setPermMode đổi chế độ quyền: áp ngay cho phiên đang mở (nếu có) rồi ghi làm
// mặc định cho các phiên sau của chat này.
func (b *Bot) setPermMode(chatID int64, sess *Session, mode string) {
	l := b.lang(chatID)
	mode = strings.TrimSpace(mode)
	if !validPermMode(mode) {
		b.send(chatID, l.t("perm.invalid", htmlEscape(mode), strings.Join(permModes, ", ")), true)
		return
	}
	mode = normPermMode(mode)

	sess.mu.Lock()
	cs := sess.claude
	sess.mu.Unlock()

	if cs != nil && cs.alive() {
		if err := cs.setPermMode(mode); err != nil {
			msg := l.t("perm.failed", htmlEscape(err.Error()))
			if mode == "bypassPermissions" {
				msg += l.t("perm.bypass.hint")
			}
			b.send(chatID, msg, true)
			return
		}
	}
	sess.mu.Lock()
	sess.permMode = mode
	sess.mu.Unlock()

	suffix := l.t("perm.changed.next")
	if cs != nil && cs.alive() {
		suffix = ""
	}
	b.send(chatID, l.t("perm.changed", htmlEscape(mode), htmlEscape(permModeDesc(l, mode)))+suffix, true)
}
