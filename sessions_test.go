package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/user/project/telegram-terminal": "-home-user-project-telegram-terminal",
		"/tmp/tt.slug_test-01":                 "-tmp-tt-slug-test-01",
		"/tmp/claude-1000/-home-user":          "-tmp-claude-1000--home-user",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, muốn %q", in, got, want)
		}
	}
}

// writeSession tạo 1 file phiên giả giống Claude Code ghi ra.
func writeSession(t *testing.T, dir, id, cwd, prompt string, mtime time.Time) {
	t.Helper()
	lines := []any{
		map[string]any{"type": "queue-operation", "operation": "enqueue", "sessionId": id, "content": prompt},
		map[string]any{"type": "user", "cwd": cwd, "sessionId": id,
			"message": map[string]any{"role": "user", "content": prompt}},
		map[string]any{"type": "assistant", "cwd": cwd, "sessionId": id,
			"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}}}},
	}
	var sb strings.Builder
	for _, l := range lines {
		data, _ := json.Marshal(l)
		sb.Write(data)
		sb.WriteByte('\n')
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// fakeProjects dựng cây ~/.claude/projects giả cho cwd, trả về root.
func fakeProjects(t *testing.T, cwd string, slug bool) string {
	t.Helper()
	root := t.TempDir()
	name := projectSlug(cwd)
	if !slug {
		name = "-thu-muc-lech-slug"
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeSession(t, dir, "11111111-1111-1111-1111-111111111111", cwd, "phiên cũ nhất", now.Add(-2*time.Hour))
	writeSession(t, dir, "22222222-2222-2222-2222-222222222222", cwd, "sửa lại hàm parse config", now.Add(-time.Hour))
	writeSession(t, dir, "33333333-3333-3333-3333-333333333333", cwd, "thêm test cho phần liệt kê", now)
	// Phiên của thư mục khác -> không được lẫn vào.
	other := filepath.Join(root, projectSlug("/tmp/khac"))
	os.MkdirAll(other, 0o755)
	writeSession(t, dir+"/../"+filepath.Base(other), "44444444-4444-4444-4444-444444444444", "/tmp/khac", "việc khác", now)
	return root
}

func TestListSessions(t *testing.T) {
	cwd := "/home/user/project/telegram-terminal"
	root := fakeProjects(t, cwd, true)

	list := listSessions(root, cwd, 12)
	if len(list) != 3 {
		t.Fatalf("muốn 3 phiên, có %d: %+v", len(list), list)
	}
	if !strings.HasPrefix(list[0].ID, "33333333") {
		t.Errorf("phiên mới nhất phải đứng đầu, có %q", list[0].ID)
	}
	if list[0].First != "thêm test cho phần liệt kê" {
		t.Errorf("nhãn sai: %q", list[0].First)
	}
	if list[0].Cwd != cwd {
		t.Errorf("cwd sai: %q", list[0].Cwd)
	}
	if got := listSessions(root, cwd, 2); len(got) != 2 {
		t.Errorf("giới hạn max không có tác dụng: %d", len(got))
	}
	if got := listSessions(root, "/khong/co", 12); len(got) != 0 {
		t.Errorf("thư mục không có phiên phải trả rỗng, có %d", len(got))
	}
}

// Slug không khớp (đường dẫn lạ) -> vẫn dò ra nhờ cwd ghi trong file phiên.
func TestListSessionsFallbackTheoCwd(t *testing.T) {
	cwd := "/home/user/dự án/việt"
	root := fakeProjects(t, cwd, false)
	list := listSessions(root, cwd, 12)
	if len(list) != 3 {
		t.Fatalf("muốn 3 phiên qua nhánh dò cwd, có %d", len(list))
	}
}

func TestResolveSession(t *testing.T) {
	list := []sessionInfo{
		{ID: "aaaaaaaa-1111-1111-1111-111111111111"},
		{ID: "aaaaaaaa-2222-2222-2222-222222222222"},
		{ID: "bbbbbbbb-3333-3333-3333-333333333333"},
	}
	if got, err := resolveSession(langEN, list, "3"); err != nil || got != list[2].ID {
		t.Errorf("theo số: %q %v", got, err)
	}
	if got, err := resolveSession(langEN, list, "bbbbbbbb"); err != nil || got != list[2].ID {
		t.Errorf("theo prefix: %q %v", got, err)
	}
	if _, err := resolveSession(langEN, list, "aaaaaaaa"); err == nil {
		t.Error("prefix trùng nhiều phiên phải báo lỗi")
	}
	if _, err := resolveSession(langEN, list, "9"); err == nil {
		t.Error("số ngoài danh sách phải báo lỗi")
	}
	if _, err := resolveSession(langEN, list, ""); err == nil {
		t.Error("thiếu tham số phải báo lỗi")
	}
	// Id toàn chữ số không được hiểu thành số thứ tự.
	digits := []sessionInfo{{ID: "33333333-3333-3333-3333-333333333333"}, {ID: "44444444-0000-0000-0000-000000000000"}}
	if got, err := resolveSession(langEN, digits, "33333333"); err != nil || got != digits[0].ID {
		t.Errorf("id toàn chữ số: %q %v", got, err)
	}
	if got, err := resolveSession(langEN, digits, "2"); err != nil || got != digits[1].ID {
		t.Errorf("số thứ tự 1-2 chữ số vẫn phải là số thứ tự: %q %v", got, err)
	}

	// Id lạ nhưng đủ dài -> vẫn cho thử.
	if got, err := resolveSession(langEN, list, "cccccccc-4444"); err != nil || got != "cccccccc-4444" {
		t.Errorf("id ngoài danh sách: %q %v", got, err)
	}
}

// TestSessionsVaResumeQuaTelegram: /sessions rồi /resume 1 đi qua đúng đường
// handle() và bot phải truyền --resume <id> cho claude.
func TestSessionsVaResumeQuaTelegram(t *testing.T) {
	cwd := t.TempDir()
	root := fakeProjects(t, cwd, true)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(root)) // projectsDir() = <root>/projects
	if err := os.Rename(root, filepath.Join(filepath.Dir(root), "projects")); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("TT_FAKE_ARGS", argsFile)
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "dec.json"))

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: buildFakeClaude(t), StartDir: cwd})
	sess := b.session(1)
	defer b.stopClaude(sess)

	b.handle(msgUpdate(t, "/sessions"))
	sent, _, _ := tg.all()
	if !containsSub(sent, "thêm test cho phần liệt kê") || !containsSub(sent, "33333333") {
		t.Fatalf("/sessions chưa liệt kê phiên: %q", sent)
	}

	b.handle(msgUpdate(t, "/resume 1"))
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("claude chưa được khởi động lại: %v", err)
	}
	if !strings.Contains(string(got), "--resume 33333333-3333-3333-3333-333333333333") {
		t.Errorf("thiếu --resume đúng id, args = %q", got)
	}
	sent, _, _ = tg.all()
	if !containsSub(sent, "Resumed session") {
		t.Errorf("chưa báo mở lại phiên: %q", sent)
	}

	// /sessions phải đánh dấu phiên đang mở ngay trong danh sách, và không
	// lặp lại khối thông tin phiên (đó là việc của /status).
	b.handle(msgUpdate(t, "/sessions"))
	sent, _, _ = tg.all()
	last := sent[len(sent)-1]
	if !strings.Contains(last, "▶️ open") {
		t.Errorf("/sessions chưa đánh dấu phiên đang mở:\n%s", last)
	}
	if strings.Contains(last, "permissions") || strings.Contains(last, "mode:") {
		t.Errorf("/sessions không nên lặp thông tin của /status:\n%s", last)
	}

	// /status là chỗ duy nhất xem tổng trạng thái.
	b.handle(msgUpdate(t, "/status"))
	sent, _, _ = tg.all()
	st := sent[len(sent)-1]
	for _, want := range []string{"mode:", "📁", "Session:", "permissions", "33333333"} {
		if !strings.Contains(st, want) {
			t.Errorf("/status thiếu %q:\n%s", want, st)
		}
	}

	// cd sang thư mục khác -> /status phải cảnh báo phiên đang ở thư mục cũ.
	sess.mu.Lock()
	sess.cwd = "/tmp"
	sess.mu.Unlock()
	b.handle(msgUpdate(t, "/status"))
	sent, _, _ = tg.all()
	if !strings.Contains(sent[len(sent)-1], "/session new to reopen") {
		t.Errorf("/status chưa cảnh báo lệch thư mục:\n%s", sent[len(sent)-1])
	}
}

// Chưa mở phiên nào thì /status vẫn phải nói rõ quyền nào sẽ được áp.
func TestStatusTextChuaCoPhien(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, StartDir: "/home/user", ClaudePermissionMode: "acceptEdits"})
	got := b.statusText(1, b.session(1))
	for _, want := range []string{"mode: <b>shell</b>", "/home/user", "none open", "acceptEdits"} {
		if !strings.Contains(got, want) {
			t.Errorf("/status thiếu %q:\n%s", want, got)
		}
	}

	// Claude tắt trong config -> nói thẳng, không bàn tới quyền.
	b2 := tg.bot(Config{StartDir: "/home/user"})
	if got := b2.statusText(2, b2.session(2)); !strings.Contains(got, "not enabled in the config") {
		t.Errorf("thiếu thông báo Claude chưa bật:\n%s", got)
	}
}

func TestOneLine(t *testing.T) {
	cases := map[string]string{
		"  nhiều   khoảng\ntrắng ":                                 "nhiều khoảng trắng",
		"<local-command-caveat>Caveat: abc</local-command-caveat>": "Caveat: abc",
		"<command-name>/init</command-name> chạy đi":               "/init chạy đi",
		"so sánh a < b và c > d":                                   "so sánh a < b và c > d",
	}
	for in, want := range cases {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, muốn %q", in, got, want)
		}
	}
}
