// Liệt kê & mở lại (resume) các phiên Claude Code đã lưu trên máy.
//
// Claude Code lưu mỗi phiên thành 1 file JSONL:
//
//	~/.claude/projects/<slug thư mục>/<session-id>.jsonl
//
// trong đó <slug thư mục> là đường dẫn cwd với mọi ký tự không phải chữ/số đổi
// thành "-" (VD /tmp/tt.slug_test-01 -> -tmp-tt-slug-test-01).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sessionListMax = 12 // số phiên cũ hiển thị trong /sessions

type sessionInfo struct {
	ID    string
	Path  string
	MTime time.Time
	First string // prompt đầu tiên, dùng làm nhãn
	Cwd   string
}

// projectsDir: nơi Claude Code lưu phiên.
func projectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// projectSlug đổi cwd thành tên thư mục lưu phiên.
func projectSlug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// listSessions trả về các phiên đã lưu cho cwd, mới nhất trước, tối đa max.
func listSessions(root, cwd string, max int) []sessionInfo {
	if root == "" {
		return nil
	}
	dir := filepath.Join(root, projectSlug(cwd))
	if _, err := os.Stat(dir); err != nil {
		// Slug không khớp (đường dẫn lạ) -> dò theo cwd ghi trong file phiên.
		alt := findProjectDir(root, cwd)
		if alt == "" {
			return nil
		}
		dir = alt
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []sessionInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, sessionInfo{
			ID:    strings.TrimSuffix(e.Name(), ".jsonl"),
			Path:  filepath.Join(dir, e.Name()),
			MTime: fi.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MTime.After(out[j].MTime) })
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	// Chỉ đọc nội dung của những phiên thực sự hiển thị.
	for i := range out {
		out[i].First, out[i].Cwd = peekSession(out[i].Path)
	}
	return out
}

// findProjectDir dò thư mục project có phiên ghi đúng cwd này.
func findProjectDir(root, cwd string) string {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p := filepath.Join(root, d.Name())
		files, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			if _, c := peekSession(filepath.Join(p, f.Name())); c == cwd {
				return p
			}
			break // 1 file/thư mục là đủ để biết cwd
		}
	}
	return ""
}

// peekSession đọc phần đầu file phiên để lấy prompt đầu tiên và cwd.
func peekSession(path string) (first, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < 200; i++ {
		line, rerr := readJSONLine(br)
		if len(line) > 0 {
			var rec struct {
				Type    string `json:"type"`
				Cwd     string `json:"cwd"`
				Content string `json:"content"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &rec) == nil {
				if cwd == "" && rec.Cwd != "" {
					cwd = rec.Cwd
				}
				if first == "" && rec.Type == "user" && rec.Message.Role == "user" {
					first = oneLine(contentText(rec.Message.Content))
				}
				if first == "" && rec.Type == "queue-operation" {
					first = oneLine(rec.Content)
				}
				if first != "" && cwd != "" {
					return first, cwd
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	return first, cwd
}

// oneLine gộp về 1 dòng, bỏ các thẻ <...> mà Claude Code chèn vào prompt
// (VD <local-command-caveat>, <command-name>) cho nhãn dễ đọc.
func oneLine(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		if strings.ContainsAny(s[i+1:i+j], " \n") { // có khoảng trắng -> không phải thẻ
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		b.WriteString(s[:i])
		s = s[i+j+1:]
	}
	b.WriteString(s)
	return truncStr(strings.Join(strings.Fields(b.String()), " "), 60)
}

// shortTime: giờ nếu là hôm nay, thêm ngày nếu cũ hơn.
func shortTime(t time.Time) string {
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("02-01 15:04")
}

// --------------------------- Lệnh Telegram ----------------------------

// sessionsText dựng nội dung cho /session: danh sách phiên đã lưu ở thư mục
// hiện tại, phiên đang mở được đánh dấu ngay trong danh sách (trạng thái đầy đủ
// xem ở /status).
func (b *Bot) sessionsText(chatID int64, sess *Session) string {
	sess.mu.Lock()
	cwd, cs := sess.cwd, sess.claude
	sess.mu.Unlock()

	curID, curCwd := "", ""
	if cs != nil && cs.alive() {
		cs.mu.Lock()
		curID = cs.sessID
		cs.mu.Unlock()
		curCwd = cs.cwd
	}

	l := b.lang(chatID)
	var sb strings.Builder
	sb.WriteString(l.t("sess.head", htmlEscape(cwd)))

	list := listSessions(projectsDir(), cwd, sessionListMax)
	if len(list) == 0 {
		sb.WriteString(l.t("sess.none"))
	}
	inList := false
	for i, s := range list {
		label := s.First
		if label == "" {
			label = l.t("sess.unknown")
		}
		mark := ""
		if s.ID == curID {
			mark, inList = l.t("sess.open.mark"), true
		}
		fmt.Fprintf(&sb, "%d. <code>%s</code> · %s · %s%s\n",
			i+1, htmlEscape(shortID(s.ID)), shortTime(s.MTime), htmlEscape(label), mark)
	}
	if curID != "" && !inList {
		sb.WriteString(l.t("sess.open.other", htmlEscape(shortID(curID)), htmlEscape(curCwd)))
	}
	if len(list) > 0 {
		sb.WriteString(l.t("sess.hint"))
	}
	return sb.String()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// resolveSession đổi tham số của /resume (số thứ tự hoặc id/prefix id) thành
// session id đầy đủ.
func resolveSession(l *language, list []sessionInfo, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("%s", l.t("sess.err.missing"))
	}
	// Số thứ tự chỉ có 1-2 chữ số (danh sách tối đa sessionListMax); dài hơn
	// thì đó là id — id là hex nên có thể toàn chữ số, VD "33333333".
	if n, err := strconv.Atoi(arg); err == nil && len(arg) <= 2 {
		if n < 1 || n > len(list) {
			return "", fmt.Errorf("%s", l.t("sess.err.number", n, len(list)))
		}
		return list[n-1].ID, nil
	}
	var hits []string
	for _, s := range list {
		if strings.HasPrefix(s.ID, arg) {
			hits = append(hits, s.ID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		// Không có trong danh sách nhưng vẫn cho thử nếu trông như UUID.
		if len(arg) >= 8 {
			return arg, nil
		}
		return "", fmt.Errorf("%s", l.t("sess.err.not.found", arg))
	default:
		return "", fmt.Errorf("%s", l.t("sess.err.ambiguous", arg, len(hits)))
	}
}

// sessionCmd gom mọi việc về phiên vào một lệnh:
//
//	/session            liệt kê phiên đã lưu ở thư mục hiện tại
//	/session <số|id>    mở lại một phiên
//	/session new        đóng phiên hiện tại, mở phiên mới
//
// /sessions, /ss, /newchat, /resume, /r là tên gọi cũ, quy về đúng nhánh trên.
func (b *Bot) sessionCmd(chatID int64, sess *Session, cmd, arg string) {
	switch {
	case cmd == "/newchat", strings.EqualFold(arg, "new"), strings.EqualFold(arg, "mới"):
		b.newSession(chatID, sess)
	case arg != "":
		b.resumeSession(chatID, sess, arg)
	case cmd == "/resume", cmd == "/r":
		b.send(chatID, b.t(chatID, "sess.usage"), true)
	default:
		b.send(chatID, b.sessionsText(chatID, sess), true)
	}
}

// newSession đóng phiên đang mở; phiên mới sẽ được mở ở prompt kế tiếp.
func (b *Bot) newSession(chatID int64, sess *Session) {
	had := b.stopClaude(sess)
	sess.mu.Lock()
	cwd := sess.cwd
	sess.mu.Unlock()
	msg := b.t(chatID, "sess.new", cwd)
	if had {
		msg = b.t(chatID, "sess.new.closed") + msg
	}
	b.send(chatID, msg, false)
}

// resumeSession đóng phiên hiện tại và mở lại phiên theo id.
func (b *Bot) resumeSession(chatID int64, sess *Session, arg string) {
	sess.mu.Lock()
	cwd, running := sess.cwd, sess.running
	sess.mu.Unlock()
	if running {
		b.send(chatID, b.t(chatID, "sess.busy"), false)
		return
	}

	l := b.lang(chatID)
	id, err := resolveSession(l, listSessions(projectsDir(), cwd, sessionListMax), arg)
	if err != nil {
		b.send(chatID, l.t("sess.resolve.error", err.Error()), false)
		return
	}

	mode := b.permModeFor(sess)
	b.stopClaude(sess)
	cs, err := b.startClaude(chatID, cwd, id, mode)
	if err != nil {
		b.send(chatID, l.t("sess.resume.error", err.Error()), false)
		return
	}
	sess.mu.Lock()
	sess.claude = cs
	sess.claudeMode = true
	sess.mu.Unlock()

	b.send(chatID, l.t("sess.resumed", htmlEscape(shortID(id)), htmlEscape(cwd),
		htmlEscape(l.t("session.permission", cs.curPermMode()))), true)
}
