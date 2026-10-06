// Package store persists configuration and runtime state as JSON files that
// only the current user can read.
package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/quota"
)

const (
	configFile   = "config.json"
	stateFile    = "state.json"
	snapshotFile = "claudecode.json"

	DefaultListen = "127.0.0.1:47821"
	DefaultServer = "https://ntfy.sh"

	KindThreshold = "threshold"
	KindReset     = "reset"

	maxHistory = 200
)

type NtfyConfig struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
	// Token is kept in this file until OS credential stores are supported.
	Token string `json:"token,omitempty"`
}

// Rule is one notification rule. Targets lists "provider/window" keys; an
// empty list means every window.
type Rule struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Enabled  bool     `json:"enabled"`
	Targets  []string `json:"targets"`
	Levels   []int    `json:"levels,omitempty"`
	Priority int      `json:"priority"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
}

func (r Rule) AppliesTo(key string) bool {
	if len(r.Targets) == 0 {
		return true
	}
	for _, t := range r.Targets {
		if t == key {
			return true
		}
	}
	return false
}

type ProviderConfig struct {
	Enabled bool `json:"enabled"`
}

// StatuslineConfig records what was in Claude Code's settings before
// wrist-quota registered itself, so the registration can be undone.
type StatuslineConfig struct {
	Installed bool `json:"installed"`
	// Wrapped is the command of the status line that was configured before,
	// which the statusline subcommand keeps running.
	Wrapped string `json:"wrapped,omitempty"`
	// Previous is the raw statusLine value from before registration.
	Previous json.RawMessage `json:"previous,omitempty"`
	// WrittenHash identifies the settings file as wrist-quota left it.
	WrittenHash string `json:"written_hash,omitempty"`
	BackupPath  string `json:"backup_path,omitempty"`
}

type Config struct {
	Listen      string                    `json:"listen"`
	PollSeconds int                       `json:"poll_seconds"`
	Ntfy        NtfyConfig                `json:"ntfy"`
	Providers   map[string]ProviderConfig `json:"providers"`
	Rules       []Rule                    `json:"rules"`
	Statusline  StatuslineConfig          `json:"statusline"`
}

func DefaultRules() []Rule {
	return []Rule{
		{
			ID: "threshold", Kind: KindThreshold, Enabled: true, Targets: []string{},
			Levels: []int{20, 10, 5}, Priority: 4,
			Title: "{provider} {window} 残り{remaining}%", Body: "リセットまで {resets_in}",
		},
		{
			ID: "reset", Kind: KindReset, Enabled: true, Targets: []string{}, Priority: 3,
			Title: "{provider} {window} リセット", Body: "利用枠が回復しました",
		},
	}
}

// applyDefaults fills in whatever an older or hand-edited file leaves out.
func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.PollSeconds < 5 {
		c.PollSeconds = 30
	}
	if c.Ntfy.Server == "" {
		c.Ntfy.Server = DefaultServer
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	for _, p := range []string{"claude-code", "codex"} {
		if _, ok := c.Providers[p]; !ok {
			c.Providers[p] = ProviderConfig{Enabled: true}
		}
	}
	if c.Rules == nil {
		c.Rules = DefaultRules()
	}
}

// Track is the notification bookkeeping for one cycle of one window.
type Track struct {
	ResetsAt   *time.Time `json:"resets_at"`
	SequenceID string     `json:"sequence_id"`
	SentLevels []int      `json:"sent_levels"`
	// Notified is true once a threshold notification of this cycle was
	// delivered; only then is a reset notification owed.
	Notified bool `json:"notified"`
	// ResetScheduledFor is set once the reset notification is queued on the
	// ntfy server for delivery at that time.
	ResetScheduledFor *time.Time     `json:"reset_scheduled_for,omitempty"`
	ResetDone         bool           `json:"reset_done"`
	Failures          map[string]int `json:"failures,omitempty"`
}

type HistoryEntry struct {
	Time  time.Time `json:"time"`
	Kind  string    `json:"kind"`
	Title string    `json:"title,omitempty"`
	Body  string    `json:"body,omitempty"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

type ProviderStatus struct {
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type State struct {
	Windows   map[string]quota.Window   `json:"windows"`
	Tracks    map[string]*Track         `json:"tracks"`
	Providers map[string]ProviderStatus `json:"providers"`
	History   []HistoryEntry            `json:"history"`
	SentDay   string                    `json:"sent_day"`
	SentCount int                       `json:"sent_count"`
}

func (s *State) init() {
	if s.Windows == nil {
		s.Windows = map[string]quota.Window{}
	}
	if s.Tracks == nil {
		s.Tracks = map[string]*Track{}
	}
	if s.Providers == nil {
		s.Providers = map[string]ProviderStatus{}
	}
}

// AddHistory appends an entry and drops the oldest ones beyond the cap.
func (s *State) AddHistory(e HistoryEntry) {
	s.History = append(s.History, e)
	if n := len(s.History) - maxHistory; n > 0 {
		s.History = append([]HistoryEntry(nil), s.History[n:]...)
	}
}

// Store reads and writes the files in one directory.
type Store struct {
	Dir string
}

// DefaultDir returns $WRIST_QUOTA_HOME, or wrist-quota under the OS config
// directory.
func DefaultDir() (string, error) {
	if d := os.Getenv("WRIST_QUOTA_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "wrist-quota"), nil
}

func (s Store) SnapshotPath() string { return filepath.Join(s.Dir, snapshotFile) }

// LoadConfig returns the saved configuration, creating one with a fresh
// random topic on first use.
func (s Store) LoadConfig() (*Config, error) {
	var c Config
	err := s.read(configFile, &c)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c.applyDefaults()
	if err == nil {
		// A hand-made file may be readable by others; the topic in it is a secret.
		os.Chmod(filepath.Join(s.Dir, configFile), 0o600)
	}
	if c.Ntfy.Topic == "" {
		topic, terr := GenerateTopic()
		if terr != nil {
			return nil, terr
		}
		c.Ntfy.Topic = topic
		if serr := s.SaveConfig(&c); serr != nil {
			return nil, serr
		}
	}
	return &c, nil
}

func (s Store) SaveConfig(c *Config) error { return s.write(configFile, c) }

// LoadConfigReadOnly reads the configuration without creating or changing
// the file. It is for the statusline subcommand, which runs alongside the
// daemon and must never write the configuration.
func (s Store) LoadConfigReadOnly() (*Config, error) {
	var c Config
	if err := s.read(configFile, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return &c, nil
}

// SaveSnapshot stores the latest Claude Code observation for the daemon.
func (s Store) SaveSnapshot(v any) error { return s.write(snapshotFile, v) }

func (s Store) LoadState() (*State, error) {
	var st State
	err := s.read(stateFile, &st)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	st.init()
	return &st, nil
}

func (s Store) SaveState(st *State) error { return s.write(stateFile, st) }

func (s Store) read(name string, v any) error {
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", name, err)
	}
	return nil
}

func (s Store) write(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(s.Dir, name), append(b, '\n'))
}

// WriteFileAtomic writes via a temporary file and a rename, so readers never
// see a partial file. The file and its directory are private to the user.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateTopic returns a topic name that works as a shared secret: 22
// base62 characters carry about 131 bits from the system's secure source.
func GenerateTopic() (string, error) {
	out := []byte("wq-")
	limit := big.NewInt(int64(len(base62)))
	for i := 0; i < 22; i++ {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out = append(out, base62[n.Int64()])
	}
	return string(out), nil
}

// WeakTopic reports whether a user-chosen topic is easy to guess.
func WeakTopic(topic string) bool {
	if len(topic) < 16 {
		return true
	}
	seen := map[rune]bool{}
	for _, r := range topic {
		seen[r] = true
	}
	return len(seen) < 8
}
