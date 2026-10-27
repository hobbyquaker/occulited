package warnings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noticeIn(sev string) AddonNoticeInput {
	return AddonNoticeInput{Severity: sev, Title: map[string]string{"de": "Batterie schwach", "en": "Battery low"}, Text: map[string]string{"de": "3 Geräte", "en": "3 devices"}}
}

// occulited task 119: an addon's notices - posted, replaced, cleared, expired, kept across a
// restart, dropped with the addon, and shown as the Status page's warning addon-notice.
func TestAddonNotices(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	installed := map[string]bool{"battery": true, "history": true}
	s := &AddonNotices{File: filepath.Join(dir, "addon-notices.json"), Now: func() time.Time { return now }, Installed: func(id string) bool { return installed[id] }}

	n, created, err := s.Put("battery", "low", noticeIn(SeverityWarning))
	if err != nil || !created || n.ID != "low" || !n.Posted.Equal(now) {
		t.Fatalf("%+v %v %v", n, created, err)
	}
	// the same id again replaces it, keeps when it was first posted
	now = now.Add(time.Hour)
	in := noticeIn(SeverityError)
	in.Link = "/addon-settings/battery#/notifications"
	in.TTL = 3600
	n, created, err = s.Put("battery", "low", in)
	if err != nil || created || n.Severity != SeverityError || n.Posted.Equal(n.Updated) || !n.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("replace: %+v %v %v", n, created, err)
	}
	if _, _, err := s.Put("history", "full", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "Speicher voll", "en": "Storage full"}}); err != nil {
		t.Fatal(err)
	}
	ws := s.Warnings()
	if len(ws) != 2 || ws[0].ID != AddonNoticeID || ws[0].Variant != "battery/low" || ws[0].Severity != SeverityError || ws[0].Href != "/addon-settings/battery#/notifications" ||
		ws[1].Variant != "history/full" || ws[1].Href != "/addon-settings/history" || ws[1].Params["text"] != nil {
		t.Fatalf("warnings %+v", ws)
	}
	if ws[0].Params["addon"] != "battery" || ws[0].Params["title"].(map[string]string)["en"] != "Battery low" || ws[0].Params["expires"] == nil {
		t.Errorf("params %+v", ws[0].Params)
	}

	// kept across a restart of occulited
	s2 := &AddonNotices{File: s.File, Now: s.Now, Installed: s.Installed}
	if l := s2.List("battery"); len(l) != 1 || l[0].Severity != SeverityError || l[0].Text["de"] != "3 Geräte" {
		t.Fatalf("reloaded %+v", l)
	}
	if st, err := os.Stat(s.File); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file %v %v", st, err)
	}
	// the expiry ends it by itself
	now = now.Add(time.Hour)
	if l := s2.List("battery"); len(l) != 0 {
		t.Errorf("expired, still there: %+v", l)
	}
	// a gone addon's notices are dropped, and a reinstall does not bring them back
	installed["history"] = false
	if ws := s2.Warnings(); len(ws) != 0 {
		t.Errorf("a gone addon's notice: %+v", ws)
	}
	installed["history"] = true
	if l := s2.List("history"); len(l) != 0 {
		t.Errorf("came back: %+v", l)
	}

	// delete and clear
	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := s2.Put("battery", id, noticeIn(SeverityInfo)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s2.Delete("battery", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Delete("battery", "b"); !errors.Is(err, ErrNoSuchNotice) {
		t.Errorf("deleted twice: %v", err)
	}
	if l := s2.List("battery"); len(l) != 2 || l[0].ID != "a" || l[1].ID != "c" {
		t.Errorf("after delete %+v", l)
	}
	if n, err := s2.Clear("battery"); err != nil || n != 2 || len(s2.List("battery")) != 0 {
		t.Errorf("clear %d %v", n, err)
	}
	if n, err := s2.Clear("battery"); err != nil || n != 0 {
		t.Errorf("clear again %d %v", n, err)
	}

	// the limit
	for i := 0; i < MaxAddonNotices; i++ {
		if _, _, err := s2.Put("battery", "n"+string(rune('a'+i)), noticeIn(SeverityInfo)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, _, err := s2.Put("battery", "one-more", noticeIn(SeverityInfo)); !errors.Is(err, ErrTooManyNotices) {
		t.Errorf("past the limit: %v", err)
	}
	if _, _, err := s2.Put("battery", "na", noticeIn(SeverityWarning)); err != nil {
		t.Errorf("a replace at the limit: %v", err)
	}
}

func TestAddonNoticeRefusals(t *testing.T) {
	s := &AddonNotices{}
	long := strings.Repeat("x", MaxNoticeTitle+1)
	for name, c := range map[string]struct {
		addon, id string
		in        AddonNoticeInput
	}{
		"addon id":       {"Bad", "x", noticeIn(SeverityInfo)},
		"notice id":      {"a", "-x", noticeIn(SeverityInfo)},
		"notice id /":    {"a", "a/b", noticeIn(SeverityInfo)},
		"severity":       {"a", "x", noticeIn("fatal")},
		"no title":       {"a", "x", AddonNoticeInput{Severity: SeverityInfo}},
		"only en":        {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"en": "x"}}},
		"a third":        {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x", "fr": "x"}}},
		"empty de":       {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": " ", "en": "x"}}},
		"long":           {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": long, "en": "x"}}},
		"text one lang":  {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, Text: map[string]string{"de": "x"}}},
		"link scheme":    {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, Link: "https://evil.example/"}},
		"link js":        {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, Link: "javascript:alert(1)"}},
		"link host":      {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, Link: "//evil.example/x"}},
		"link quote":     {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, Link: "/a\"b"}},
		"ttl negative":   {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, TTL: -1}},
		"ttl a lifetime": {"a", "x", AddonNoticeInput{Severity: SeverityInfo, Title: map[string]string{"de": "x", "en": "x"}, TTL: int64(MaxNoticeTTL/time.Second) + 1}},
	} {
		if _, _, err := s.Put(c.addon, c.id, c.in); !errors.Is(err, ErrNoticeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The notices in the tracker: silenced like a system warning, kept silenced when the addon posts
// the same notice again, and the silence spent once the addon clears it.
func TestAddonNoticesInTheTracker(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := &AddonNotices{Now: func() time.Time { return now }}
	tr := &Tracker{Now: func() time.Time { return now }, Sources: []Source{{IDs: []string{AddonNoticeID}, Eval: func(context.Context) ([]Warning, bool) { return s.Warnings(), true }}}}
	ctx := context.Background()
	if _, _, err := s.Put("battery", "low", noticeIn(SeverityWarning)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Silence(ctx, "admin", AddonNoticeID, "battery/low", 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put("battery", "low", noticeIn(SeverityError)); err != nil {
		t.Fatal(err)
	}
	if l := tr.List(ctx, "admin", true); len(l) != 1 || l[0].Silenced == nil || l[0].Severity != SeverityError {
		t.Fatalf("%+v", l)
	}
	if len(tr.Unsilenced()) != 0 {
		t.Error("a silenced notice on the LED")
	}
	if err := s.Delete("battery", "low"); err != nil {
		t.Fatal(err)
	}
	tr.Evaluate(ctx)
	if _, _, err := s.Put("battery", "low", noticeIn(SeverityWarning)); err != nil {
		t.Fatal(err)
	}
	if l := tr.List(ctx, "admin", true); len(l) != 1 || l[0].Silenced != nil {
		t.Fatalf("the silence outlived the cleared notice: %+v", l)
	}
}
