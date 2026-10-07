package main

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWindows(t *testing.T) {
	ws, err := ParseWindows("09:00-10:00, 17:00-18:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || FormatWindows(ws) != "09:00-10:00,17:00-18:00" {
		t.Fatalf("windows = %v", FormatWindows(ws))
	}
	for _, bad := range []string{"", "10:00-09:00", "25:00-26:00", "0900-1000", "09:00"} {
		if _, err := ParseWindows(bad); err == nil {
			t.Fatalf("ParseWindows(%q) должен вернуть ошибку", bad)
		}
	}
}

func TestSchedulerNext(t *testing.T) {
	ws, _ := ParseWindows("09:00-10:00,17:00-18:00")
	s := NewScheduler(ws)
	loc := time.UTC

	// Утром до первого окна — ближайшее окно сегодня 09:xx.
	now := time.Date(2026, 9, 1, 7, 0, 0, 0, loc)
	next, idx := s.Next(now)
	if idx != 0 || next.Day() != 1 || next.Hour() != 9 {
		t.Fatalf("next = %v idx = %d", next, idx)
	}

	// После отработки утреннего окна следующее — вечернее, сегодня.
	s.MarkRun(0, next)
	next2, idx2 := s.Next(next.Add(time.Minute))
	if idx2 != 1 || next2.Day() != 1 || next2.Hour() != 17 {
		t.Fatalf("next2 = %v idx = %d", next2, idx2)
	}

	// Поздно вечером — утро следующего дня.
	late := time.Date(2026, 9, 1, 22, 0, 0, 0, loc)
	next3, idx3 := s.Next(late)
	if idx3 != 0 || next3.Day() != 2 || next3.Hour() != 9 {
		t.Fatalf("next3 = %v idx = %d", next3, idx3)
	}
}

func TestFeedNewer(t *testing.T) {
	cases := []struct {
		newD, oldD string
		want       bool
	}{
		{"07.07.2026 10:20", "", true},
		{"07.07.2026 10:20", "07.07.2026 10:20", false},
		{"08.07.2026 09:00", "07.07.2026 10:20", true},
		{"01.01.2026 09:00", "07.07.2026 10:20", false},
	}
	for _, c := range cases {
		if got := FeedNewer(c.newD, c.oldD); got != c.want {
			t.Fatalf("FeedNewer(%q, %q) = %v, want %v", c.newD, c.oldD, got, c.want)
		}
	}
}

func TestCollectMessageIDs(t *testing.T) {
	a := "11111111-2222-3333-4444-555555555555"
	b := "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	content := `<td data-messageId="` + a + `">`
	raw := `{"Parameters":[{"Name":"messageId","Value":"` + b + `"}],"x":"messageId ` + a + `"}`
	ids := collectMessageIDs(content, raw)
	if len(ids) != 2 || ids[0] != a || ids[1] != strings.ToLower(b) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestFeedURL(t *testing.T) {
	f, ok := FeedByName("info")
	if !ok {
		t.Fatal("лента info не найдена")
	}
	u := FeedURL(f)
	const prefix = "https://portal.fedsfm.ru/#"
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("url = %s", u)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), f.PageID) || !strings.Contains(string(raw), `"KbObjectType":4`) {
		t.Fatalf("payload = %s", raw)
	}
}

func TestLatestBySectionAndUnread(t *testing.T) {
	mk := func(id, code, date string, read bool) Notification {
		n := Notification{ID: id, CreateDate: date, IsRead: read}
		n.Event.EventTypeCode = code
		return n
	}
	items := []Notification{
		mk("1", "FedsfmPortal.NewTerroristCatalog", "2026-10-01T10:00:00.000", true),
		mk("2", "FedsfmPortal.NewTerroristCatalog", "2026-10-05T17:37:12.613", false),
		mk("3", "FedsfmPortal.NewMvkCatalog", "2026-06-25T12:20:58.827", false),
		mk("4", "FedsfmPortal.SbOonCatalog", "2026-10-06T18:00:25.837", false),
		mk("5", "Something.Else", "2026-10-07T00:00:00.000", false),
	}
	latest, ids := LatestBySection(items)
	if latest["terrorism"] != "2026-10-05T17:37:12.613" || latest["mvk"] == "" || latest["oon"] == "" {
		t.Fatalf("latest = %v", latest)
	}
	if len(ids["terrorism"]) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	if _, ok := latest[""]; ok {
		t.Fatal("неизвестный тип события не должен попадать в разделы")
	}
	unread := UnreadIDs(items, ids["terrorism"])
	if len(unread) != 1 || unread[0] != "2" {
		t.Fatalf("unread = %v", unread)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("отсутствующий state.json не должен быть ошибкой: %v", err)
	}
	s.Catalogs["oon"] = "2026-10-06T18:00:25.837"
	s.Feeds["info"] = "07.07.2026 10:20"
	if err := SaveState(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Catalogs["oon"] != s.Catalogs["oon"] || got.Feeds["info"] != s.Feeds["info"] {
		t.Fatalf("state = %+v", got)
	}
}

func TestParseSourcesAndFeeds(t *testing.T) {
	all, err := parseSources("all")
	if err != nil || len(all) != len(DefaultSources) {
		t.Fatalf("all = %v, %v", all, err)
	}
	some, err := parseSources("word, mvk")
	if err != nil || len(some) != 2 || some[0].Name != "doc" || some[1].Section != "mvk" {
		t.Fatalf("some = %v, %v", some, err)
	}
	if _, err := parseSources("nope"); err == nil {
		t.Fatal("неизвестный каталог должен давать ошибку")
	}

	none, err := parseFeeds("none", 67, 11)
	if err != nil || len(none) != 0 {
		t.Fatalf("none = %v, %v", none, err)
	}
	feeds, err := parseFeeds("court", 99, 11)
	if err != nil || len(feeds) != 1 || feeds[0].IdMessageType != 99 {
		t.Fatalf("feeds = %v, %v", feeds, err)
	}
}

func TestSummarizeRun(t *testing.T) {
	results := []ItemResult{
		{Section: "oon", Title: "ООН", Status: StatusUpdated, Saved: SavedFile{Archive: "perechen_oon-rus.zip", Bytes: 1 << 20, SHA256: strings.Repeat("a", 64)}},
		{Section: "mvk", Title: "МВК", Status: StatusUnchanged},
		{Section: "info", Title: "Инфо", Status: StatusError, Err: errors.New("сбой"), Link: "https://portal.fedsfm.ru/#x"},
	}
	text := summarizeRun(filepath.Join("data", "2026-10-07_09-12"), results)
	for _, want := range []string{"ИЗМЕНИЛОСЬ", "БЕЗ ИЗМЕНЕНИЙ", "ОШИБКИ", "perechen_oon-rus.zip", "сбой", "открыть в кабинете",
		"Итого: изменилось 1, без изменений 1, с ошибкой 1."} {
		if !strings.Contains(text, want) {
			t.Fatalf("в письме нет %q:\n%s", want, text)
		}
	}
	u, n, f := countStatuses(results)
	if u != 1 || n != 1 || f != 1 {
		t.Fatalf("counts = %d %d %d", u, n, f)
	}
}

func TestBuildMessageHeadersASCII(t *testing.T) {
	msg := buildMessage("robot@example.org", "team@example.org", "rosfin: изменений нет", "Текст письма")
	head := msg[:strings.Index(msg, "\r\n\r\n")]
	for _, r := range head {
		if r > 127 {
			t.Fatalf("в заголовках не-ASCII символ: %q", head)
		}
	}
	if !strings.Contains(head, "Content-Transfer-Encoding: base64") {
		t.Fatal("тело должно кодироваться base64")
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":    "passwd",
		"Решение суда №1.pdf": "Решение суда _1.pdf",
		"  ..  ":              "file",
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Fatalf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
