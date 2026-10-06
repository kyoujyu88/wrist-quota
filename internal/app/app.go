// Package app ties collection, rules and notification together and is the
// single owner of configuration and state at run time.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/claudesettings"
	"github.com/kyoujyu88/wrist-quota/internal/collector/claudecode"
	"github.com/kyoujyu88/wrist-quota/internal/collector/codex"
	"github.com/kyoujyu88/wrist-quota/internal/notifier/ntfy"
	"github.com/kyoujyu88/wrist-quota/internal/quota"
	"github.com/kyoujyu88/wrist-quota/internal/rules"
	"github.com/kyoujyu88/wrist-quota/internal/store"
)

const (
	snapshotPoll = 2 * time.Second
	historyShown = 50
	backupFile   = "claude-settings.backup.json"
)

type Options struct {
	Dir            string
	CodexHome      string
	ClaudeSettings string
	// Executable is the path registered as the status line command.
	Executable string
	Now        func() time.Time
	HTTP       *http.Client
}

type App struct {
	mu      sync.Mutex
	opts    Options
	store   store.Store
	cfg     *store.Config
	state   *store.State
	engine  *rules.Engine
	codex   *codex.Collector
	snapMod time.Time
}

func New(o Options) (*App, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &App{opts: o, store: store.Store{Dir: o.Dir}, codex: &codex.Collector{Home: o.CodexHome}}
	var err error
	if a.cfg, err = a.store.LoadConfig(); err != nil {
		return nil, err
	}
	if a.state, err = a.store.LoadState(); err != nil {
		return nil, err
	}
	a.engine = &rules.Engine{Config: a.cfg, State: a.state, Notifier: liveNotifier{a}, Now: o.Now}
	return a, nil
}

// liveNotifier builds the client from the current configuration on each
// use, so that a changed topic or server takes effect at once. It is only
// called with a.mu held.
type liveNotifier struct{ a *App }

func (n liveNotifier) Send(ctx context.Context, m ntfy.Message) error {
	return n.a.client().Send(ctx, m)
}

func (n liveNotifier) Cancel(ctx context.Context, id string) error {
	return n.a.client().Cancel(ctx, id)
}

func (a *App) client() *ntfy.Client {
	return &ntfy.Client{Server: a.cfg.Ntfy.Server, Topic: a.cfg.Ntfy.Topic, Token: a.cfg.Ntfy.Token, HTTP: a.opts.HTTP}
}

func (a *App) Listen() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Listen
}

// Run collects and evaluates until ctx is cancelled.
func (a *App) Run(ctx context.Context) {
	a.PollClaude()
	a.PollCodex()
	a.Tick()

	snap := time.NewTicker(snapshotPoll)
	defer snap.Stop()
	a.mu.Lock()
	interval := time.Duration(a.cfg.PollSeconds) * time.Second
	a.mu.Unlock()
	poll := time.NewTicker(interval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-snap.C:
			a.PollClaude()
		case <-poll.C:
			a.PollCodex()
			a.Tick()
		}
	}
}

// PollClaude picks up the snapshot the statusline subcommand writes. The
// file is only read when it changed, which keeps the frequent check cheap.
func (a *App) PollClaude() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Providers[claudecode.Provider].Enabled {
		return
	}
	info, err := os.Stat(a.store.SnapshotPath())
	if err != nil {
		if !os.IsNotExist(err) {
			a.setStatus(claudecode.Provider, err)
		}
		return
	}
	if info.ModTime().Equal(a.snapMod) {
		return
	}
	a.snapMod = info.ModTime()
	snap, err := claudecode.ReadSnapshot(a.store.SnapshotPath())
	a.setStatus(claudecode.Provider, err)
	if err == nil && snap != nil {
		a.engine.Observe(snap.Windows)
	}
	a.save()
}

func (a *App) PollCodex() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Providers[codex.Provider].Enabled {
		return
	}
	ws, err := a.codex.Collect()
	a.setStatus(codex.Provider, err)
	if err == nil {
		a.engine.Observe(ws)
	}
	a.save()
}

func (a *App) Tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.engine.Tick()
	a.save()
}

// setStatus records a collection failure for one provider only; the other
// providers and the process keep running.
func (a *App) setStatus(provider string, err error) {
	st := store.ProviderStatus{CheckedAt: a.opts.Now()}
	if err != nil {
		st.Error = err.Error()
	}
	a.state.Providers[provider] = st
}

func (a *App) save() {
	if err := a.store.SaveState(a.state); err != nil {
		log.Printf("cannot save state: %v", err)
	}
}

type WindowView struct {
	Key               string     `json:"key"`
	Provider          string     `json:"provider"`
	ProviderName      string     `json:"provider_name"`
	Window            string     `json:"window"`
	UsedPercent       float64    `json:"used_percent"`
	Remaining         float64    `json:"remaining"`
	ResetsAt          *time.Time `json:"resets_at"`
	ObservedAt        time.Time  `json:"observed_at"`
	Source            string     `json:"source"`
	Status            string     `json:"status"`
	SentLevels        []int      `json:"sent_levels"`
	ResetScheduledFor *time.Time `json:"reset_scheduled_for"`
}

type ProviderView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error"`
	HasData   bool       `json:"has_data"`
}

type StatuslineView struct {
	Installed    bool   `json:"installed"`
	Wrapped      string `json:"wrapped"`
	Current      string `json:"current"`
	SettingsPath string `json:"settings_path"`
	Error        string `json:"error"`
}

type View struct {
	Now          time.Time            `json:"now"`
	Windows      []WindowView         `json:"windows"`
	Providers    []ProviderView       `json:"providers"`
	History      []store.HistoryEntry `json:"history"`
	Statusline   StatuslineView       `json:"statusline"`
	SubscribeURL string               `json:"subscribe_url"`
	TopicWeak    bool                 `json:"topic_weak"`
	TokenInFile  bool                 `json:"token_in_file"`
	SentToday    int                  `json:"sent_today"`
	DailyLimit   int                  `json:"daily_limit"`
}

// View returns everything the dashboard shows.
func (a *App) View() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opts.Now()
	v := View{
		Now:          now,
		Windows:      []WindowView{},
		SubscribeURL: strings.TrimRight(a.cfg.Ntfy.Server, "/") + "/" + a.cfg.Ntfy.Topic,
		TopicWeak:    store.WeakTopic(a.cfg.Ntfy.Topic),
		TokenInFile:  a.cfg.Ntfy.Token != "",
		DailyLimit:   rules.DailyLimit,
	}
	if a.state.SentDay == now.UTC().Format("2006-01-02") {
		v.SentToday = a.state.SentCount
	}

	hasData := map[string]bool{}
	for key, w := range a.state.Windows {
		wv := WindowView{
			Key: key, Provider: w.Provider, ProviderName: rules.ProviderName(w.Provider), Window: w.Window,
			UsedPercent: w.UsedPercent, Remaining: w.Remaining(), ResetsAt: w.ResetsAt, ObservedAt: w.ObservedAt,
			Source: w.Source, Status: string(w.StatusAt(now)), SentLevels: []int{},
		}
		if wv.Status == string(quota.StatusResetEstimated) {
			wv.UsedPercent, wv.Remaining = 0, 100
		}
		if t := a.state.Tracks[key]; t != nil {
			wv.SentLevels = append(wv.SentLevels, t.SentLevels...)
			wv.ResetScheduledFor = t.ResetScheduledFor
		}
		hasData[w.Provider] = true
		v.Windows = append(v.Windows, wv)
	}
	sort.Slice(v.Windows, func(i, j int) bool { return v.Windows[i].Key < v.Windows[j].Key })

	for _, id := range []string{claudecode.Provider, codex.Provider} {
		pv := ProviderView{ID: id, Name: rules.ProviderName(id), Enabled: a.cfg.Providers[id].Enabled, HasData: hasData[id]}
		if st, ok := a.state.Providers[id]; ok {
			checked := st.CheckedAt
			pv.CheckedAt, pv.Error = &checked, st.Error
		}
		v.Providers = append(v.Providers, pv)
	}

	for i := len(a.state.History) - 1; i >= 0 && len(v.History) < historyShown; i-- {
		v.History = append(v.History, a.state.History[i])
	}
	if v.History == nil {
		v.History = []store.HistoryEntry{}
	}

	v.Statusline = StatuslineView{
		Installed: a.cfg.Statusline.Installed, Wrapped: a.cfg.Statusline.Wrapped, SettingsPath: a.opts.ClaudeSettings,
	}
	current, err := claudesettings.Current(a.opts.ClaudeSettings)
	if err != nil {
		v.Statusline.Error = err.Error()
	}
	v.Statusline.Current = current
	return v
}

type NtfyView struct {
	Server   string `json:"server"`
	Topic    string `json:"topic"`
	TokenSet bool   `json:"token_set"`
}

type ConfigView struct {
	PollSeconds int                             `json:"poll_seconds"`
	Ntfy        NtfyView                        `json:"ntfy"`
	Providers   map[string]store.ProviderConfig `json:"providers"`
	Rules       []store.Rule                    `json:"rules"`
}

// PublicConfig is the editable configuration. The access token is write-only.
func (a *App) PublicConfig() ConfigView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ConfigView{
		PollSeconds: a.cfg.PollSeconds,
		Ntfy:        NtfyView{Server: a.cfg.Ntfy.Server, Topic: a.cfg.Ntfy.Topic, TokenSet: a.cfg.Ntfy.Token != ""},
		Providers:   a.cfg.Providers,
		Rules:       a.cfg.Rules,
	}
}

type NtfyUpdate struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
	// Token replaces the stored token when present; "" removes it.
	Token *string `json:"token"`
}

// ConfigUpdate changes the parts that are present and leaves the rest.
type ConfigUpdate struct {
	PollSeconds *int                            `json:"poll_seconds"`
	Ntfy        *NtfyUpdate                     `json:"ntfy"`
	Providers   map[string]store.ProviderConfig `json:"providers"`
	Rules       []store.Rule                    `json:"rules"`
}

var (
	topicPattern = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	idPattern    = regexp.MustCompile(`^[-_A-Za-z0-9]{1,40}$`)
	keyPattern   = regexp.MustCompile(`^[-_A-Za-z0-9]{1,40}/[-_A-Za-z0-9]{1,40}$`)
)

func (a *App) UpdateConfig(u ConfigUpdate) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := *a.cfg

	if u.PollSeconds != nil {
		if *u.PollSeconds < 5 || *u.PollSeconds > 3600 {
			return errors.New("収集間隔は 5〜3600 秒で指定してください")
		}
		next.PollSeconds = *u.PollSeconds
	}
	destinationChanged := false
	if u.Ntfy != nil {
		server := strings.TrimRight(strings.TrimSpace(u.Ntfy.Server), "/")
		parsed, err := url.Parse(server)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return errors.New("サーバー URL は http:// または https:// で始まる URL を指定してください")
		}
		if !topicPattern.MatchString(u.Ntfy.Topic) {
			return errors.New("トピック名は半角英数字・ハイフン・アンダースコアの 1〜64 文字で指定してください")
		}
		destinationChanged = server != next.Ntfy.Server || u.Ntfy.Topic != next.Ntfy.Topic
		next.Ntfy.Server, next.Ntfy.Topic = server, u.Ntfy.Topic
		if u.Ntfy.Token != nil {
			next.Ntfy.Token = strings.TrimSpace(*u.Ntfy.Token)
		}
	}
	if u.Providers != nil {
		providers := map[string]store.ProviderConfig{}
		for id, p := range next.Providers {
			providers[id] = p
		}
		for id, p := range u.Providers {
			if _, known := providers[id]; !known {
				return fmt.Errorf("未対応のプロバイダです: %s", id)
			}
			providers[id] = p
		}
		next.Providers = providers
	}
	if u.Rules != nil {
		if err := validateRules(u.Rules); err != nil {
			return err
		}
		next.Rules = u.Rules
	}

	if err := a.store.SaveConfig(&next); err != nil {
		return err
	}
	*a.cfg = next
	if destinationChanged {
		a.forgetSchedules()
	}
	return nil
}

func validateRules(rs []store.Rule) error {
	seen := map[string]bool{}
	for i := range rs {
		r := &rs[i]
		if !idPattern.MatchString(r.ID) || seen[r.ID] {
			return errors.New("ルールの ID が重複しているか、使えない文字を含んでいます")
		}
		seen[r.ID] = true
		if r.Kind != store.KindThreshold && r.Kind != store.KindReset {
			return fmt.Errorf("ルールの種類が不正です: %s", r.Kind)
		}
		if r.Priority < 1 || r.Priority > 5 {
			return errors.New("優先度は 1〜5 で指定してください")
		}
		if strings.TrimSpace(r.Title) == "" {
			return errors.New("タイトルを入力してください")
		}
		if r.Targets == nil {
			r.Targets = []string{}
		}
		for _, t := range r.Targets {
			if !keyPattern.MatchString(t) {
				return fmt.Errorf("対象の指定が不正です: %s", t)
			}
		}
		if r.Kind == store.KindReset {
			r.Levels = nil
			continue
		}
		if len(r.Levels) == 0 || len(r.Levels) > 10 {
			return errors.New("しきい値は 1〜10 個指定してください")
		}
		for _, l := range r.Levels {
			if l < 1 || l > 99 {
				return errors.New("しきい値は 1〜99 の整数で指定してください")
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(r.Levels)))
	}
	return nil
}

// forgetSchedules makes pending reset notifications be scheduled again on
// the new destination. Those queued on the old one are left to expire.
func (a *App) forgetSchedules() {
	for _, t := range a.state.Tracks {
		if !t.ResetDone {
			t.ResetScheduledFor = nil
			delete(t.Failures, "schedule")
		}
	}
	a.save()
}

// TestSend sends a sample notification rendered from the given rule, so the
// wording can be checked on the watch. An empty id sends a generic message.
func (a *App) TestSend(ctx context.Context, ruleID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opts.Now()
	msg := ntfy.Message{Title: "wrist-quota テスト送信", Body: "この通知が届いていれば設定は完了です", Priority: 3}
	if ruleID != "" {
		var rule *store.Rule
		for i := range a.cfg.Rules {
			if a.cfg.Rules[i].ID == ruleID {
				rule = &a.cfg.Rules[i]
			}
		}
		if rule == nil {
			return errors.New("ルールが見つかりません。先に保存してください")
		}
		reset := now.Add(104 * time.Minute)
		sample := quota.Window{Provider: claudecode.Provider, Window: "5h", UsedPercent: 82, ResetsAt: &reset, ObservedAt: now}
		if rule.Kind == store.KindReset {
			sample.UsedPercent = 0
		}
		msg = ntfy.Message{
			Title:    rules.Render(rule.Title, sample, now, nil),
			Body:     rules.Render(rule.Body, sample, now, nil),
			Priority: rule.Priority,
		}
	}
	err := a.client().Send(ctx, msg)
	entry := store.HistoryEntry{Time: now, Kind: "test", Title: msg.Title, Body: msg.Body, OK: err == nil}
	if err != nil {
		entry.Error = err.Error()
	}
	a.state.AddHistory(entry)
	a.save()
	return err
}

func (a *App) RegenerateTopic() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	topic, err := store.GenerateTopic()
	if err != nil {
		return err
	}
	next := *a.cfg
	next.Ntfy.Topic = topic
	if err := a.store.SaveConfig(&next); err != nil {
		return err
	}
	*a.cfg = next
	a.forgetSchedules()
	return nil
}

// StatuslineCommand is what gets written into Claude Code's settings.
func StatuslineCommand(executable string) string {
	return `"` + filepath.ToSlash(executable) + `" statusline`
}

func (a *App) InstallStatusline() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.Statusline.Installed {
		return errors.New("すでに登録されています")
	}
	if a.opts.Executable == "" {
		return errors.New("実行ファイルの場所が分からないため登録できません")
	}
	res, err := claudesettings.Install(a.opts.ClaudeSettings, StatuslineCommand(a.opts.Executable), filepath.Join(a.store.Dir, backupFile))
	if err != nil {
		return err
	}
	next := *a.cfg
	next.Statusline = res
	if err := a.store.SaveConfig(&next); err != nil {
		// Without the saved record the registration could not be undone.
		claudesettings.Uninstall(a.opts.ClaudeSettings, res)
		return err
	}
	*a.cfg = next
	return nil
}

func (a *App) UninstallStatusline() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Statusline.Installed {
		return errors.New("登録されていません")
	}
	if err := claudesettings.Uninstall(a.opts.ClaudeSettings, a.cfg.Statusline); err != nil {
		return err
	}
	next := *a.cfg
	next.Statusline = store.StatuslineConfig{}
	if err := a.store.SaveConfig(&next); err != nil {
		return err
	}
	*a.cfg = next
	return nil
}
