// Package codex reads rate-limit numbers from Codex CLI session logs.
package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/quota"
)

const (
	Provider = "codex"
	Source   = "session-log"

	// How many of the most recently modified logs are compared. Sessions can
	// overlap, so the newest file does not always hold the newest event.
	recentFiles = 3
	chunkSize   = 64 * 1024
	maxLine     = 1 << 20
)

var marker = []byte(`"token_count"`)

type limit struct {
	UsedPercent   *float64        `json:"used_percent"`
	WindowMinutes int             `json:"window_minutes"`
	ResetsAt      json.RawMessage `json:"resets_at"`
}

type event struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *limit `json:"primary"`
			Secondary *limit `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// ParseEventLine returns the windows of one log line. ok is false for lines
// that are not token_count events or that carry no window values; such lines
// are skipped by the caller rather than treated as "no data".
func ParseEventLine(line []byte) (ws []quota.Window, ok bool) {
	if !bytes.Contains(line, marker) {
		return nil, false
	}
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, false
	}
	if ev.Type != "event_msg" || ev.Payload.Type != "token_count" || ev.Payload.RateLimits == nil {
		return nil, false
	}
	observed, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return nil, false
	}
	for _, l := range []*limit{ev.Payload.RateLimits.Primary, ev.Payload.RateLimits.Secondary} {
		if l == nil || l.UsedPercent == nil || l.WindowMinutes <= 0 {
			continue
		}
		resets, err := quota.ParseTime(l.ResetsAt)
		if err != nil {
			resets = nil
		}
		// The window is identified by its length, not by primary/secondary.
		ws = append(ws, quota.Window{
			Provider:    Provider,
			Window:      quota.WindowName(l.WindowMinutes),
			UsedPercent: *l.UsedPercent,
			ResetsAt:    resets,
			ObservedAt:  observed.UTC(),
			Source:      Source,
		})
	}
	return ws, len(ws) > 0
}

type cached struct {
	size    int64
	modTime time.Time
	windows []quota.Window
}

// Collector finds the latest valued token_count event under a Codex home.
type Collector struct {
	Home  string
	cache map[string]cached
}

// DefaultHome returns $CODEX_HOME, or ~/.codex when it is unset.
func DefaultHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

// Collect returns the windows of the newest event that carries values.
// It returns nil when no session log has one.
func (c *Collector) Collect() ([]quota.Window, error) {
	files, err := filepath.Glob(filepath.Join(c.Home, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return nil, err
	}
	type entry struct {
		path string
		info os.FileInfo
	}
	var entries []entry
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.Mode().IsRegular() {
			entries = append(entries, entry{f, info})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].info.ModTime().After(entries[j].info.ModTime()) })

	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	var best []quota.Window
	found := 0
	var firstErr error
	for _, e := range entries {
		ws, err := c.scan(e.path, e.info)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(ws) == 0 {
			continue
		}
		if best == nil || ws[0].ObservedAt.After(best[0].ObservedAt) {
			best = ws
		}
		if found++; found >= recentFiles {
			break
		}
	}
	if best == nil {
		return nil, firstErr
	}
	return best, nil
}

func (c *Collector) scan(path string, info os.FileInfo) ([]quota.Window, error) {
	if hit, ok := c.cache[path]; ok && hit.size == info.Size() && hit.modTime.Equal(info.ModTime()) {
		return hit.windows, nil
	}
	ws, err := lastValued(path)
	if err != nil {
		return nil, err
	}
	c.cache[path] = cached{size: info.Size(), modTime: info.ModTime(), windows: ws}
	return ws, nil
}

// lastValued walks the file backwards so that large logs are not read in
// full, and stops at the first event that has window values.
func lastValued(path string) ([]quota.Window, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	var tail []byte   // end of a line whose beginning lies in an earlier chunk
	skipping := false // inside a line too long to be a token_count event
	pos := info.Size()
	buf := make([]byte, chunkSize)
	for pos > 0 {
		n := int64(chunkSize)
		if pos < n {
			n = pos
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil && err != io.EOF {
			return nil, err
		}
		data := append([]byte{}, buf[:n]...)
		if skipping {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				continue
			}
			data = data[:i]
			skipping = false
		} else {
			data = append(data, tail...)
		}
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if ws, ok := ParseEventLine(data[i+1:]); ok {
				return ws, nil
			}
			data = data[:i]
		}
		tail = data
		// Conversation rows can be megabytes long. They are never parsed, so
		// stop accumulating them.
		if len(tail) > maxLine {
			tail, skipping = nil, true
		}
	}
	if !skipping {
		if ws, ok := ParseEventLine(tail); ok {
			return ws, nil
		}
	}
	return nil, nil
}
