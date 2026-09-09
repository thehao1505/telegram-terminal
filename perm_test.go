package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermModeNames(t *testing.T) {
	// Cờ CLI gọi mặc định là "manual", control_request gọi là "default".
	if got := ctlPermMode("manual"); got != "default" {
		t.Errorf("ctlPermMode(manual) = %q, muốn default", got)
	}
	if got := flagPermMode("default"); got != "manual" {
		t.Errorf("flagPermMode(default) = %q, muốn manual", got)
	}
	if got := normPermMode("default"); got != "manual" {
		t.Errorf("normPermMode(default) = %q, muốn manual", got)
	}
	for _, m := range []string{"acceptEdits", "auto", "dontAsk", "plan", "bypassPermissions"} {
		if ctlPermMode(m) != m || flagPermMode(m) != m || normPermMode(m) != m {
			t.Errorf("%q không nên bị đổi tên", m)
		}
		if !validPermMode(m) {
			t.Errorf("%q phải hợp lệ", m)
		}
	}
	if validPermMode("khong-ton-tai") {
		t.Error("chế độ lạ phải bị coi là không hợp lệ")
	}
	if permModeDesc("auto") == "" {
		t.Error("thiếu mô tả cho auto")
	}
}

// TestPermModeQuaTelegram: /perm khi chưa có phiên -> chỉ đặt mặc định; khi đã
// có phiên -> gửi set_permission_mode; bypassPermissions bị từ chối thì mặc
// định của chat không bị đổi.
func TestPermModeQuaTelegram(t *testing.T) {
	cwd := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	modesFile := filepath.Join(t.TempDir(), "modes.txt")
	t.Setenv("TT_FAKE_ARGS", argsFile)
	t.Setenv("TT_FAKE_MODES", modesFile)
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "dec.json"))

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: buildFakeClaude(t), StartDir: cwd, ClaudeAskTimeout: 1})
	sess := b.session(1)
	defer b.stopClaude(sess)

	modes := func() string {
		data, _ := os.ReadFile(modesFile)
		return string(data)
	}

	// 1) Chưa có phiên -> chỉ ghi mặc định, không gửi control_request.
	b.handle(msgUpdate(t, "/perm auto"))
	if got := b.permModeFor(sess); got != "auto" {
		t.Fatalf("mặc định của chat = %q, muốn auto", got)
	}
	if modes() != "" {
		t.Errorf("chưa có phiên thì không được gửi set_permission_mode, đã gửi: %q", modes())
	}
	sent, _, _ := tg.all()
	if !containsSub(sent, "phiên Claude tiếp theo") {
		t.Errorf("thiếu thông báo áp dụng cho phiên sau: %q", sent)
	}

	// 2) Mở phiên -> claude phải được khởi động với --permission-mode auto.
	b.handle(msgUpdate(t, "/c chào bạn"))
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("claude chưa khởi động: %v", err)
	}
	if !strings.Contains(string(args), "--permission-mode auto") {
		t.Errorf("args thiếu chế độ auto: %q", args)
	}

	// 3) Đổi khi phiên đang mở -> gửi set_permission_mode acceptEdits.
	b.handle(msgUpdate(t, "/perm acceptEdits"))
	if !strings.Contains(modes(), "acceptEdits") {
		t.Errorf("chưa gửi acceptEdits: %q", modes())
	}
	sess.mu.Lock()
	cs := sess.claude
	sess.mu.Unlock()
	if got := cs.curPermMode(); got != "acceptEdits" {
		t.Errorf("phiên báo chế độ %q, muốn acceptEdits", got)
	}

	// 4) manual phải đi trên dây dưới tên "default".
	b.handle(msgUpdate(t, "/perm manual"))
	if !strings.Contains(modes(), "\ndefault\n") && !strings.HasPrefix(modes(), "default\n") {
		t.Errorf("manual phải gửi \"default\", các mode đã gửi: %q", modes())
	}

	// 5) bypassPermissions bị claude từ chối -> báo lỗi, mặc định giữ nguyên.
	b.handle(msgUpdate(t, "/perm bypassPermissions"))
	sent, _, _ = tg.all()
	if !containsSub(sent, "không đổi được chế độ") || !containsSub(sent, "dangerously-skip-permissions") {
		t.Errorf("chưa báo lỗi kèm gợi ý: %q", sent[len(sent)-1:])
	}
	if got := b.permModeFor(sess); got != "manual" {
		t.Errorf("mặc định bị đổi thành %q dù lệnh thất bại", got)
	}

	// 6) Chế độ lạ -> chặn ngay, không gửi gì.
	before := modes()
	b.handle(msgUpdate(t, "/perm khong-ton-tai"))
	if modes() != before {
		t.Error("chế độ không hợp lệ vẫn bị gửi cho claude")
	}
	sent, _, _ = tg.all()
	if !containsSub(sent, "chế độ không hợp lệ") {
		t.Errorf("chưa báo chế độ không hợp lệ: %q", sent)
	}

	// 7) /perm không tham số -> bảng trạng thái kèm nút.
	b.handle(msgUpdate(t, "/perm"))
	sent, _, _ = tg.all()
	if !containsSub(sent, "Chế độ quyền:") || !containsSub(sent, "acceptEdits") {
		t.Errorf("bảng /perm thiếu nội dung: %q", sent)
	}
}

// TestRealPermMode chạy với `claude` thật (tốn API):
//
//	TT_E2E_CLAUDE=1 go test -run TestRealPermMode -v -timeout 10m
func TestRealPermMode(t *testing.T) {
	if os.Getenv("TT_E2E_CLAUDE") != "1" {
		t.Skip("đặt TT_E2E_CLAUDE=1 để chạy với claude thật")
	}
	dir := t.TempDir()
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: "claude", StartDir: dir})
	sess := b.session(1)
	defer b.stopClaude(sess)

	cs, err := b.claudeFor(1, sess)
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.curPermMode(); got != "manual" {
		t.Errorf("chế độ lúc khởi động = %q, muốn manual", got)
	}
	// manual -> acceptEdits: sau đó Write không được hỏi quyền nữa.
	if err := cs.setPermMode("acceptEdits"); err != nil {
		t.Fatalf("đổi sang acceptEdits: %v", err)
	}
	if got := cs.curPermMode(); got != "acceptEdits" {
		t.Errorf("phiên báo %q, muốn acceptEdits", got)
	}
	if err := cs.sendPrompt(context.Background(),
		textPrompt("Dùng tool Write tạo file p.txt nội dung 'ok'. Trả lời 1 câu.")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.txt")); err != nil {
		t.Errorf("acceptEdits mà vẫn không ghi được file: %v", err)
	}
	sent, _, _ := tg.all()
	if containsSub(sent, "xin phép") {
		t.Errorf("acceptEdits mà vẫn hỏi quyền: %q", sent)
	}
	// Quay lại manual: trên dây phải là "default" và claude phải nhận.
	if err := cs.setPermMode("manual"); err != nil {
		t.Fatalf("đổi lại manual: %v", err)
	}
	if got := cs.curPermMode(); got != "manual" {
		t.Errorf("phiên báo %q, muốn manual", got)
	}
	// bypassPermissions phải bị từ chối vì không khởi động kèm cờ nguy hiểm.
	if err := cs.setPermMode("bypassPermissions"); err == nil {
		t.Error("bypassPermissions đáng lẽ bị từ chối")
	} else {
		t.Logf("bypassPermissions bị chặn đúng như mong đợi: %v", err)
	}
}

// Chế độ mặc định đi trên dây dưới tên "default"; bot phải quy đổi lại thành
// "manual" khi đọc current_permission_mode từ trả lời initialize.
func TestInitEchoDefaultMode(t *testing.T) {
	t.Setenv("TT_FAKE_ARGS", filepath.Join(t.TempDir(), "args.txt"))
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: buildFakeClaude(t), StartDir: t.TempDir()})
	sess := b.session(1)
	defer b.stopClaude(sess)

	cs, err := b.claudeFor(1, sess)
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.curPermMode(); got != "manual" {
		t.Errorf("curPermMode() = %q, muốn manual", got)
	}
	// Chưa chạy lượt nào -> chưa có session id, và phải nói rõ chứ không để rỗng.
	if got := cs.describe(); !strings.Contains(got, "mới (chưa có id)") {
		t.Errorf("describe() = %q, muốn nói rõ là phiên mới", got)
	}
}
