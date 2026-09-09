package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pngBytes dựng 1 ảnh PNG đặc màu để làm file đính kèm giả.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0, 0, 255, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// photoUpdate dựng 1 Update ảnh (đi qua đúng đường giải mã JSON của Telegram).
// Ảnh có 2 size: bản nhỏ "thumb" và bản lớn fileID.
func photoUpdate(t *testing.T, msgID int64, caption, groupID, fileID string, size int) Update {
	t.Helper()
	raw := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":7},"chat":{"id":1},
		"caption":%q,"media_group_id":%q,"photo":[
		{"file_id":"thumb","width":90,"height":60,"file_size":600},
		{"file_id":%q,"width":1280,"height":800,"file_size":%d}]}}`,
		msgID, msgID, caption, groupID, fileID, size)
	var u Update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

// rawUpdate giải mã 1 update viết tay (dùng cho document / voice…).
func rawUpdate(t *testing.T, body string) Update {
	t.Helper()
	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":9,"from":{"id":7},"chat":{"id":1},`+body+`}}`), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

// mediaBot: bot nối vào tgMock + fakeclaude, lưu file vào thư mục tạm. Trả về
// cả đường dẫn file mà fakeclaude ghi lại message nhận được.
func mediaBot(t *testing.T, tg *tgMock, cfg Config) (*Bot, string) {
	t.Helper()
	msgFile := filepath.Join(t.TempDir(), "message.json")
	t.Setenv("TT_FAKE_MESSAGE", msgFile)
	t.Setenv("TT_FAKE_NOASK", "1")

	cfg.ClaudeEnabled = true
	cfg.ClaudeBin = buildFakeClaude(t)
	cfg.StartDir = t.TempDir()
	cfg.ImageDir = t.TempDir()
	return tg.bot(cfg), msgFile
}

// claudeMessage đọc message mà fakeclaude nhận được.
func claudeMessage(t *testing.T, path string) (text string, images []map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fakeclaude không nhận được message nào: %v", err)
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// content có thể là string thuần (không ảnh) hoặc mảng content block.
	if json.Unmarshal(m.Content, &text) == nil {
		return text, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		t.Fatalf("content lạ: %s", m.Content)
	}
	for _, blk := range blocks {
		switch blk["type"] {
		case "text":
			text, _ = blk["text"].(string)
		case "image":
			images = append(images, blk)
		}
	}
	return text, images
}

func imageSource(t *testing.T, blk map[string]any) (mediaType string, data []byte) {
	t.Helper()
	src, ok := blk["source"].(map[string]any)
	if !ok {
		t.Fatalf("block ảnh không có source: %v", blk)
	}
	mediaType, _ = src["media_type"].(string)
	b64, _ := src["data"].(string)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("data không phải base64: %v", err)
	}
	return mediaType, data
}

// ------------------------------ Unit test -----------------------------

func TestClaudeContentPlainTextStaysString(t *testing.T) {
	c := textPrompt("chào bạn").claudeContent()
	if s, ok := c.(string); !ok || s != "chào bạn" {
		t.Fatalf("prompt không ảnh phải là string thuần, nhận %#v", c)
	}
}

func TestClaudeContentInlineImage(t *testing.T) {
	p := prompt{text: "ảnh này là gì?", atts: []attachment{
		{path: "/tmp/x/a.png", name: "a.png", mediaType: "image/png", data: []byte("PNGDATA"), inline: true},
	}}
	blocks, ok := p.claudeContent().([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("muốn 2 content block, nhận %#v", p.claudeContent())
	}
	text := blocks[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "ảnh này là gì?") || !strings.Contains(text, "/tmp/x/a.png") {
		t.Errorf("text block thiếu caption hoặc đường dẫn: %q", text)
	}
	img := blocks[1].(map[string]any)
	if img["type"] != "image" {
		t.Fatalf("block 2 không phải ảnh: %#v", img)
	}
	mt, data := imageSource(t, img)
	if mt != "image/png" || string(data) != "PNGDATA" {
		t.Errorf("source sai: %q / %q", mt, data)
	}
}

// Ảnh quá lớn (inline=false) -> content vẫn là string, nhưng có đường dẫn để
// Claude tự Read.
func TestClaudeContentOversizeFallsBackToPath(t *testing.T) {
	p := prompt{text: "xem đi", atts: []attachment{
		{path: "/tmp/big.jpg", name: "big.jpg", mediaType: "image/jpeg", size: 9 << 20},
	}}
	s, ok := p.claudeContent().(string)
	if !ok {
		t.Fatalf("muốn string thuần, nhận %#v", p.claudeContent())
	}
	if !strings.Contains(s, "/tmp/big.jpg") || !strings.Contains(s, "Read") {
		t.Errorf("thiếu hướng dẫn đọc file: %q", s)
	}
}

func TestImageMediaType(t *testing.T) {
	if got := imageMediaType(pngBytes(t, 8, 8)); got != "image/png" {
		t.Errorf("PNG -> %q", got)
	}
	if got := imageMediaType([]byte("chỉ là chữ thôi")); got != "" {
		t.Errorf("text khai là ảnh vẫn phải bị loại, nhận %q", got)
	}
	if got := imageMediaType([]byte("%PDF-1.4\n...")); got != "" {
		t.Errorf("PDF -> %q", got)
	}
}

func TestPickPhoto(t *testing.T) {
	sizes := []tgPhotoSize{
		{FileID: "s", Width: 90, Height: 60, FileSize: 600},
		{FileID: "m", Width: 800, Height: 600, FileSize: 100_000},
		{FileID: "l", Width: 2000, Height: 1500, FileSize: 5_000_000},
	}
	if got := pickPhoto(sizes, 1_000_000).FileID; got != "m" {
		t.Errorf("muốn bản lớn nhất còn vừa giới hạn (m), nhận %q", got)
	}
	if got := pickPhoto(sizes, 10_000_000).FileID; got != "l" {
		t.Errorf("muốn bản nét nhất (l), nhận %q", got)
	}
	// Mọi size đều vượt -> lấy bản lớn nhất, để Claude tự Read.
	if got := pickPhoto(sizes, 100).FileID; got != "l" {
		t.Errorf("muốn l khi mọi bản đều quá lớn, nhận %q", got)
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":  "passwd",
		"/abs/path/ảnh.png": "ảnh.png",
		"ok_file-1.PNG":     "ok_file-1.PNG",
		"":                  "file",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, muốn %q", in, got, want)
		}
	}
}

func TestCleanupAttachments(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "1")
	os.MkdirAll(sub, 0o700)
	old := filepath.Join(sub, "old.png")
	fresh := filepath.Join(sub, "fresh.png")
	os.WriteFile(old, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	cleanupAttachments(dir, 24)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("file cũ chưa bị xóa")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("file mới bị xóa oan")
	}

	// keepHours = 0 -> giữ mãi.
	os.Chtimes(fresh, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	cleanupAttachments(dir, 0)
	if _, err := os.Stat(fresh); err != nil {
		t.Error("keepHours=0 phải giữ nguyên file")
	}
}

// --------------------------- Test tích hợp ----------------------------

// TestMediaPhotoToClaude: gửi ảnh kèm caption trong chế độ Claude -> ảnh được
// nhúng base64 vào lượt, file gốc được lưu ra đĩa.
func TestMediaPhotoToClaude(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	img := pngBytes(t, 64, 64)
	tg.fileData = img

	b, msgFile := mediaBot(t, tg, Config{})
	sess := b.session(1)
	sess.claudeMode = true
	defer b.stopClaude(sess)

	b.handle(photoUpdate(t, 5, "ảnh này màu gì?", "", "big", len(img)))

	text, images := claudeMessage(t, msgFile)
	if len(images) != 1 {
		t.Fatalf("muốn 1 block ảnh, nhận %d (text=%q)", len(images), text)
	}
	mt, data := imageSource(t, images[0])
	if mt != "image/png" {
		t.Errorf("media_type = %q", mt)
	}
	if !bytes.Equal(data, img) {
		t.Errorf("bytes ảnh gửi đi khác bytes tải về (%d vs %d)", len(data), len(img))
	}
	if !strings.Contains(text, "ảnh này màu gì?") {
		t.Errorf("caption không thành prompt: %q", text)
	}

	// Bot phải chọn bản nét nhất còn vừa giới hạn, không phải thumbnail.
	tg.mu.Lock()
	ids := append([]string(nil), tg.fileIDs...)
	tg.mu.Unlock()
	if len(ids) != 1 || ids[0] != "big" {
		t.Errorf("getFile gọi với %v, muốn [big]", ids)
	}

	// File gốc còn trên đĩa để Claude đọc lại về sau.
	files, _ := filepath.Glob(filepath.Join(b.cfg.imageDir(), "1", "*"))
	if len(files) != 1 {
		t.Fatalf("muốn 1 file được lưu, nhận %v", files)
	}
	if !strings.Contains(text, files[0]) {
		t.Errorf("prompt không nêu đường dẫn %q: %q", files[0], text)
	}
}

// Không caption -> dùng prompt mặc định.
func TestMediaPhotoNoCaptionUsesDefaultPrompt(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	tg.fileData = pngBytes(t, 16, 16)

	b, msgFile := mediaBot(t, tg, Config{ImageDefaultPrompt: "Mô tả ảnh giúp tôi."})
	sess := b.session(1)
	sess.claudeMode = true
	defer b.stopClaude(sess)

	b.handle(photoUpdate(t, 5, "", "", "big", 900))

	text, images := claudeMessage(t, msgFile)
	if len(images) != 1 {
		t.Fatalf("muốn 1 block ảnh, nhận %d", len(images))
	}
	if !strings.Contains(text, "Mô tả ảnh giúp tôi.") {
		t.Errorf("thiếu prompt mặc định: %q", text)
	}
}

// TestMediaAlbumOneTurn: 3 ảnh gửi cùng lúc (cùng media_group_id) phải gom
// thành MỘT lượt Claude, không bị "đang bận".
func TestMediaAlbumOneTurn(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	tg.fileData = pngBytes(t, 32, 32)

	b, msgFile := mediaBot(t, tg, Config{})
	sess := b.session(1)
	sess.claudeMode = true
	defer b.stopClaude(sess)

	for i, cap := range []string{"ba ảnh này khác gì nhau?", "", ""} {
		b.handle(photoUpdate(t, int64(10+i), cap, "grp-1", fmt.Sprintf("p%d", i), 900))
	}

	// Chờ hết cửa sổ gom album rồi tới lượt Claude chạy xong.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(msgFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	text, images := claudeMessage(t, msgFile)
	if len(images) != 3 {
		t.Fatalf("muốn 3 block ảnh trong 1 lượt, nhận %d", len(images))
	}
	if !strings.Contains(text, "ba ảnh này khác gì nhau?") {
		t.Errorf("caption của album bị mất: %q", text)
	}
	sent, _, _ := tg.all()
	if containsSub(sent, "Đang bận") {
		t.Errorf("ảnh trong album bị chặn vì đang bận: %v", sent)
	}
}

// Ảnh vượt giới hạn nhúng -> chỉ đưa đường dẫn, kèm lời nhắc cho người dùng.
func TestMediaOversizePhotoPathOnly(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	tg.fileData = pngBytes(t, 64, 64)

	b, msgFile := mediaBot(t, tg, Config{ImageMaxBytes: 10})
	sess := b.session(1)
	sess.claudeMode = true
	defer b.stopClaude(sess)

	b.handle(photoUpdate(t, 5, "xem ảnh", "", "big", 5_000_000))

	text, images := claudeMessage(t, msgFile)
	if len(images) != 0 {
		t.Fatalf("ảnh quá lớn thì không được nhúng, nhận %d block", len(images))
	}
	if !strings.Contains(text, "Read") {
		t.Errorf("prompt phải hướng Claude tự Read: %q", text)
	}
	sent, _, _ := tg.all()
	if !containsSub(sent, "giới hạn nhúng") {
		t.Errorf("chưa nhắc người dùng về giới hạn: %v", sent)
	}
}

// Chế độ shell: chỉ lưu file và báo đường dẫn, không gọi Claude.
func TestMediaInShellModeSavesOnly(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	tg.fileData = pngBytes(t, 16, 16)

	b, msgFile := mediaBot(t, tg, Config{})
	b.handle(photoUpdate(t, 5, "", "", "big", 900))

	if _, err := os.Stat(msgFile); err == nil {
		t.Error("chế độ shell không được gửi gì cho Claude")
	}
	sent, _, _ := tg.all()
	if !containsSub(sent, "Đã lưu") {
		t.Errorf("chưa báo đường dẫn file: %v", sent)
	}
	files, _ := filepath.Glob(filepath.Join(b.cfg.imageDir(), "1", "*"))
	if len(files) != 1 {
		t.Errorf("muốn 1 file được lưu, nhận %v", files)
	}
}

// File không phải ảnh: vẫn lưu, đưa đường dẫn, không nhúng.
func TestMediaDocumentPathOnly(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	tg.fileData = []byte("cột A,cột B\n1,2\n")

	b, msgFile := mediaBot(t, tg, Config{})
	sess := b.session(1)
	sess.claudeMode = true
	defer b.stopClaude(sess)

	b.handle(rawUpdate(t, `"caption":"đọc file này",
		"document":{"file_id":"doc1","file_name":"số liệu.csv","mime_type":"text/csv","file_size":16}`))

	text, images := claudeMessage(t, msgFile)
	if len(images) != 0 {
		t.Fatalf("file không phải ảnh thì không nhúng, nhận %d block", len(images))
	}
	if !strings.Contains(text, "đọc file này") || !strings.Contains(text, ".csv") {
		t.Errorf("prompt thiếu caption/tên file: %q", text)
	}
	files, _ := filepath.Glob(filepath.Join(b.cfg.imageDir(), "1", "*.csv"))
	if len(files) != 1 {
		t.Errorf("muốn file .csv được lưu, nhận %v", files)
	}
}

// TestRealImagePrompt chạy với `claude` thật (tốn API): ảnh nhúng base64 phải
// đi tới được model.
//
//	TT_E2E_CLAUDE=1 go test -run TestRealImagePrompt -v -timeout 10m
func TestRealImagePrompt(t *testing.T) {
	if os.Getenv("TT_E2E_CLAUDE") != "1" {
		t.Skip("đặt TT_E2E_CLAUDE=1 để chạy với claude thật")
	}
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{ClaudeEnabled: true, ClaudeBin: "claude", StartDir: t.TempDir()})
	sess := b.session(1)
	defer b.stopClaude(sess)

	cs, err := b.claudeFor(1, sess)
	if err != nil {
		t.Fatal(err)
	}
	img := pngBytes(t, 64, 64) // xanh dương thuần
	err = cs.sendPrompt(context.Background(), prompt{
		text: "Ảnh đính kèm màu gì? Trả lời đúng một từ, không dùng tool.",
		atts: []attachment{{
			path: filepath.Join(t.TempDir(), "blue.png"), name: "blue.png",
			mediaType: "image/png", data: img, size: len(img), inline: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, edits, _ := tg.all()
	all := strings.ToLower(strings.Join(append(sent, edits...), "\n"))
	if !strings.Contains(all, "xanh") && !strings.Contains(all, "blue") {
		t.Errorf("Claude không thấy được ảnh, trả lời: %q", all)
	}
}

// Loại media chưa hỗ trợ -> báo lại, không tải, không gọi Claude.
func TestMediaUnsupportedVoice(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()

	b, msgFile := mediaBot(t, tg, Config{})
	sess := b.session(1)
	sess.claudeMode = true

	b.handle(rawUpdate(t, `"voice":{"file_id":"v1","mime_type":"audio/ogg","file_size":100}`))

	if _, err := os.Stat(msgFile); err == nil {
		t.Error("không được gửi tin nhắn thoại cho Claude")
	}
	sent, _, _ := tg.all()
	if !containsSub(sent, "Chưa hỗ trợ") {
		t.Errorf("chưa báo loại media không hỗ trợ: %v", sent)
	}
	tg.mu.Lock()
	ids := tg.fileIDs
	tg.mu.Unlock()
	if len(ids) != 0 {
		t.Errorf("không nên tải file: %v", ids)
	}
}
