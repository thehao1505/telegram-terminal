package main

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

var verbRe = regexp.MustCompile(`%[-+# 0-9.\[\]]*[a-zA-Z]`)

// TestLangCatalogParity: hai bộ chuỗi không được lệch nhau — thiếu key thì
// người dùng thấy tiếng Anh lẫn vào, lệch %verb thì Sprintf in ra rác.
func TestLangCatalogParity(t *testing.T) {
	for _, l := range languages {
		if l.Code == langEN.Code {
			continue
		}
		var missing, extra []string
		for k := range langEN.s {
			if _, ok := l.s[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range l.s {
			if _, ok := langEN.s[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s thiếu %d key: %v", l.Code, len(missing), missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s có key lạ không có ở en: %v", l.Code, extra)
		}
		for k, en := range langEN.s {
			tr, ok := l.s[k]
			if !ok {
				continue
			}
			if a, b := verbRe.FindAllString(en, -1), verbRe.FindAllString(tr, -1); strings.Join(a, "") != strings.Join(b, "") {
				t.Errorf("%s/%q: chuỗi thay thế lệch nhau — en=%v %s=%v", l.Code, k, a, l.Code, b)
			}
		}
	}
}

// Không được có key nào rỗng: gửi tin nhắn rỗng thì Telegram từ chối.
func TestLangNoEmptyStrings(t *testing.T) {
	for _, l := range languages {
		for k, v := range l.s {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s/%q rỗng", l.Code, k)
			}
		}
	}
}

func TestLanguageLookup(t *testing.T) {
	if l := languageByCode("VI "); l == nil || l.Code != "vi" {
		t.Errorf("phải nhận mã hoa/có khoảng trắng, nhận %v", l)
	}
	if languageByCode("de") != nil {
		t.Error("mã không có phải trả nil")
	}
	if got := (&Config{}).language(); got.Code != "en" {
		t.Errorf("config trống phải ra en, nhận %s", got.Code)
	}
	if got := (&Config{Language: "vi"}).language(); got.Code != "vi" {
		t.Errorf("config vi phải ra vi, nhận %s", got.Code)
	}
	if got := (&Config{Language: "khong-co"}).language(); got.Code != "en" {
		t.Errorf("mã lạ trong config phải lùi về en, nhận %s", got.Code)
	}
	// Key không tồn tại: hiện ra để lập trình viên thấy ngay chứ không im lặng.
	if got := langVI.t("khong.co.key.nay"); got != "!khong.co.key.nay" {
		t.Errorf("key thiếu ở cả hai bộ: %q", got)
	}
	// Key chỉ có ở en thì vi phải lùi về en.
	langEN.s["chi.co.o.en"] = "only in english"
	defer delete(langEN.s, "chi.co.o.en")
	if got := langVI.t("chi.co.o.en"); got != "only in english" {
		t.Errorf("phải lùi về en, nhận %q", got)
	}
}

// TestLangCommand: /lang liệt kê, /lang vi đổi, mã lạ bị chặn, nút bấm đổi
// được, và ngôn ngữ sống qua /reset.
func TestLangCommand(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{StartDir: "/home/user"})

	// Mặc định: tiếng Anh.
	if got := b.lang(1).Code; got != "en" {
		t.Fatalf("mặc định phải là en, nhận %s", got)
	}
	b.handle(msgUpdate(t, "/sh"))
	sent, _, _ := tg.all()
	if !containsSub(sent, "Shell mode") {
		t.Errorf("mặc định phải trả lời tiếng Anh: %q", sent)
	}

	// /lang: bảng liệt kê kèm nút.
	b.handle(msgUpdate(t, "/lang"))
	sent, _, _ = tg.all()
	last := sent[len(sent)-1]
	for _, want := range []string{"Language", "<code>en</code>", "<code>vi</code>", "Tiếng Việt", "✅"} {
		if !strings.Contains(last, want) {
			t.Errorf("/lang thiếu %q:\n%s", want, last)
		}
	}

	// /lang vi: đổi, và các tin sau phải là tiếng Việt.
	b.handle(msgUpdate(t, "/lang vi"))
	if got := b.lang(1).Code; got != "vi" {
		t.Fatalf("sau /lang vi phải là vi, nhận %s", got)
	}
	b.handle(msgUpdate(t, "/sh"))
	sent, _, _ = tg.all()
	if !containsSub(sent, "Chế độ shell") {
		t.Errorf("chưa chuyển sang tiếng Việt: %q", sent[len(sent)-1:])
	}

	// Mã lạ: báo lỗi (bằng đúng ngôn ngữ đang dùng) và không đổi gì.
	b.handle(msgUpdate(t, "/lang de"))
	sent, _, _ = tg.all()
	if !strings.Contains(sent[len(sent)-1], "không có ngôn ngữ") {
		t.Errorf("chưa báo mã lạ: %q", sent[len(sent)-1])
	}
	if got := b.lang(1).Code; got != "vi" {
		t.Errorf("mã lạ không được đổi ngôn ngữ, giờ là %s", got)
	}

	// /reset không được xóa lựa chọn ngôn ngữ.
	b.handle(msgUpdate(t, "/reset"))
	if got := b.lang(1).Code; got != "vi" {
		t.Errorf("/reset đã xóa ngôn ngữ, giờ là %s", got)
	}

	// Nút bấm lg|en đổi lại về tiếng Anh.
	var q callbackQuery
	raw := `{"id":"cb1","from":{"id":7},"message":{"message_id":9,"chat":{"id":1}},"data":"lg|en"}`
	if err := jsonUnmarshalString(raw, &q); err != nil {
		t.Fatal(err)
	}
	b.handleCallback(&q)
	if got := b.lang(1).Code; got != "en" {
		t.Errorf("nút bấm chưa đổi được ngôn ngữ, giờ là %s", got)
	}
	sent, _, _ = tg.all()
	if !strings.Contains(sent[len(sent)-1], "English") {
		t.Errorf("chưa báo đã đổi sang tiếng Anh: %q", sent[len(sent)-1])
	}

	// Ngôn ngữ theo từng chat: chat khác vẫn theo config.
	if got := b.lang(2).Code; got != "en" {
		t.Errorf("chat khác phải theo config, nhận %s", got)
	}
}

// Ngôn ngữ trong config áp cho mọi chat chưa chọn riêng.
func TestLangFromConfig(t *testing.T) {
	tg := newTGMock()
	defer tg.srv.Close()
	b := tg.bot(Config{StartDir: "/home/user", Language: "vi"})
	b.handle(msgUpdate(t, "/sh"))
	sent, _, _ := tg.all()
	if !containsSub(sent, "Chế độ shell") {
		t.Errorf("config vi chưa có tác dụng: %q", sent)
	}
	b.handle(msgUpdate(t, "/lang en"))
	b.handle(msgUpdate(t, "/sh"))
	sent, _, _ = tg.all()
	if !containsSub(sent, "Shell mode") {
		t.Errorf("/lang en chưa ghi đè config: %q", sent[len(sent)-1:])
	}
}
