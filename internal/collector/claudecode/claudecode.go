// Package claudecode reads the rate-limit numbers Claude Code passes to its
// status line command.
package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/quota"
)

const (
	Provider = "claude-code"
	Source   = "statusline"
)

var windowNames = map[string]string{
	"five_hour": "5h",
	"seven_day": "7d",
}

type rateLimit struct {
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

// Parse extracts the windows from one status line input. It returns no
// windows and no error when rate_limits is absent, which is normal for the
// first call of a session and for API-key usage.
func Parse(input []byte, now time.Time) ([]quota.Window, error) {
	var doc struct {
		RateLimits map[string]json.RawMessage `json:"rate_limits"`
	}
	if err := json.Unmarshal(input, &doc); err != nil {
		return nil, fmt.Errorf("status line input is not JSON: %w", err)
	}
	var out []quota.Window
	for name, raw := range doc.RateLimits {
		var rl rateLimit
		if err := json.Unmarshal(raw, &rl); err != nil || rl.UsedPercentage == nil {
			continue
		}
		resets, err := quota.ParseTime(rl.ResetsAt)
		if err != nil {
			resets = nil
		}
		label, ok := windowNames[name]
		if !ok {
			label = name
		}
		out = append(out, quota.Window{
			Provider:    Provider,
			Window:      label,
			UsedPercent: *rl.UsedPercentage,
			ResetsAt:    resets,
			ObservedAt:  now.UTC(),
			Source:      Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Window < out[j].Window })
	return out, nil
}

// Snapshot is what the statusline subcommand leaves for the daemon. It holds
// only quota numbers, never session IDs or paths from the input.
type Snapshot struct {
	ObservedAt time.Time      `json:"observed_at"`
	Windows    []quota.Window `json:"windows"`
}

// ReadSnapshot loads the file written by the statusline subcommand.
// A missing file is not an error.
func ReadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("snapshot is not JSON: %w", err)
	}
	return &s, nil
}
