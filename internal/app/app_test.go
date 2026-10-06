package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/collector/claudecode"
	"github.com/kyoujyu88/wrist-quota/internal/quota"
	"github.com/kyoujyu88/wrist-quota/internal/store"
)

type ntfyServer struct {
	mu       sync.Mutex
	messages []map[string]any
	deletes  []string
	*httptest.Server
}

func newNtfyServer(t *testing.T) *ntfyServer {
	s := &ntfyServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodDelete {
			s.deletes = append(s.deletes, r.URL.Path)
			return
		}
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		s.messages = append(s.messages, m)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *ntfyServer) titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.messages {
		title, _ := m["title"].(string)
		if _, scheduled := m["delay"]; scheduled {
			title += " (scheduled)"
		}
		out = append(out, title)
	}
	return out
}

type fixture struct {
	t    *testing.T
	dir  string
	now  time.Time
	ntfy *ntfyServer
	app  *App
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, dir: t.TempDir(), now: time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC), ntfy: newNtfyServer(t)}
	f.start()
	server := f.ntfy.URL
	if err := f.app.UpdateConfig(ConfigUpdate{Ntfy: &NtfyUpdate{Server: server, Topic: f.app.PublicConfig().Ntfy.Topic}}); err != nil {
		t.Fatal(err)
	}
	return f
}

// start creates the app from what is on disk, as a process start does.
func (f *fixture) start() {
	a, err := New(Options{
		Dir:            filepath.Join(f.dir, "config"),
		CodexHome:      filepath.Join(f.dir, "codex"),
		ClaudeSettings: filepath.Join(f.dir, "claude", "settings.json"),
		Executable:     "/opt/wrist-quota/wrist-quota",
		Now:            func() time.Time { return f.now },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.app = a
}

// claudeUses simulates the statusline subcommand storing an observation.
func (f *fixture) claudeUses(used float64, resetsAt time.Time) {
	f.t.Helper()
	snap := claudecode.Snapshot{ObservedAt: f.now, Windows: []quota.Window{{
		Provider: claudecode.Provider, Window: "5h", UsedPercent: used, ResetsAt: &resetsAt, ObservedAt: f.now, Source: claudecode.Source,
	}}}
	st := store.Store{Dir: filepath.Join(f.dir, "config")}
	if err := st.SaveSnapshot(snap); err != nil {
		f.t.Fatal(err)
	}
	// Make sure the modification time differs from the previous write.
	mod := f.now.Add(time.Duration(len(f.ntfy.titles())+1) * time.Second)
	os.Chtimes(st.SnapshotPath(), mod, mod)
	f.now = f.now.Add(time.Second)
	f.app.PollClaude()
}

func (f *fixture) wantTitles(want ...string) {
	f.t.Helper()
	got := f.ntfy.titles()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		f.t.Fatalf("notifications = %q; want %q", got, want)
	}
}

func TestFirstStartCreatesPrivateConfigWithRandomTopic(t *testing.T) {
	f := newFixture(t)
	topic := f.app.PublicConfig().Ntfy.Topic
	if !strings.HasPrefix(topic, "wq-") || len(topic) < 25 || store.WeakTopic(topic) {
		t.Errorf("generated topic %q is too weak", topic)
	}
	other, _ := store.GenerateTopic()
	if other == topic {
		t.Error("topics repeat")
	}
	for _, name := range []string{"config.json"} {
		info, err := os.Stat(filepath.Join(f.dir, "config", name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is readable by others: %o", name, perm)
		}
	}
	if info, _ := os.Stat(filepath.Join(f.dir, "config")); info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config directory is open to others: %o", info.Mode().Perm())
	}
}

func TestThresholdToResetFlowSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	reset := f.now.Add(2 * time.Hour)

	f.claudeUses(50, reset)
	f.wantTitles()
	if v := f.app.View(); len(v.Windows) != 1 || v.Windows[0].Remaining != 50 || v.Windows[0].Status != "fresh" {
		t.Fatalf("view = %+v", v.Windows)
	}

	f.claudeUses(85, reset)
	f.wantTitles("Claude 5h 残り15%", "Claude 5h リセット (scheduled)")

	f.start()
	f.app.PollClaude()
	f.app.Tick()
	f.claudeUses(86, reset)
	f.wantTitles("Claude 5h 残り15%", "Claude 5h リセット (scheduled)")

	v := f.app.View()
	if w := v.Windows[0]; len(w.SentLevels) != 1 || w.SentLevels[0] != 20 || w.ResetScheduledFor == nil {
		t.Errorf("window view = %+v", w)
	}
	if len(v.History) != 2 || v.SentToday != 2 {
		t.Errorf("history %d entries, sent today %d", len(v.History), v.SentToday)
	}

	// After the reset time the dashboard shows the window as recovered.
	f.now = reset.Add(time.Minute)
	f.app.Tick()
	if w := f.app.View().Windows[0]; w.Status != "reset_estimated" || w.Remaining != 100 {
		t.Errorf("after reset: %+v", w)
	}
	f.wantTitles("Claude 5h 残り15%", "Claude 5h リセット (scheduled)")
}

func TestNotificationCarriesOnlyQuotaFacts(t *testing.T) {
	f := newFixture(t)
	f.claudeUses(95, f.now.Add(time.Hour))
	f.ntfy.mu.Lock()
	defer f.ntfy.mu.Unlock()
	if len(f.ntfy.messages) == 0 {
		t.Fatal("nothing was sent")
	}
	allowed := map[string]bool{"topic": true, "title": true, "message": true, "priority": true, "delay": true, "sequence_id": true}
	for _, m := range f.ntfy.messages {
		for k := range m {
			if !allowed[k] {
				t.Errorf("unexpected field %q in %v", k, m)
			}
		}
	}
}

func TestCodexCollectionFailureDoesNotStopClaude(t *testing.T) {
	f := newFixture(t)
	// A directory where a log file is expected makes the scan fail.
	bad := filepath.Join(f.dir, "codex", "sessions", "2026", "10", "07", "rollout-x.jsonl")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	f.app.PollCodex()
	f.claudeUses(30, f.now.Add(time.Hour))
	v := f.app.View()
	if len(v.Windows) != 1 || v.Windows[0].Provider != claudecode.Provider {
		t.Errorf("windows = %+v", v.Windows)
	}
}

func TestUpdateConfigValidation(t *testing.T) {
	f := newFixture(t)
	topic := f.app.PublicConfig().Ntfy.Topic
	bad := map[string]ConfigUpdate{
		"server without scheme": {Ntfy: &NtfyUpdate{Server: "ntfy.sh", Topic: topic}},
		"server with userinfo":  {Ntfy: &NtfyUpdate{Server: "https://user:pass@ntfy.sh", Topic: topic}},
		"topic with slash":      {Ntfy: &NtfyUpdate{Server: "https://ntfy.sh", Topic: "a/b"}},
		"empty topic":           {Ntfy: &NtfyUpdate{Server: "https://ntfy.sh", Topic: ""}},
		"unknown provider":      {Providers: map[string]store.ProviderConfig{"cursor": {Enabled: true}}},
		"priority out of range": {Rules: []store.Rule{{ID: "a", Kind: store.KindThreshold, Levels: []int{20}, Priority: 6, Title: "t"}}},
		"level out of range":    {Rules: []store.Rule{{ID: "a", Kind: store.KindThreshold, Levels: []int{100}, Priority: 3, Title: "t"}}},
		"no levels":             {Rules: []store.Rule{{ID: "a", Kind: store.KindThreshold, Priority: 3, Title: "t"}}},
		"unknown kind":          {Rules: []store.Rule{{ID: "a", Kind: "digest", Priority: 3, Title: "t"}}},
		"empty title":           {Rules: []store.Rule{{ID: "a", Kind: store.KindReset, Priority: 3, Title: " "}}},
		"duplicate id":          {Rules: []store.Rule{{ID: "a", Kind: store.KindReset, Priority: 3, Title: "t"}, {ID: "a", Kind: store.KindReset, Priority: 3, Title: "t"}}},
		"bad target":            {Rules: []store.Rule{{ID: "a", Kind: store.KindReset, Priority: 3, Title: "t", Targets: []string{"claude"}}}},
		"poll too fast":         {PollSeconds: ptr(1)},
	}
	before := f.app.PublicConfig()
	for name, u := range bad {
		if err := f.app.UpdateConfig(u); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	after := f.app.PublicConfig()
	if a, b := mustJSON(before), mustJSON(after); a != b {
		t.Errorf("rejected updates changed the configuration:\n%s\n%s", a, b)
	}

	ok := ConfigUpdate{
		PollSeconds: ptr(60),
		Rules:       []store.Rule{{ID: "t1", Kind: store.KindThreshold, Enabled: true, Levels: []int{5, 30}, Priority: 5, Title: "x"}},
		Providers:   map[string]store.ProviderConfig{"codex": {Enabled: false}},
	}
	if err := f.app.UpdateConfig(ok); err != nil {
		t.Fatal(err)
	}
	f.start()
	got := f.app.PublicConfig()
	if got.PollSeconds != 60 || len(got.Rules) != 1 || got.Rules[0].Levels[0] != 30 || got.Providers["codex"].Enabled || !got.Providers["claude-code"].Enabled {
		t.Errorf("saved configuration = %s", mustJSON(got))
	}
}

func TestTokenIsWriteOnly(t *testing.T) {
	f := newFixture(t)
	c := f.app.PublicConfig()
	secret := "tk_verysecret"
	if err := f.app.UpdateConfig(ConfigUpdate{Ntfy: &NtfyUpdate{Server: c.Ntfy.Server, Topic: c.Ntfy.Topic, Token: &secret}}); err != nil {
		t.Fatal(err)
	}
	if out := mustJSON(f.app.PublicConfig()) + mustJSON(f.app.View()); strings.Contains(out, secret) {
		t.Error("the access token is exposed through the API")
	}
	if !f.app.PublicConfig().Ntfy.TokenSet || !f.app.View().TokenInFile {
		t.Error("token state is not reported")
	}
	// Saving the destination again without a token keeps the stored one.
	if err := f.app.UpdateConfig(ConfigUpdate{Ntfy: &NtfyUpdate{Server: c.Ntfy.Server, Topic: c.Ntfy.Topic}}); err != nil {
		t.Fatal(err)
	}
	if !f.app.PublicConfig().Ntfy.TokenSet {
		t.Error("token was dropped by an update that did not mention it")
	}
}

func TestTestSendAndTopicRegeneration(t *testing.T) {
	f := newFixture(t)
	if err := f.app.TestSend(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.app.TestSend(context.Background(), "threshold"); err != nil {
		t.Fatal(err)
	}
	if err := f.app.TestSend(context.Background(), "missing"); err == nil {
		t.Error("test send for an unknown rule succeeded")
	}
	f.wantTitles("wrist-quota テスト送信", "Claude 5h 残り18%")

	// A reset scheduled on the old topic is scheduled again on the new one.
	f.claudeUses(85, f.now.Add(2*time.Hour))
	old := f.app.PublicConfig().Ntfy.Topic
	if err := f.app.RegenerateTopic(); err != nil {
		t.Fatal(err)
	}
	next := f.app.PublicConfig().Ntfy.Topic
	if next == old {
		t.Fatal("topic did not change")
	}
	f.app.Tick()
	f.ntfy.mu.Lock()
	last := f.ntfy.messages[len(f.ntfy.messages)-1]
	f.ntfy.mu.Unlock()
	if last["topic"] != next || last["delay"] == nil {
		t.Errorf("last message = %v; want the reset rescheduled on the new topic", last)
	}
}

func TestStatuslineInstallAndUninstall(t *testing.T) {
	f := newFixture(t)
	settings := filepath.Join(f.dir, "claude", "settings.json")
	original := "{\n  \"theme\": \"dark\",\n  \"statusLine\": {\"type\": \"command\", \"command\": \"my-status.sh\"}\n}\n"
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(original), 0o600)

	if err := f.app.InstallStatusline(); err != nil {
		t.Fatal(err)
	}
	if err := f.app.InstallStatusline(); err == nil {
		t.Error("second install succeeded")
	}
	v := f.app.View().Statusline
	if !v.Installed || v.Wrapped != "my-status.sh" || v.Current != `"/opt/wrist-quota/wrist-quota" statusline` {
		t.Errorf("statusline view = %+v", v)
	}

	f.start() // the registration record survives a restart
	if err := f.app.UninstallStatusline(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(settings); string(got) != original {
		t.Errorf("settings after uninstall:\n%s", got)
	}
	if f.app.View().Statusline.Installed {
		t.Error("still reported as installed")
	}
	if err := f.app.UninstallStatusline(); err == nil {
		t.Error("second uninstall succeeded")
	}
}

func ptr[T any](v T) *T { return &v }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
