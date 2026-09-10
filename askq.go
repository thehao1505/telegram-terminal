// Trả lời tool AskUserQuestion — khi Claude hỏi ngược người dùng và gợi ý sẵn
// các đáp án.
//
// Claude xin dùng tool này qua control_request/can_use_tool như mọi tool khác,
// nhưng câu trả lời không phải "cho phép hay không": host phải chọn hộ người
// dùng rồi trả về chính input đó kèm field "answers" (map câu hỏi -> nhãn đáp
// án đã chọn). Bot dựng mỗi câu hỏi thành 1 tin nhắn kèm nút bấm:
//
//   - chọn một: bấm 1 nút là xong câu đó;
//   - chọn nhiều (multiSelect): bấm để bật/tắt từng đáp án, xong bấm Gửi;
//   - nhiều câu hỏi 1 lúc: mỗi câu 1 tin nhắn, trả lời xong hết mới gửi đi.
//
// Nhiều đáp án của cùng một câu nối bằng ", " — đúng như CLI tự gộp mảng lựa
// chọn thành chuỗi trước khi đưa vào input schema của tool.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	askQuestionTool = "AskUserQuestion"
	askOptLabelMax  = 120 // nhãn đáp án trong phần chữ
	askOptBtnMax    = 40  // nhãn đáp án trên mặt nút (Telegram hẹp)
	askOptDescMax   = 300
	askQuestionMax  = 400
)

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	MultiSelect bool        `json:"multiSelect"`
	Options     []askOption `json:"options"`
}

// parseAskQuestions tách input của AskUserQuestion, giữ nguyên map gốc để lát
// nữa trả lại kèm answers. Trả về false nếu input không đúng dạng mong đợi —
// lúc đó bên gọi lùi về nút Cho phép / Từ chối như mọi tool khác.
func parseAskQuestions(input json.RawMessage) ([]askQuestion, map[string]any, bool) {
	if len(input) == 0 {
		return nil, nil, false
	}
	var raw map[string]any
	if json.Unmarshal(input, &raw) != nil {
		return nil, nil, false
	}
	var in struct {
		Questions []askQuestion `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		return nil, nil, false
	}
	for _, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 {
			return nil, nil, false
		}
	}
	return in.Questions, raw, true
}

// questionAsk: một lần AskUserQuestion đang chờ người dùng bấm nút.
type questionAsk struct {
	id    string // khóa ngắn để nhét vừa callback_data
	reqID string
	s     *claudeSession
	input map[string]any
	qs    []askQuestion

	mu     sync.Mutex
	picked [][]bool // picked[câu][đáp án]
	msgIDs []int64  // tin nhắn Telegram của từng câu
	sent   bool     // đã chốt -> nút hết tác dụng
}

// askQuestions đẩy bộ câu hỏi ra Telegram rồi chờ người dùng chọn. Chạy trong
// goroutine của handleAsk, nên trả lời control_request luôn ở đây.
func (s *claudeSession) askQuestions(reqID string, ask *pendingAsk, qs []askQuestion, input map[string]any) {
	qa := &questionAsk{
		reqID:  reqID,
		s:      s,
		input:  input,
		qs:     qs,
		picked: make([][]bool, len(qs)),
		msgIDs: make([]int64, len(qs)),
	}
	for i, q := range qs {
		qa.picked[i] = make([]bool, len(q.Options))
	}

	s.mu.Lock()
	s.askSeq++
	qa.id = "a" + strconv.Itoa(s.askSeq)
	if s.asks == nil {
		s.asks = map[string]*questionAsk{}
	}
	s.asks[qa.id] = qa
	s.mu.Unlock()

	for i := range qs {
		text, kb := qa.render(i)
		qa.mu.Lock()
		qa.msgIDs[i] = s.b.sendText(s.chatID, text, true, kb)
		qa.mu.Unlock()
	}
	s.markSent()

	timeout := time.Duration(s.b.cfg.claudeAskTimeout()) * time.Second
	var dec permDecision
	select {
	case dec = <-ask.ch:
	case <-time.After(timeout):
		dec = permDecision{behavior: "deny", note: s.b.t(s.chatID, "note.timeout")}
	case <-s.exited:
		s.clearAsk(reqID)
		s.dropQuestionAsk(qa.id)
		return
	}
	s.clearAsk(reqID)
	s.dropQuestionAsk(qa.id)
	qa.finish(dec)

	if dec.canceled {
		return // claude đã rút yêu cầu, không cần trả lời nữa
	}
	resp := map[string]any{"behavior": dec.behavior}
	if dec.behavior == "allow" {
		out := make(map[string]any, len(qa.input)+1)
		for k, v := range qa.input {
			out[k] = v
		}
		out["answers"] = dec.answers
		resp["updatedInput"] = out
	} else {
		msg := s.b.t(s.chatID, "deny.reason")
		if dec.note != "" {
			msg += " (" + dec.note + ")"
		}
		resp["message"] = msg
	}
	if err := s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   resp,
		},
	}); err != nil {
		log.Printf("claude: answering the question request failed: %v", err)
	}
}

func (s *claudeSession) askByID(id string) *questionAsk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asks[id]
}

func (s *claudeSession) dropQuestionAsk(id string) {
	s.mu.Lock()
	delete(s.asks, id)
	s.mu.Unlock()
}

// ----------------------------- Dựng tin nhắn ---------------------------

func (qa *questionAsk) render(i int) (string, []byte) {
	qa.mu.Lock()
	defer qa.mu.Unlock()
	return qa.renderLocked(i)
}

func (qa *questionAsk) renderLocked(i int) (string, []byte) {
	q := qa.qs[i]
	l := qa.s.b.lang(qa.s.chatID)

	var sb strings.Builder
	sb.WriteString(qa.titleLocked(i))
	sb.WriteString("\n" + htmlEscape(truncStr(q.Question, askQuestionMax)))

	var rows [][]ikButton
	for j, o := range q.Options {
		mark := pickMark(q.MultiSelect, qa.picked[i][j])
		sb.WriteString(fmt.Sprintf("\n\n%s <b>%d. %s</b>", mark, j+1,
			htmlEscape(truncStr(o.Label, askOptLabelMax))))
		if d := strings.TrimSpace(o.Description); d != "" {
			sb.WriteString("\n" + htmlEscape(truncStr(d, askOptDescMax)))
		}
		rows = append(rows, []ikButton{{
			Text: fmt.Sprintf("%s %d. %s", mark, j+1, truncStr(o.Label, askOptBtnMax)),
			Data: fmt.Sprintf("q|%s|%d|%d", qa.id, i, j),
		}})
	}
	if q.MultiSelect {
		sb.WriteString(l.t("askq.multi.hint"))
	}

	// Hàng điều khiển nằm ở tin nhắn cuối — nó chốt cho cả bộ câu hỏi.
	if i == len(qa.qs)-1 {
		var ctl []ikButton
		// Một câu chọn-một thì bấm đáp án là gửi luôn, khỏi cần nút Gửi. Còn
		// lại vẫn cần, kể cả khi mọi câu đều chọn-một: người dùng có thể muốn
		// gửi mà bỏ trống bớt câu.
		if len(qa.qs) > 1 || !qa.autoSubmits() {
			ctl = append(ctl, ikButton{Text: l.t("btn.askq.send"), Data: "q|" + qa.id + "|s"})
		}
		ctl = append(ctl, ikButton{Text: l.t("btn.askq.cancel"), Data: "q|" + qa.id + "|x"})
		rows = append(rows, ctl)
		sb.WriteString(l.t("ask.timeout.note",
			(time.Duration(qa.s.b.cfg.claudeAskTimeout()) * time.Second).Minutes()))
	}
	return sb.String(), inlineKeyboard(rows...)
}

// titleLocked: dòng đầu của tin nhắn — tiêu đề ngắn Claude đặt cho câu hỏi.
func (qa *questionAsk) titleLocked(i int) string {
	l := qa.s.b.lang(qa.s.chatID)
	head := strings.TrimSpace(qa.qs[i].Header)
	if head == "" {
		head = l.t("askq.header")
	}
	if len(qa.qs) > 1 {
		return l.t("askq.title.n", htmlEscape(head), i+1, len(qa.qs))
	}
	return l.t("askq.title", htmlEscape(head))
}

// summaryLocked: tin nhắn sau khi đã chốt — bỏ danh sách đáp án, chỉ giữ câu
// hỏi và kết quả.
func (qa *questionAsk) summaryLocked(i int, dec permDecision) string {
	l := qa.s.b.lang(qa.s.chatID)
	text := qa.titleLocked(i) + "\n" + htmlEscape(truncStr(qa.qs[i].Question, askQuestionMax))
	switch {
	case dec.canceled:
		return text + l.t("ask.withdrawn")
	case dec.behavior != "allow":
		note := dec.note
		if note == "" {
			note = l.t("note.cancelled")
		}
		return text + l.t("askq.declined", htmlEscape(note))
	}
	if picked := qa.answerLocked(i); picked != "" {
		return text + l.t("askq.chosen", htmlEscape(picked))
	}
	return text + l.t("askq.unanswered")
}

func pickMark(multi, on bool) string {
	switch {
	case multi && on:
		return "☑️"
	case multi:
		return "⬜"
	case on:
		return "✅"
	}
	return "▫️"
}

// ------------------------------ Bấm nút -------------------------------

// pick bật/tắt một đáp án. Trả về chữ cho toast; ok=false nghĩa là yêu cầu đã
// cũ (hết hạn, đã gửi…).
func (qa *questionAsk) pick(qi, oi int) (string, bool) {
	qa.mu.Lock()
	if qa.sent || qi < 0 || qi >= len(qa.qs) || oi < 0 || oi >= len(qa.qs[qi].Options) {
		qa.mu.Unlock()
		return "", false
	}
	q := qa.qs[qi]
	if q.MultiSelect {
		qa.picked[qi][oi] = !qa.picked[qi][oi]
	} else {
		for j := range qa.picked[qi] {
			qa.picked[qi][j] = j == oi
		}
	}
	on := qa.picked[qi][oi]
	toast := pickMark(q.MultiSelect, on) + " " + truncStr(q.Options[oi].Label, askOptBtnMax)
	text, kb := qa.renderLocked(qi)
	msgID := qa.msgIDs[qi]
	auto := qa.autoSubmits() && qa.allAnsweredLocked()
	qa.mu.Unlock()

	if msgID != 0 {
		qa.s.b.editText(qa.s.chatID, msgID, text, true, kb)
	}
	if auto {
		return qa.submit()
	}
	return toast, true
}

// submit chốt câu trả lời và gửi cho Claude.
func (qa *questionAsk) submit() (string, bool) {
	l := qa.s.b.lang(qa.s.chatID)
	qa.mu.Lock()
	if qa.sent {
		qa.mu.Unlock()
		return "", false
	}
	answers := qa.answersLocked()
	if len(answers) == 0 {
		qa.mu.Unlock()
		return l.t("askq.pick.first"), true
	}
	qa.sent = true
	qa.mu.Unlock()

	if !qa.s.decide(qa.reqID, permDecision{behavior: "allow", answers: answers}) {
		return "", false
	}
	return l.t("askq.sent"), true
}

// cancel: không trả lời câu hỏi nào cả — với Claude là tool bị từ chối.
func (qa *questionAsk) cancel() (string, bool) {
	l := qa.s.b.lang(qa.s.chatID)
	qa.mu.Lock()
	if qa.sent {
		qa.mu.Unlock()
		return "", false
	}
	qa.sent = true
	qa.mu.Unlock()

	if !qa.s.decide(qa.reqID, permDecision{behavior: "deny", note: l.t("note.cancelled")}) {
		return "", false
	}
	return l.t("askq.canceled"), true
}

// finish khóa bộ câu hỏi lại và viết kết quả đè lên các tin nhắn.
func (qa *questionAsk) finish(dec permDecision) {
	qa.mu.Lock()
	qa.sent = true
	msgIDs := append([]int64(nil), qa.msgIDs...)
	texts := make([]string, len(qa.qs))
	for i := range qa.qs {
		texts[i] = qa.summaryLocked(i, dec)
	}
	qa.mu.Unlock()

	for i, id := range msgIDs {
		if id != 0 {
			qa.s.b.editText(qa.s.chatID, id, texts[i], true, emptyKeyboard())
		}
	}
}

// ---------------------------- Trạng thái ------------------------------

// autoSubmits: bộ câu hỏi tự gửi ngay khi trả lời đủ, không cần nút Gửi. Chỉ
// đúng khi mọi câu đều chọn-một (chọn-nhiều thì phải chờ người dùng bấm xong).
func (qa *questionAsk) autoSubmits() bool {
	for _, q := range qa.qs {
		if q.MultiSelect {
			return false
		}
	}
	return true
}

func (qa *questionAsk) allAnsweredLocked() bool {
	for i := range qa.qs {
		if qa.answerLocked(i) == "" {
			return false
		}
	}
	return true
}

// answerLocked: đáp án của 1 câu, nhiều lựa chọn thì nối bằng ", ".
func (qa *questionAsk) answerLocked(i int) string {
	var out []string
	for j, on := range qa.picked[i] {
		if on {
			out = append(out, qa.qs[i].Options[j].Label)
		}
	}
	return strings.Join(out, ", ")
}

// answersLocked: map "câu hỏi" -> "đáp án", đúng khóa mà tool đợi. Câu chưa
// trả lời thì bỏ hẳn ra ngoài — Claude hiểu là không ai chọn gì.
func (qa *questionAsk) answersLocked() map[string]string {
	out := map[string]string{}
	for i, q := range qa.qs {
		if a := qa.answerLocked(i); a != "" {
			out[q.Question] = a
		}
	}
	return out
}

// ---------------------------- Callback --------------------------------

// handleQuestionCallback xử lý nút của AskUserQuestion.
// callback_data: "q|<ask>|<câu>|<đáp án>" · "q|<ask>|s" gửi · "q|<ask>|x" hủy.
func (b *Bot) handleQuestionCallback(q *callbackQuery, chatID int64, parts []string) {
	sess := b.session(chatID)
	sess.mu.Lock()
	cs := sess.claude
	sess.mu.Unlock()

	var qa *questionAsk
	if cs != nil {
		qa = cs.askByID(parts[0])
	}
	if qa == nil {
		b.answerCallback(q.ID, b.t(chatID, "cb.stale"))
		return
	}

	var (
		toast string
		ok    bool
	)
	switch {
	case parts[1] == "s":
		toast, ok = qa.submit()
	case parts[1] == "x":
		toast, ok = qa.cancel()
	case len(parts) == 3:
		qi, err1 := strconv.Atoi(parts[1])
		oi, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			b.answerCallback(q.ID, "")
			return
		}
		toast, ok = qa.pick(qi, oi)
	default:
		b.answerCallback(q.ID, "")
		return
	}
	if !ok {
		b.answerCallback(q.ID, b.t(chatID, "cb.stale"))
		return
	}
	b.answerCallback(q.ID, toast)
}
