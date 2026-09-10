// Nhận biết bản mới trên GitHub Releases.
//
// Binary phát hành được nhúng số version lúc build (release.sh dùng
// -ldflags "-X main.version=v0.1.0"). Bot hỏi GitHub xem release mới nhất là
// tag nào, so với số đó, và nhắc **một lần cho mỗi version** vào chat riêng của
// những người trong allowed_user_ids.
//
// Bot không tự cài bản mới: nó chạy dưới user thường, mà cài thì cần sudo —
// sudo qua bot sẽ treo ở chỗ hỏi mật khẩu. Nên ở đây chỉ báo + đưa đúng lệnh
// cần chạy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// version: gán lúc build bằng -ldflags "-X main.version=v0.1.0".
// Giữ nguyên devVersion = binary tự build, không có mốc để so -> không nhắc.
var version = devVersion

const (
	devVersion         = "dev"
	defaultUpdateRepo  = "thehao1505/telegram-terminal"
	defaultGitHubAPI   = "https://api.github.com"
	updateFirstDelay   = 60 * time.Second // để lúc khởi động lo việc chính trước
	updateCheckTimeout = 15 * time.Second
)

// versionLine: bản đang chạy, dạng ngắn cho log và cho `-version`.
func versionLine() string {
	return fmt.Sprintf("telegram-terminal %s %s/%s", version, runtime.GOOS, runtime.GOARCH)
}

// updateRepo: repo để hỏi release, dạng "owner/name".
func (c *Config) updateRepo() string {
	if s := strings.TrimSpace(c.UpdateRepo); s != "" {
		return s
	}
	return defaultUpdateRepo
}

// updateCheckEvery: nhịp kiểm tra bản mới. 0 = mặc định 24h, số âm = tắt hẳn.
func (c *Config) updateCheckEvery() time.Duration {
	switch {
	case c.UpdateCheckHours < 0:
		return 0
	case c.UpdateCheckHours == 0:
		return 24 * time.Hour
	default:
		return time.Duration(c.UpdateCheckHours) * time.Hour
	}
}

// release: phần cần dùng trong một release của GitHub.
type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// latestRelease hỏi GitHub release mới nhất của repo.
// (nil, nil) = repo chưa phát hành bản nào (HTTP 404).
func latestRelease(ctx context.Context, client *http.Client, apiBase, repo string) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(apiBase, "/")+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "telegram-terminal/"+version) // GitHub bắt buộc có UA

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}

	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.TagName) == "" {
		return nil, errors.New("github: release without a tag")
	}
	return &r, nil
}

// parseVersion đọc dạng v?X[.Y[.Z]][-pre][+build]. ok=false nếu không phải số
// version (VD "dev", hay tag chữ) — lúc đó không so sánh được và bot im lặng.
func parseVersion(s string) (nums [3]int, pre string, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i] // build metadata không tham gia so sánh
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre, s = s[i+1:], s[:i]
	}
	parts := strings.Split(s, ".")
	if s == "" || len(parts) > 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// compareVersions: -1 nếu a < b, 0 nếu bằng, 1 nếu a > b.
// ok=false nếu có bên không đọc được.
func compareVersions(a, b string) (int, bool) {
	na, pa, oka := parseVersion(a)
	nb, pb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range na {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	// Cùng X.Y.Z: bản chính thức > bản pre-release (v1.0.0 > v1.0.0-rc1).
	switch {
	case pa == pb:
		return 0, true
	case pa == "":
		return 1, true
	case pb == "":
		return -1, true
	case pa < pb:
		return -1, true
	default:
		return 1, true
	}
}

// updateState: kết quả lần kiểm tra gần nhất. /status đọc ở đây nên không phải
// gọi mạng, và notified giữ cho mỗi version chỉ nhắc một lần.
type updateState struct {
	mu       sync.Mutex
	latest   *release // GitHub trả về (nil = chưa biết / repo chưa có release)
	newer    bool     // latest mới hơn bản đang chạy
	err      error
	notified string // tag đã nhắc
}

// checkUpdate gọi GitHub một lần, ghi lại kết quả, trả về release mới hơn bản
// đang chạy (nil = không có bản mới, hoặc không so sánh được).
func (b *Bot) checkUpdate(ctx context.Context) (*release, error) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	rel, err := latestRelease(ctx, b.sendClient, b.ghBase, b.cfg.updateRepo())

	b.upd.mu.Lock()
	defer b.upd.mu.Unlock()
	b.upd.err, b.upd.latest, b.upd.newer = err, rel, false
	if err != nil || rel == nil {
		return nil, err
	}
	if cmp, ok := compareVersions(rel.TagName, version); ok && cmp > 0 {
		b.upd.newer = true
		return rel, nil
	}
	return nil, nil
}

// updateAvailable: bản mới đã biết từ lần kiểm tra trước (không gọi mạng).
func (b *Bot) updateAvailable() *release {
	b.upd.mu.Lock()
	defer b.upd.mu.Unlock()
	if b.upd.newer {
		return b.upd.latest
	}
	return nil
}

// watchUpdates chạy nền: kiểm tra sau updateFirstDelay rồi lặp theo
// update_check_hours. Tắt khi config tắt, hoặc khi binary là bản tự build.
func (b *Bot) watchUpdates(ctx context.Context) {
	every := b.cfg.updateCheckEvery()
	switch {
	case every <= 0:
		log.Println("update check: off (update_check_hours < 0)")
		return
	case version == devVersion:
		log.Println("update check: off (self-built binary has no version to compare; /version still checks by hand)")
		return
	}
	log.Printf("update check: %s every %s", b.cfg.updateRepo(), every)

	b.upd.mu.Lock()
	b.upd.notified = loadNotifiedTag() // restart service không nhắc lại tag cũ
	b.upd.mu.Unlock()

	t := time.NewTimer(updateFirstDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if rel, err := b.checkUpdate(ctx); err != nil {
			log.Printf("update check: %v", err)
		} else if rel != nil {
			b.announceUpdate(rel)
		}
		t.Reset(every)
	}
}

// announceUpdate nhắn cho từng người trong allowlist, một lần cho mỗi tag.
func (b *Bot) announceUpdate(rel *release) {
	b.upd.mu.Lock()
	seen := b.upd.notified == rel.TagName
	b.upd.notified = rel.TagName
	b.upd.mu.Unlock()
	if seen {
		return
	}
	saveNotifiedTag(rel.TagName)
	log.Printf("update available: %s -> %s", version, rel.TagName)
	// Chat riêng với bot có chat_id = user_id. Ai chưa từng /start thì Telegram
	// trả 403 và apiCall log lại — không ảnh hưởng người còn lại.
	for _, id := range b.cfg.AllowedUserIDs {
		b.send(id, b.updateAlert(id, rel), true)
	}
}

// updateAlert: nội dung tin nhắn nhắc cập nhật.
func (b *Bot) updateAlert(chatID int64, rel *release) string {
	return b.t(chatID, "update.alert", htmlEscape(rel.TagName), htmlEscape(version), htmlEscape(rel.HTMLURL)) +
		b.t(chatID, "update.howto", htmlEscape(updateCommands(b.cfg.updateRepo())))
}

// updateCommands: đúng ba lệnh để lên bản mới, cho kiến trúc của máy này.
func updateCommands(repo string) string {
	asset := "tt-" + runtime.GOARCH + ".tar.gz"
	return fmt.Sprintf("curl -fsSLO https://github.com/%s/releases/latest/download/%s\ntar xzf %s\nsudo ./tt-%s/install.sh",
		repo, asset, asset, runtime.GOARCH)
}

// versionText: nội dung lệnh /version — kiểm tra GitHub ngay tại đây, kể cả khi
// việc kiểm tra định kỳ đang tắt (người dùng vừa hỏi thẳng).
func (b *Bot) versionText(chatID int64) string {
	var sb strings.Builder
	sb.WriteString(b.t(chatID, "ver.line", htmlEscape(version), runtime.GOOS+"/"+runtime.GOARCH))

	rel, err := b.checkUpdate(context.Background())
	b.upd.mu.Lock()
	latest := b.upd.latest
	b.upd.mu.Unlock()

	switch {
	case err != nil:
		sb.WriteString(b.t(chatID, "ver.check.failed", htmlEscape(err.Error())))
	case latest == nil:
		sb.WriteString(b.t(chatID, "ver.no.release", htmlEscape(b.cfg.updateRepo())))
	case rel != nil:
		sb.WriteString(b.t(chatID, "ver.newer", htmlEscape(rel.TagName), htmlEscape(rel.HTMLURL)))
		sb.WriteString(b.t(chatID, "update.howto", htmlEscape(updateCommands(b.cfg.updateRepo()))))
	case version == devVersion:
		sb.WriteString(b.t(chatID, "ver.dev", htmlEscape(latest.TagName), htmlEscape(latest.HTMLURL)))
	default:
		sb.WriteString(b.t(chatID, "ver.uptodate", htmlEscape(latest.TagName)))
	}
	return sb.String()
}

// notifiedTagPath: nơi ghi tag đã nhắc. Nằm trong cache của user chạy bot vì
// /etc/telegram-terminal thuộc root, service lại chạy user thường.
// "" = không xác định được HOME -> bỏ qua việc ghi nhớ.
func notifiedTagPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "telegram-terminal", "update-notified")
}

func loadNotifiedTag() string {
	p := notifiedTagPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveNotifiedTag(tag string) {
	p := notifiedTagPath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		log.Printf("update check: cannot remember the announced tag: %v", err)
		return
	}
	if err := os.WriteFile(p, []byte(tag+"\n"), 0o600); err != nil {
		log.Printf("update check: cannot remember the announced tag: %v", err)
	}
}
