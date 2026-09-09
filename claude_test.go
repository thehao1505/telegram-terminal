package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildFakeClaude build testdata/fakeclaude thành 1 binary tạm.
func buildFakeClaude(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeclaude")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakeclaude")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude: %v\n%s", err, out)
	}
	return bin
}

// tgMock giả lập Bot API: ghi lại sendMessage/editMessageText và báo ra askCh
// khi có tin nhắn kèm inline keyboard (tức là yêu cầu xin quyền).
type tgMock struct {
	srv     *httptest.Server
	mu      sync.Mutex
	sent    []string
	edits   []string
	answers []string
	askCh   chan string // callback_data của nút đầu tiên

	fileData []byte   // nội dung mọi file mà getFile/download trả về
	fileIDs  []string // các file_id mà bot đã hỏi getFile
}

func newTGMock() *tgMock {
	m := &tgMock{askCh: make(chan string, 4)}
	var nextID int64 = 100
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tải file đính kèm: .../file/<file_path>
		if strings.HasPrefix(r.URL.Path, "/file/") {
			m.mu.Lock()
			data := m.fileData
			m.mu.Unlock()
			w.Write(data)
			return
		}
		r.ParseForm()
		if path.Base(r.URL.Path) == "getFile" {
			id := r.FormValue("file_id")
			m.mu.Lock()
			m.fileIDs = append(m.fileIDs, id)
			m.mu.Unlock()
			fmt.Fprintf(w, `{"ok":true,"result":{"file_path":"photos/%s.bin"}}`, id)
			return
		}
		text := r.FormValue("text")
		m.mu.Lock()
		switch path.Base(r.URL.Path) {
		case "sendMessage":
			nextID++
			m.sent = append(m.sent, text)
			if mk := r.FormValue("reply_markup"); mk != "" {
				var kb struct {
					Rows [][]struct {
						Data string `json:"callback_data"`
					} `json:"inline_keyboard"`
				}
				if json.Unmarshal([]byte(mk), &kb) == nil && len(kb.Rows) > 0 && len(kb.Rows[0]) > 0 {
					m.askCh <- kb.Rows[0][0].Data
				}
			}
			m.mu.Unlock()
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, nextID)
			return
		case "editMessageText":
			m.edits = append(m.edits, text)
		case "answerCallbackQuery":
			m.answers = append(m.answers, text)
		}
		m.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	return m
}

func (m *tgMock) all() (sent, edits, answers []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...), append([]string(nil), m.edits...), append([]string(nil), m.answers...)
}

func (m *tgMock) bot(cfg Config) *Bot {
	return &Bot{
		cfg:        cfg,
		apiBase:    m.srv.URL + "/",
		fileBase:   m.srv.URL + "/file/",
		pollClient: m.srv.Client(),
		sendClient: m.srv.Client(),
		sessions:   map[int64]*Session{},
		langs:      map[int64]string{},
		allowed:    map[int64]bool{7: true},
		albums:     map[string]*albumBuf{},
	}
}

// jsonUnmarshalString: tiện cho test dựng struct Telegram từ JSON thật.
func jsonUnmarshalString(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

// msgUpdate dựng 1 Update tin nhắn từ user 7 trong chat 1 (đi qua đúng đường
// giải mã JSON của Telegram).
func msgUpdate(t *testing.T, text string) Update {
	t.Helper()
	var u Update
	raw := fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"from":{"id":7},"chat":{"id":1},"text":%q}}`, text)
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func containsSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestClaudeAllowAlways: stream chữ -> báo tool -> xin quyền -> người dùng bấm
// "Cho phép luôn" -> claude nhận allow + updatedPermissions -> lượt kết thúc.
func TestClaudeAllowAlways(t *testing.T) {
	bin := buildFakeClaude(t)
	decFile := filepath.Join(t.TempDir(), "decision.json")
	t.Setenv("TT_FAKE_DECISION", decFile)

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: bin, StartDir: t.TempDir()})
	sess := b.session(1)

	done := make(chan struct{})
	go func() {
		b.runClaude(context.Background(), 1, sess, textPrompt("chào bạn"))
		close(done)
	}()

	var data string
	select {
	case data = <-tg.askCh:
	case <-time.After(30 * time.Second):
		t.Fatal("không nhận được yêu cầu xin quyền")
	}
	if !strings.HasPrefix(data, "ca|a|") {
		t.Fatalf("callback_data lạ: %q", data)
	}

	// Bấm nút "Cho phép luôn" (biến thể A) — đi đúng đường callback thật.
	var q callbackQuery
	raw := fmt.Sprintf(`{"id":"cb1","from":{"id":7},"message":{"message_id":101,"chat":{"id":1}},"data":%q}`,
		strings.Replace(data, "ca|a|", "ca|A|", 1))
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	b.handleCallback(&q)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("lượt Claude không kết thúc")
	}

	sent, edits, answers := tg.all()
	if !containsSub(sent, "Xin") {
		t.Errorf("chưa gửi tin nhắn stream đầu tiên: %q", sent)
	}
	if !containsSub(edits, "Xin chào bạn") {
		t.Errorf("chưa stream đủ chữ qua editMessageText: %q", edits)
	}
	if !containsSub(sent, "ls -la") || !containsSub(sent, "Bash") {
		t.Errorf("chưa báo tool_use: %q", sent)
	}
	if !containsSub(sent, "asks to use") {
		t.Errorf("chưa gửi tin xin quyền: %q", sent)
	}
	if !containsSub(edits, "Allowed") {
		t.Errorf("chưa cập nhật lại tin xin quyền: %q", edits)
	}
	if !containsSub(answers, "Allowed") {
		t.Errorf("chưa answerCallbackQuery: %q", answers)
	}
	if !containsSub(sent, "$0.0042") || !containsSub(sent, "1.2s") {
		t.Errorf("thiếu footer chi phí/thời gian: %q", sent)
	}

	raw2, err := os.ReadFile(decFile)
	if err != nil {
		t.Fatalf("đọc quyết định: %v", err)
	}
	var got struct {
		Subtype  string `json:"subtype"`
		Response struct {
			Behavior           string          `json:"behavior"`
			UpdatedPermissions json.RawMessage `json:"updatedPermissions"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw2, &got); err != nil {
		t.Fatalf("parse quyết định %s: %v", raw2, err)
	}
	if got.Subtype != "success" || got.Response.Behavior != "allow" {
		t.Errorf("quyết định sai: %s", raw2)
	}
	if !strings.Contains(string(got.Response.UpdatedPermissions), "acceptEdits") {
		t.Errorf("thiếu updatedPermissions: %s", raw2)
	}

	if !b.stopClaude(sess) {
		t.Error("phiên claude đáng lẽ vẫn còn để dọn")
	}
}

// TestClaudeAskTimeout: không ai bấm nút -> tự động từ chối sau timeout.
func TestClaudeAskTimeout(t *testing.T) {
	bin := buildFakeClaude(t)
	decFile := filepath.Join(t.TempDir(), "decision.json")
	t.Setenv("TT_FAKE_DECISION", decFile)

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: bin, StartDir: t.TempDir(), ClaudeAskTimeout: 1})
	sess := b.session(1)
	defer b.stopClaude(sess)

	done := make(chan struct{})
	go func() {
		b.runClaude(context.Background(), 1, sess, textPrompt("chào bạn"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("lượt Claude không kết thúc")
	}

	raw, err := os.ReadFile(decFile)
	if err != nil {
		t.Fatalf("đọc quyết định: %v", err)
	}
	if !strings.Contains(string(raw), `"behavior":"deny"`) {
		t.Errorf("đáng lẽ tự động từ chối: %s", raw)
	}
	_, edits, _ := tg.all()
	if !containsSub(edits, "timed out") {
		t.Errorf("chưa báo lý do hết thời gian: %q", edits)
	}
}

// TestClaudeCancel: /cancel giữa lúc đang chờ quyền -> hủy lượt, deny yêu cầu.
func TestClaudeCancel(t *testing.T) {
	bin := buildFakeClaude(t)
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "decision.json"))

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: bin, StartDir: t.TempDir()})
	sess := b.session(1)
	defer b.stopClaude(sess)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.runClaude(ctx, 1, sess, textPrompt("chào bạn"))
		close(done)
	}()
	select {
	case <-tg.askCh:
	case <-time.After(30 * time.Second):
		t.Fatal("không nhận được yêu cầu xin quyền")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("hủy không kết thúc được lượt")
	}
	sent, _, _ := tg.all()
	if !containsSub(sent, "Cancelled") {
		t.Errorf("chưa báo đã hủy: %q", sent)
	}
}

// TestRealClaudeEndToEnd chạy với `claude` thật (tốn API) — bật bằng:
//
//	TT_E2E_CLAUDE=1 go test -run TestRealClaudeEndToEnd -v -timeout 10m
func TestRealClaudeEndToEnd(t *testing.T) {
	if os.Getenv("TT_E2E_CLAUDE") != "1" {
		t.Skip("đặt TT_E2E_CLAUDE=1 để chạy với claude thật")
	}
	dir := t.TempDir()
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: "claude", StartDir: dir})
	sess := b.session(1)
	defer b.stopClaude(sess)

	done := make(chan struct{})
	go func() {
		b.runClaude(context.Background(), 1, sess,
			textPrompt("Dùng tool Write tạo file hello.txt với nội dung 'xin chao'. Trả lời thật ngắn."))
		close(done)
	}()

	// Bấm "Cho phép" cho mọi yêu cầu quyền cho tới khi lượt kết thúc.
	for {
		select {
		case data := <-tg.askCh:
			var q callbackQuery
			raw := fmt.Sprintf(`{"id":"cb","from":{"id":7},"message":{"message_id":1,"chat":{"id":1}},"data":%q}`, data)
			json.Unmarshal([]byte(raw), &q)
			b.handleCallback(&q)
		case <-done:
			if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err != nil {
				sent, _, _ := tg.all()
				t.Fatalf("claude chưa tạo được file: %v\ntin nhắn: %q", err, sent)
			}
			sent, edits, _ := tg.all()
			t.Logf("sent=%q\nedits=%q", sent, edits)
			return
		case <-time.After(8 * time.Minute):
			t.Fatal("quá thời gian")
		}
	}
}

func TestHumanDur(t *testing.T) {
	cases := map[int64]string{
		0: "0.0s", 1234: "1.2s", 59_900: "59.9s",
		60_000: "1m00s", 1_000_800: "16m40s", 3_599_000: "59m59s",
		3_600_000: "1h00m", 7_530_000: "2h05m",
	}
	for ms, want := range cases {
		if got := humanDur(ms); got != want {
			t.Errorf("humanDur(%d) = %q, muốn %q", ms, got, want)
		}
	}
}

// total_cost_usd là tổng cộng dồn của cả phiên; turnCost phải tách ra chi phí
// của riêng lượt, và chịu được trường hợp tổng bị reset (/clear, resume).
func TestTurnCost(t *testing.T) {
	s := &claudeSession{}
	steps := []struct {
		total, wantTurn float64
	}{
		{0.10, 0.10}, // lượt đầu: cả tổng là của lượt này
		{0.35, 0.25},
		{0.35, 0.00}, // không tốn thêm
		{0.05, 0.05}, // tổng nhỏ lại -> phiên đã reset, lấy nguyên tổng
		{0.09, 0.04},
	}
	for i, st := range steps {
		turn, running := s.turnCost(st.total)
		if diff := turn - st.wantTurn; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("bước %d: turn = %.4f, muốn %.4f", i+1, turn, st.wantTurn)
		}
		if running != st.total {
			t.Errorf("bước %d: running = %.4f, muốn %.4f", i+1, running, st.total)
		}
	}
}

// TestFooterCostDelta: lượt thứ hai phải hiện chi phí của riêng nó, kèm tổng
// phiên có nhãn — chứ không in tổng cộng dồn như thể là giá của lượt đó.
func TestFooterCostDelta(t *testing.T) {
	bin := buildFakeClaude(t)
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "dec.json"))

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: bin, StartDir: t.TempDir(), ClaudeAskTimeout: 1})
	sess := b.session(1)
	defer b.stopClaude(sess)

	footer := func() string {
		sent, _, _ := tg.all()
		for i := len(sent) - 1; i >= 0; i-- {
			if strings.HasPrefix(sent[i], "— ") {
				return sent[i]
			}
		}
		return ""
	}

	// Lượt 1: tổng == chi phí lượt -> không nêu tổng phiên.
	b.runClaude(context.Background(), 1, sess, textPrompt("lần 1"))
	first := footer()
	if !strings.Contains(first, "$0.0042") || !strings.Contains(first, "1.2s") {
		t.Fatalf("footer lượt 1 sai: %q", first)
	}
	if strings.Contains(first, "session") {
		t.Errorf("lượt đầu không nên nêu tổng phiên: %q", first)
	}
	if !strings.Contains(first, "2 steps") {
		t.Errorf("thiếu số bước: %q", first)
	}

	// Lượt 2: claude giả báo tổng 0.0084 -> lượt này chỉ 0.0042.
	b.runClaude(context.Background(), 1, sess, textPrompt("lần 2"))
	second := footer()
	if !strings.Contains(second, "$0.0042") {
		t.Errorf("footer lượt 2 phải là chi phí của riêng lượt: %q", second)
	}
	if !strings.Contains(second, "session $0.0084") {
		t.Errorf("footer lượt 2 phải nêu tổng phiên: %q", second)
	}
}
