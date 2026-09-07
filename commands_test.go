package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommandSurface: mỗi tên lệnh (kể cả tên gọi cũ) phải vào đúng nhánh và
// trả lời gì đó — không được im lặng rơi xuống nhánh chạy shell.
func TestCommandSurface(t *testing.T) {
	cwd := t.TempDir()
	root := fakeProjects(t, cwd, true)
	parent := t.TempDir()
	if err := os.Rename(root, filepath.Join(parent, "projects")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", parent)
	t.Setenv("TT_FAKE_ARGS", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "dec.json"))

	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: buildFakeClaude(t), Shell: "/bin/bash",
		StartDir: cwd, ClaudeAskTimeout: 1})
	sess := b.session(1)
	defer b.stopClaude(sess)

	cases := []struct{ cmd, want string }{
		{"/help", "telegram-terminal"},
		{"/start", "telegram-terminal"},
		// Xem trạng thái: 1 nhánh, 4 tên.
		{"/status", "chế độ:"},
		{"/mode", "chế độ:"},
		{"/st", "chế độ:"},
		{"/pwd", "chế độ:"},
		// Chạy việc: /sh và /c trống = chuyển chế độ.
		{"/sh", "Chế độ shell"},
		{"/shell", "Chế độ shell"},
		{"/c", "Chế độ Claude"},
		{"/claude", "Chế độ Claude"},
		{"/sh echo xin-chao", "xin-chao"},
		// Phiên: 1 nhánh, 6 tên.
		{"/session", "Phiên Claude tại"},
		{"/sessions", "Phiên Claude tại"},
		{"/ss", "Phiên Claude tại"},
		{"/session new", "phiên Claude mới"},
		{"/newchat", "phiên Claude mới"},
		{"/resume", "Dùng: /session"},
		{"/r", "Dùng: /session"},
		{"/session 33333333", "Đã mở lại phiên"},
		{"/session 1", "Đã mở lại phiên"},
		{"/resume 1", "Đã mở lại phiên"},
		// Quyền, hủy, reset.
		{"/perm", "Chế độ quyền:"},
		{"/permission", "Chế độ quyền:"},
		{"/perm acceptEdits", "acceptEdits"},
		{"/cancel", "Không có lệnh nào đang chạy"},
		{"/stop", "Không có lệnh nào đang chạy"},
		{"/reset", "về chế độ shell"},
		// Không phải lệnh của bot -> chạy shell như thường.
		{"/khong-phai-lenh-cua-bot", "No such file or directory"},
	}

	for _, c := range cases {
		before, _, _ := tg.all()
		b.handle(msgUpdate(t, c.cmd))
		sent, _, _ := tg.all()
		if len(sent) == len(before) {
			t.Errorf("%q: bot không trả lời gì", c.cmd)
			continue
		}
		got := strings.Join(sent[len(before):], "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%q: mong có %q, nhận:\n%s", c.cmd, c.want, got)
		}
	}
}

// /reset phải xóa cả chế độ quyền riêng của chat, không chỉ thư mục & phiên.
func TestResetXoaQuyenRieng(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, StartDir: "/home/user", ClaudePermissionMode: "manual"})
	sess := b.session(1)

	b.handle(msgUpdate(t, "/perm auto"))
	if got := b.permModeFor(sess); got != "auto" {
		t.Fatalf("permModeFor = %q, muốn auto", got)
	}
	b.handle(msgUpdate(t, "/reset"))
	if got := b.permModeFor(sess); got != "manual" {
		t.Errorf("sau /reset permModeFor = %q, muốn quay về config (manual)", got)
	}
}
