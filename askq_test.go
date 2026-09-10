package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAskQuestions(t *testing.T) {
	raw := json.RawMessage(`{"questions":[{"question":"Chọn cái nào?","header":"Lib",
		"multiSelect":true,"options":[{"label":"A","description":"a"},{"label":"B"}]}],
		"metadata":{"source":"remember"}}`)
	qs, input, ok := parseAskQuestions(raw)
	if !ok {
		t.Fatal("phải parse được input hợp lệ")
	}
	if len(qs) != 1 || qs[0].Question != "Chọn cái nào?" || !qs[0].MultiSelect {
		t.Fatalf("parse sai: %+v", qs)
	}
	if len(qs[0].Options) != 2 || qs[0].Options[1].Label != "B" {
		t.Fatalf("đáp án sai: %+v", qs[0].Options)
	}
	// Field lạ phải giữ nguyên để trả lại nguyên vẹn kèm answers.
	if _, has := input["metadata"]; !has {
		t.Errorf("input gốc mất metadata: %v", input)
	}

	bad := []string{
		``,
		`{"questions":[]}`,
		`{"questions":[{"question":"","options":[{"label":"A"}]}]}`,
		`{"questions":[{"question":"Hỏi?","options":[]}]}`,
		`không phải json`,
	}
	for _, b := range bad {
		if _, _, ok := parseAskQuestions(json.RawMessage(b)); ok {
			t.Errorf("input hỏng phải bị từ chối: %s", b)
		}
	}
}

// askqBot dựng bot + fakeclaude ở chế độ hỏi lại, chạy sẵn 1 lượt Claude.
// Trả về callback_data của nút đầu tiên trong từng tin nhắn câu hỏi.
func askqBot(t *testing.T, mode string, questions int) (*tgMock, *Bot, []string, chan struct{}) {
	t.Helper()
	t.Setenv("TT_FAKE_ASKQ", mode)
	t.Setenv("TT_FAKE_DECISION", filepath.Join(t.TempDir(), "decision.json"))

	tg := newTGMock()
	t.Cleanup(tg.srv.Close)
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: buildFakeClaude(t), StartDir: t.TempDir()})
	sess := b.session(1)
	t.Cleanup(func() { b.stopClaude(sess) })

	done := make(chan struct{})
	go func() {
		b.runClaude(context.Background(), 1, sess, textPrompt("chọn giúp mình"))
		close(done)
	}()

	var first []string
	for i := 0; i < questions; i++ {
		select {
		case data := <-tg.askCh:
			first = append(first, data)
		case <-time.After(30 * time.Second):
			t.Fatalf("thiếu tin nhắn câu hỏi thứ %d", i+1)
		}
	}
	return tg, b, first, done
}

// tapButton bấm 1 nút inline đúng đường callback thật.
func tapButton(t *testing.T, b *Bot, data string) {
	t.Helper()
	var q callbackQuery
	raw := fmt.Sprintf(`{"id":"cb","from":{"id":7},"message":{"message_id":101,"chat":{"id":1}},"data":%q}`, data)
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	b.handleCallback(&q)
}

// decisionResponse đọc control_response mà bot đã gửi cho claude.
func decisionResponse(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("TT_FAKE_DECISION"))
	if err != nil {
		t.Fatalf("chưa có quyết định nào được gửi: %v", err)
	}
	var env struct {
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("quyết định không phải JSON: %v (%s)", err, data)
	}
	return env.Response
}

// TestAskQuestionsMulti: 2 câu hỏi 1 lúc, câu sau cho chọn nhiều -> chọn xong
// bấm Gửi, claude nhận updatedInput.answers với nhiều đáp án nối bằng ", ".
func TestAskQuestionsMulti(t *testing.T) {
	tg, b, first, done := askqBot(t, "two", 2)
	if len(first) != 2 {
		t.Fatalf("phải có 2 tin nhắn câu hỏi, có %d", len(first))
	}
	askID := strings.Split(first[0], "|")[1]

	sent, _, _ := tg.all()
	if !containsSub(sent, "Thư viện") || !containsSub(sent, "không thêm phụ thuộc") {
		t.Errorf("tin nhắn câu hỏi thiếu tiêu đề/mô tả đáp án: %q", sent)
	}
	if !containsSub(sent, "câu 1/2") && !containsSub(sent, "question 1/2") {
		t.Errorf("thiếu đánh số câu hỏi: %q", sent)
	}
	// Chưa trả lời đủ thì Gửi phải báo lại chứ không chốt.
	tapButton(t, b, "q|"+askID+"|s")
	if _, err := os.Stat(os.Getenv("TT_FAKE_DECISION")); err == nil {
		t.Fatal("chưa chọn gì mà đã gửi cho claude")
	}

	tapButton(t, b, "q|"+askID+"|0|0") // câu 1: stdlib (chọn một)
	tapButton(t, b, "q|"+askID+"|1|0") // câu 2: log
	tapButton(t, b, "q|"+askID+"|1|2") // câu 2: + tracing
	tapButton(t, b, "q|"+askID+"|1|2") // bấm lại -> bỏ tracing
	tapButton(t, b, "q|"+askID+"|1|1") // câu 2: + metrics

	if _, err := os.Stat(os.Getenv("TT_FAKE_DECISION")); err == nil {
		t.Fatal("có câu chọn-nhiều thì không được tự gửi")
	}
	tapButton(t, b, "q|"+askID+"|s")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("lượt Claude không kết thúc")
	}

	resp := decisionResponse(t)
	if resp["behavior"] != "allow" {
		t.Fatalf("phải là allow: %v", resp)
	}
	input, _ := resp["updatedInput"].(map[string]any)
	answers, _ := input["answers"].(map[string]any)
	if answers["Dùng thư viện nào?"] != "stdlib" {
		t.Errorf("đáp án câu 1 sai: %v", answers)
	}
	if answers["Bật thêm phần nào?"] != "log, metrics" {
		t.Errorf("đáp án câu 2 sai (phải nối bằng \", \"): %v", answers)
	}
	if _, has := input["questions"]; !has {
		t.Errorf("updatedInput phải giữ nguyên input gốc: %v", input)
	}

	_, edits, answersToast := tg.all()
	if !containsSub(edits, "log, metrics") {
		t.Errorf("tin nhắn chưa được viết lại kèm đáp án: %q", edits)
	}
	if !containsSub(answersToast, "Answers sent") {
		t.Errorf("chưa báo lại cho người bấm: %q", answersToast)
	}
}

// TestAskQuestionsAutoSubmit: 1 câu chọn-một -> bấm đáp án là gửi luôn.
func TestAskQuestionsAutoSubmit(t *testing.T) {
	tg, b, first, done := askqBot(t, "one", 1)
	askID := strings.Split(first[0], "|")[1]

	// Chỉ có nút Hủy ở hàng điều khiển, không có nút Gửi.
	if mk := strings.Join(tg.allMarkups(), " "); strings.Contains(mk, "|s\"") {
		t.Errorf("câu chọn-một duy nhất thì không cần nút Gửi: %s", mk)
	}

	tapButton(t, b, "q|"+askID+"|0|1") // cobra
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("lượt Claude không kết thúc")
	}

	resp := decisionResponse(t)
	input, _ := resp["updatedInput"].(map[string]any)
	answers, _ := input["answers"].(map[string]any)
	if resp["behavior"] != "allow" || answers["Dùng thư viện nào?"] != "cobra" {
		t.Fatalf("đáp án gửi đi sai: %v", resp)
	}
}

// TestAskQuestionsCancel: bấm Hủy -> claude nhận deny kèm lý do.
func TestAskQuestionsCancel(t *testing.T) {
	tg, b, first, done := askqBot(t, "one", 1)
	askID := strings.Split(first[0], "|")[1]

	tapButton(t, b, "q|"+askID+"|x")
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("lượt Claude không kết thúc")
	}

	resp := decisionResponse(t)
	if resp["behavior"] != "deny" {
		t.Fatalf("phải là deny: %v", resp)
	}
	if msg, _ := resp["message"].(string); !strings.Contains(msg, "denied it via Telegram") {
		t.Errorf("thiếu lý do từ chối: %v", resp)
	}

	// Nút bấm sau khi đã chốt phải bị coi là cũ.
	tapButton(t, b, "q|"+askID+"|0|0")
	_, _, toasts := tg.all()
	if !containsSub(toasts, "no longer waiting") {
		t.Errorf("bấm nút cũ phải báo hết hạn: %q", toasts)
	}
}
