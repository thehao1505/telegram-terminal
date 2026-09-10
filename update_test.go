package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.2.0", "v0.1.0", 1, true},
		{"v0.1.0", "v0.2.0", -1, true},
		{"v0.1.0", "0.1.0", 0, true},   // "v" không bắt buộc
		{"v1.2", "v1.2.0", 0, true},    // thiếu số cuối = 0
		{"v1.10.0", "v1.9.0", 1, true}, // so số, không so chữ
		{"v2.0.0", "v1.99.99", 1, true},
		{"v1.0.0", "v1.0.0-rc1", 1, true}, // bản chính thức > pre-release
		{"v1.0.0-rc1", "v1.0.0-rc2", -1, true},
		{"v1.0.0+build9", "v1.0.0", 0, true}, // build metadata bị bỏ qua
		{"dev", "v1.0.0", 0, false},          // bản tự build: không so được
		{"v1.0.0", "dev", 0, false},
		{"nightly", "v1.0.0", 0, false},
		{"v1.0.0.1", "v1.0.0", 0, false}, // 4 số: không phải semver
		{"v", "v1.0.0", 0, false},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("compareVersions(%q, %q) = %d, %v; muốn %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestConfigUpdateCheckEvery(t *testing.T) {
	cases := []struct {
		hours int
		want  time.Duration
	}{
		{0, 24 * time.Hour}, // mặc định
		{6, 6 * time.Hour},
		{-1, 0}, // tắt
	}
	for _, c := range cases {
		if got := (&Config{UpdateCheckHours: c.hours}).updateCheckEvery(); got != c.want {
			t.Errorf("update_check_hours=%d -> %v; muốn %v", c.hours, got, c.want)
		}
	}
	if got := (&Config{}).updateRepo(); got != defaultUpdateRepo {
		t.Errorf("repo mặc định = %q; muốn %q", got, defaultUpdateRepo)
	}
	if got := (&Config{UpdateRepo: " me/tt "}).updateRepo(); got != "me/tt" {
		t.Errorf("repo = %q; muốn %q", got, "me/tt")
	}
}

// ghMock: GitHub API giả, trả về đúng một release (hoặc mã lỗi).
type ghMock struct {
	srv   *httptest.Server
	calls int
}

func newGHMock(t *testing.T, status int, body string) *ghMock {
	m := &ghMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls++
		if want := "/repos/me/tt/releases/latest"; r.URL.Path != want {
			t.Errorf("đường dẫn = %q; muốn %q", r.URL.Path, want)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("thiếu User-Agent — GitHub sẽ trả 403")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func TestLatestRelease(t *testing.T) {
	const body = `{"tag_name":"v0.2.0","html_url":"https://example.test/r/v0.2.0"}`

	gh := newGHMock(t, http.StatusOK, body)
	rel, err := latestRelease(context.Background(), gh.srv.Client(), gh.srv.URL, "me/tt")
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if rel == nil || rel.TagName != "v0.2.0" || rel.HTMLURL != "https://example.test/r/v0.2.0" {
		t.Fatalf("release = %+v", rel)
	}

	// Repo chưa phát hành bản nào: không phải lỗi, chỉ là không có gì.
	gh404 := newGHMock(t, http.StatusNotFound, `{"message":"Not Found"}`)
	rel, err = latestRelease(context.Background(), gh404.srv.Client(), gh404.srv.URL, "me/tt")
	if err != nil || rel != nil {
		t.Fatalf("404 -> (%+v, %v); muốn (nil, nil)", rel, err)
	}

	// Rate limit / GitHub sập: phải là lỗi để log ra, không im lặng.
	gh403 := newGHMock(t, http.StatusForbidden, `{"message":"rate limit"}`)
	if _, err = latestRelease(context.Background(), gh403.srv.Client(), gh403.srv.URL, "me/tt"); err == nil {
		t.Error("403 phải trả lỗi")
	}

	// Release không có tag thì không so sánh được -> coi là lỗi.
	ghEmpty := newGHMock(t, http.StatusOK, `{"tag_name":"  "}`)
	if _, err = latestRelease(context.Background(), ghEmpty.srv.Client(), ghEmpty.srv.URL, "me/tt"); err == nil {
		t.Error("release thiếu tag phải trả lỗi")
	}
}

// updBot dựng bot nói chuyện với cả Telegram giả và GitHub giả, và ghim
// version của binary trong lúc test.
func updBot(t *testing.T, tg *tgMock, gh *ghMock, ver string, users ...int64) *Bot {
	t.Helper()
	old := version
	version = ver
	t.Cleanup(func() { version = old })
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // đừng chạm vào cache thật của máy

	b := tg.bot(Config{UpdateRepo: "me/tt", AllowedUserIDs: users})
	b.ghBase = gh.srv.URL
	return b
}

func TestCheckUpdate(t *testing.T) {
	const body = `{"tag_name":"v0.2.0","html_url":"https://example.test/r"}`

	// Có bản mới hơn.
	tg, gh := newTGMock(), newGHMock(t, http.StatusOK, body)
	b := updBot(t, tg, gh, "v0.1.0")
	rel, err := b.checkUpdate(context.Background())
	if err != nil || rel == nil {
		t.Fatalf("checkUpdate = (%+v, %v); muốn có bản mới", rel, err)
	}
	if got := b.updateAvailable(); got == nil || got.TagName != "v0.2.0" {
		t.Errorf("updateAvailable = %+v; muốn v0.2.0", got)
	}

	// Đang chạy đúng bản mới nhất: biết release nhưng không coi là có bản mới.
	tg2, gh2 := newTGMock(), newGHMock(t, http.StatusOK, body)
	b2 := updBot(t, tg2, gh2, "v0.2.0")
	if rel, err = b2.checkUpdate(context.Background()); err != nil || rel != nil {
		t.Fatalf("checkUpdate = (%+v, %v); muốn (nil, nil)", rel, err)
	}
	if got := b2.updateAvailable(); got != nil {
		t.Errorf("updateAvailable = %+v; muốn nil", got)
	}

	// Bản tự build: không so được -> không bao giờ báo có bản mới.
	tg3, gh3 := newTGMock(), newGHMock(t, http.StatusOK, body)
	b3 := updBot(t, tg3, gh3, devVersion)
	if rel, err = b3.checkUpdate(context.Background()); err != nil || rel != nil {
		t.Fatalf("bản dev: checkUpdate = (%+v, %v); muốn (nil, nil)", rel, err)
	}
	if got := b3.updateAvailable(); got != nil {
		t.Errorf("bản dev: updateAvailable = %+v; muốn nil", got)
	}
}

// Mỗi version chỉ nhắc một lần, dù vòng kiểm tra chạy lại nhiều lần.
func TestAnnounceUpdateOnlyOnce(t *testing.T) {
	tg := newTGMock()
	gh := newGHMock(t, http.StatusOK, `{"tag_name":"v0.2.0","html_url":"https://example.test/r"}`)
	b := updBot(t, tg, gh, "v0.1.0", 7, 8)

	rel := &release{TagName: "v0.2.0", HTMLURL: "https://example.test/r"}
	b.announceUpdate(rel)
	b.announceUpdate(rel)

	sent, _, _ := tg.all()
	if len(sent) != 2 {
		t.Fatalf("gửi %d tin (%v); muốn 2 — mỗi người trong allowlist một tin", len(sent), sent)
	}
	for _, s := range sent {
		if !strings.Contains(s, "v0.2.0") || !strings.Contains(s, "v0.1.0") {
			t.Errorf("tin nhắn thiếu số version: %q", s)
		}
		if !strings.Contains(s, "install.sh") {
			t.Errorf("tin nhắn thiếu lệnh cập nhật: %q", s)
		}
	}

	// Tag mới hơn nữa thì nhắc lại.
	b.announceUpdate(&release{TagName: "v0.3.0", HTMLURL: "https://example.test/r3"})
	if sent, _, _ = tg.all(); len(sent) != 4 {
		t.Fatalf("gửi %d tin; muốn 4 sau khi có tag mới", len(sent))
	}
}

// Tag đã nhắc được ghi ra đĩa: restart service không nhắc lại.
func TestNotifiedTagSurvivesRestart(t *testing.T) {
	tg := newTGMock()
	gh := newGHMock(t, http.StatusOK, `{"tag_name":"v0.2.0","html_url":"https://example.test/r"}`)
	b := updBot(t, tg, gh, "v0.1.0", 7)

	rel := &release{TagName: "v0.2.0", HTMLURL: "https://example.test/r"}
	b.announceUpdate(rel)

	// Bot mới, cùng cache (XDG_CACHE_HOME giữ nguyên trong cả test này).
	b2 := tg.bot(Config{UpdateRepo: "me/tt", AllowedUserIDs: []int64{7}})
	b2.ghBase = gh.srv.URL
	b2.upd.notified = loadNotifiedTag()
	b2.announceUpdate(rel)

	if sent, _, _ := tg.all(); len(sent) != 1 {
		t.Fatalf("gửi %d tin; muốn 1 — bản mới đã nhắc trước khi restart", len(sent))
	}
}

func TestVersionTextStates(t *testing.T) {
	const body = `{"tag_name":"v0.2.0","html_url":"https://example.test/r"}`

	tg, gh := newTGMock(), newGHMock(t, http.StatusOK, body)
	b := updBot(t, tg, gh, "v0.1.0")
	if got := b.versionText(1); !strings.Contains(got, "v0.1.0") || !strings.Contains(got, "v0.2.0") ||
		!strings.Contains(got, "install.sh") {
		t.Errorf("có bản mới: /version = %q", got)
	}

	tg2, gh2 := newTGMock(), newGHMock(t, http.StatusOK, body)
	b2 := updBot(t, tg2, gh2, "v0.2.0")
	if got := b2.versionText(1); strings.Contains(got, "install.sh") {
		t.Errorf("đang mới nhất mà vẫn hiện lệnh cập nhật: %q", got)
	}

	// GitHub lỗi: /version vẫn trả lời được, chỉ kèm ghi chú.
	tg3, gh3 := newTGMock(), newGHMock(t, http.StatusInternalServerError, `{}`)
	b3 := updBot(t, tg3, gh3, "v0.1.0")
	if got := b3.versionText(1); !strings.Contains(got, "v0.1.0") || !strings.Contains(got, "500") {
		t.Errorf("GitHub lỗi: /version = %q", got)
	}

	// Repo chưa có release nào.
	tg4, gh4 := newTGMock(), newGHMock(t, http.StatusNotFound, `{}`)
	b4 := updBot(t, tg4, gh4, "v0.1.0")
	if got := b4.versionText(1); !strings.Contains(got, "me/tt") {
		t.Errorf("chưa có release: /version = %q", got)
	}
}

// watchUpdates phải tự tắt (không gọi mạng) khi config tắt hoặc bản tự build.
func TestWatchUpdatesOff(t *testing.T) {
	for _, c := range []struct {
		name  string
		ver   string
		hours int
	}{
		{"config tắt", "v0.1.0", -1},
		{"bản tự build", devVersion, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tg := newTGMock()
			gh := newGHMock(t, http.StatusOK, `{"tag_name":"v9.9.9"}`)
			b := updBot(t, tg, gh, c.ver)
			b.cfg.UpdateCheckHours = c.hours

			done := make(chan struct{})
			go func() { b.watchUpdates(context.Background()); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("watchUpdates không thoát ngay khi bị tắt")
			}
			if gh.calls != 0 {
				t.Errorf("đã gọi GitHub %d lần dù đang tắt", gh.calls)
			}
		})
	}
}
