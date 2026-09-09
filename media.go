// Nhận file đính kèm từ Telegram (ảnh, sticker, document) và đưa vào lượt
// Claude.
//
// Luồng: getFile(file_id) -> tải về từ .../file/bot<token>/<file_path> -> lưu
// ra đĩa -> nếu là ảnh và đủ nhỏ thì nhúng thẳng vào prompt dưới dạng block
// base64 (Claude thấy ngay, không cần xin quyền tool). Ảnh quá lớn / file
// không phải ảnh thì chỉ đưa đường dẫn để Claude tự Read.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	albumWait      = 1400 * time.Millisecond // chờ gom các ảnh gửi cùng một album
	maxAttachBytes = 20 << 20                // Bot API không cho tải file lớn hơn mức này
)

// attachment: một file đã tải về đĩa. inline=true -> sẽ nhúng base64 vào prompt.
type attachment struct {
	path      string
	name      string
	mediaType string // "image/png"… rỗng nếu không nhận ra là ảnh
	size      int
	data      []byte // chỉ giữ lại khi inline
	inline    bool
}

// prompt: một lượt gửi cho Claude — chữ, kèm file đính kèm (nếu có).
type prompt struct {
	text string
	atts []attachment
}

// textPrompt: lượt chỉ có chữ (đường cũ, không đính kèm gì).
func textPrompt(s string) prompt { return prompt{text: s} }

// claudeContent dựng field "content" của tin nhắn user gửi cho Claude: string
// thuần khi không có ảnh nhúng, mảng content block khi có.
func (p prompt) claudeContent() any {
	var inline []attachment
	for _, a := range p.atts {
		if a.inline {
			inline = append(inline, a)
		}
	}
	if len(inline) == 0 {
		return p.textBlock()
	}
	blocks := []any{map[string]any{"type": "text", "text": p.textBlock()}}
	for _, a := range inline {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": a.mediaType,
				"data":       base64.StdEncoding.EncodeToString(a.data),
			},
		})
	}
	return blocks
}

// textBlock: chữ của người dùng, kèm danh sách file đính kèm và đường dẫn thật
// trên máy (để Claude đọc lại, crop, hay so sánh về sau).
func (p prompt) textBlock() string {
	if len(p.atts) == 0 {
		return p.text
	}
	var lines []string
	for _, a := range p.atts {
		switch {
		case a.inline:
			lines = append(lines, fmt.Sprintf("- %s — đã nhúng ở trên, bản gốc: %s", a.name, a.path))
		case a.mediaType != "":
			lines = append(lines, fmt.Sprintf("- %s — ảnh quá lớn để nhúng, dùng tool Read để xem: %s", a.name, a.path))
		default:
			lines = append(lines, fmt.Sprintf("- %s — %s", a.name, a.path))
		}
	}
	return strings.TrimSpace(p.text) + "\n\nNgười dùng gửi kèm qua Telegram:\n" + strings.Join(lines, "\n")
}

// ---------------------------- Tải file về ------------------------------

// getFile hỏi Telegram đường dẫn tải của một file_id.
func (b *Bot) getFile(fileID string) (string, error) {
	v := url.Values{}
	v.Set("file_id", fileID)
	resp, err := b.sendClient.PostForm(b.api("getFile"), v)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if !r.OK || r.Result.FilePath == "" {
		if r.Description == "" {
			r.Description = "getFile không trả về file_path"
		}
		return "", fmt.Errorf("%s", r.Description)
	}
	return r.Result.FilePath, nil
}

// downloadFile tải nội dung file theo file_path mà getFile trả về.
func (b *Bot) downloadFile(remotePath string) ([]byte, error) {
	resp, err := b.sendClient.Get(b.fileBase + remotePath)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tải file: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachBytes {
		return nil, fmt.Errorf("file lớn hơn %s", humanSize(maxAttachBytes))
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("file rỗng")
	}
	return data, nil
}

// fetchAttachment tải 1 file của tin nhắn về đĩa và quyết định có nhúng được
// vào prompt hay không.
func (b *Bot) fetchAttachment(chatID, msgID int64, fileID, name string) (attachment, error) {
	remote, err := b.getFile(fileID)
	if err != nil {
		return attachment{}, err
	}
	data, err := b.downloadFile(remote)
	if err != nil {
		return attachment{}, err
	}
	if name == "" {
		name = filepath.Base(remote)
	}
	name = safeName(name)

	dir := filepath.Join(b.cfg.imageDir(), strconv.FormatInt(chatID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return attachment{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d-%s", time.Now().Format("20060102-150405"), msgID, name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return attachment{}, err
	}

	a := attachment{path: path, name: name, mediaType: imageMediaType(data), size: len(data)}
	if a.mediaType != "" && len(data) <= b.cfg.imageMaxBytes() {
		a.inline, a.data = true, data
	}
	return a, nil
}

// inlineImageTypes: đúng những định dạng ảnh mà API Claude nhận.
var inlineImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// imageMediaType đoán media_type từ chính bytes, trả về "" nếu không phải ảnh
// nhúng được. Không tin mime_type mà Telegram khai: API Claude đòi media_type
// khớp đúng dữ liệu, khai sai thì cả lượt bị lỗi.
func imageMediaType(data []byte) string {
	t := http.DetectContentType(data)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	if t = strings.ToLower(strings.TrimSpace(t)); inlineImageTypes[t] {
		return t
	}
	return ""
}

// safeName giữ tên file gốc (kể cả chữ có dấu) nhưng bỏ mọi thứ có thể thoát ra
// khỏi thư mục lưu.
func safeName(s string) string {
	s = filepath.Base(strings.ReplaceAll(s, `\`, "/"))
	var sb strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	out := strings.Trim(sb.String(), "._")
	if out == "" {
		return "file"
	}
	if len(out) > 80 {
		out = out[len(out)-80:]
	}
	return out
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// photoRank: cỡ của một size ảnh, để chọn bản nét nhất.
func photoRank(p tgPhotoSize) int {
	if a := p.Width * p.Height; a > 0 {
		return a
	}
	return p.FileSize
}

// pickPhoto chọn bản lớn nhất còn vừa giới hạn nhúng. Telegram gửi kèm nhiều
// size nên thường không cần resize gì; nếu mọi size đều vượt thì lấy bản lớn
// nhất và để Claude tự Read (tool Read tự thu nhỏ ảnh).
func pickPhoto(sizes []tgPhotoSize, max int) tgPhotoSize {
	var best, biggest tgPhotoSize
	for _, s := range sizes {
		if s.FileID == "" {
			continue
		}
		if photoRank(s) > photoRank(biggest) {
			biggest = s
		}
		if s.FileSize > 0 && s.FileSize <= max && photoRank(s) > photoRank(best) {
			best = s
		}
	}
	if best.FileID != "" {
		return best
	}
	return biggest
}

// --------------------------- Xử lý tin nhắn ---------------------------

// attachRef chọn file cần tải trong tin nhắn: ảnh (bản vừa giới hạn nhúng),
// document, hoặc sticker. Trả về file_id rỗng nếu không có gì để tải.
func (m *tgMessage) attachRef(max int) (fileID, name string) {
	switch {
	case len(m.Photo) > 0:
		// Ảnh không có tên; lấy theo file_path Telegram trả về.
		return pickPhoto(m.Photo, max).FileID, ""
	case m.Document != nil:
		return m.Document.FileID, m.Document.FileName
	case m.Sticker != nil:
		return m.Sticker.FileID, ""
	}
	return "", ""
}

// unsupportedMedia trả về tên loại media chưa hỗ trợ (rỗng nếu không có).
func (m *tgMessage) unsupportedMedia() string {
	switch {
	case m.Video != nil:
		return "video"
	case m.Animation != nil:
		return "GIF động"
	case m.Voice != nil:
		return "tin nhắn thoại"
	case m.Audio != nil:
		return "audio"
	}
	return ""
}

// hasMedia: tin nhắn có đính kèm gì đó (kể cả loại chưa hỗ trợ, để còn báo lại).
func (m *tgMessage) hasMedia() bool {
	id, _ := m.attachRef(0)
	return id != "" || m.unsupportedMedia() != ""
}

// handleMedia xử lý tin nhắn có file đính kèm: tải về, lưu, rồi gửi cho Claude
// (chế độ claude) hoặc chỉ báo đường dẫn (chế độ shell).
func (b *Bot) handleMedia(chatID int64, sess *Session, msg *tgMessage, text string) {
	if msg.unsupportedMedia() != "" {
		b.send(chatID, "⚠️ Chưa hỗ trợ gửi "+msg.unsupportedMedia()+" cho Claude. Ảnh và file thì được.", false)
		return
	}
	b.typing(chatID)

	fileID, name := msg.attachRef(b.cfg.imageMaxBytes())
	if fileID == "" {
		return
	}
	a, err := b.fetchAttachment(chatID, msg.MessageID, fileID, name)
	if err != nil {
		b.send(chatID, "⚠️ không tải được file: "+htmlEscape(err.Error()), false)
		return
	}

	sess.mu.Lock()
	claudeMode := sess.claudeMode
	sess.mu.Unlock()
	if !claudeMode || !b.cfg.ClaudeEnabled {
		b.send(chatID, fmt.Sprintf("📎 Đã lưu <code>%s</code> (%s)\nĐang ở chế độ shell nên chưa gửi cho Claude — /c rồi gửi lại là Claude xem được ảnh.",
			htmlEscape(a.path), humanSize(a.size)), true)
		return
	}

	// Album (nhiều ảnh gửi một lần) về thành nhiều update riêng lẻ: gom lại rồi
	// chạy MỘT lượt, nếu không ảnh thứ hai sẽ bị execGuarded chặn vì đang bận.
	if msg.MediaGroupID != "" {
		b.queueAlbum(chatID, sess, msg.MediaGroupID, text, a)
		return
	}

	if !a.inline {
		b.send(chatID, b.attachNote(a), false)
	}
	b.execGuarded(chatID, sess, func(ctx context.Context) {
		b.runClaude(ctx, chatID, sess, prompt{text: b.mediaText(text), atts: []attachment{a}})
	})
}

// attachNote: nhắc khi file không được nhúng thẳng vào lượt.
func (b *Bot) attachNote(a attachment) string {
	if a.mediaType != "" {
		return fmt.Sprintf("📎 %s (%s) lớn hơn giới hạn nhúng %s — Claude sẽ tự đọc file bằng tool Read.",
			a.name, humanSize(a.size), humanSize(b.cfg.imageMaxBytes()))
	}
	return fmt.Sprintf("📎 %s (%s) không phải ảnh — đã lưu, Claude sẽ tự đọc file nếu cần.",
		a.name, humanSize(a.size))
}

// mediaText: caption của người dùng, hoặc prompt mặc định nếu gửi ảnh trơn.
func (b *Bot) mediaText(text string) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	return b.cfg.imageDefaultPrompt()
}

// albumBuf: các ảnh cùng một media_group_id đang chờ gom.
type albumBuf struct {
	timer *time.Timer
	text  string
	atts  []attachment
}

func (b *Bot) queueAlbum(chatID int64, sess *Session, groupID, text string, a attachment) {
	b.amu.Lock()
	if b.albums == nil {
		b.albums = map[string]*albumBuf{}
	}
	buf := b.albums[groupID]
	if buf == nil {
		buf = &albumBuf{}
		b.albums[groupID] = buf
		buf.timer = time.AfterFunc(albumWait, func() { b.flushAlbum(chatID, sess, groupID) })
	}
	buf.atts = append(buf.atts, a)
	if buf.text == "" {
		buf.text = text // caption chỉ đi kèm một ảnh trong album
	}
	buf.timer.Reset(albumWait)
	b.amu.Unlock()
}

// flushAlbum chạy một lượt Claude cho cả album vừa gom được.
func (b *Bot) flushAlbum(chatID int64, sess *Session, groupID string) {
	b.amu.Lock()
	buf := b.albums[groupID]
	delete(b.albums, groupID)
	b.amu.Unlock()
	if buf == nil || len(buf.atts) == 0 {
		return
	}
	for _, a := range buf.atts {
		if !a.inline {
			b.send(chatID, b.attachNote(a), false)
		}
	}
	b.execGuarded(chatID, sess, func(ctx context.Context) {
		b.runClaude(ctx, chatID, sess, prompt{text: b.mediaText(buf.text), atts: buf.atts})
	})
}

// cleanupAttachments xóa file đính kèm cũ lúc khởi động (0 giờ = giữ mãi).
func cleanupAttachments(dir string, keepHours int) {
	if keepHours <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(keepHours) * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, chatDir := range entries {
		if !chatDir.IsDir() {
			continue
		}
		sub := filepath.Join(dir, chatDir.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil || info.IsDir() || info.ModTime().After(cutoff) {
				continue
			}
			os.Remove(filepath.Join(sub, f.Name()))
		}
	}
}
