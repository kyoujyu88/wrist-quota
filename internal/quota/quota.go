// Package quota defines the provider-independent model of a usage window.
package quota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Window is one rate-limit window of one provider, as last observed.
type Window struct {
	Provider    string     `json:"provider"`
	Window      string     `json:"window"`
	UsedPercent float64    `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at"`
	ObservedAt  time.Time  `json:"observed_at"`
	Source      string     `json:"source"`
}

// Key identifies a window across observations.
func (w Window) Key() string { return w.Provider + "/" + w.Window }

// Remaining returns the unused share of the window in percent.
func (w Window) Remaining() float64 {
	r := 100 - w.UsedPercent
	if r < 0 {
		return 0
	}
	if r > 100 {
		return 100
	}
	return r
}

type Status string

const (
	StatusFresh          Status = "fresh"
	StatusStale          Status = "stale"
	StatusResetEstimated Status = "reset_estimated"
)

// StaleAfter is how long an observation without a reset time stays fresh.
const StaleAfter = 6 * time.Hour

// StatusAt classifies the observation. Usage never drops on its own, so
// freshness follows resets_at; observed_at only matters when it is unknown.
func (w Window) StatusAt(now time.Time) Status {
	if w.ResetsAt != nil {
		if !now.Before(*w.ResetsAt) {
			return StatusResetEstimated
		}
		return StatusFresh
	}
	if now.Sub(w.ObservedAt) > StaleAfter {
		return StatusStale
	}
	return StatusFresh
}

// WindowName derives a short label such as "5h" or "7d" from a window length.
func WindowName(minutes int) string {
	switch {
	case minutes <= 0:
		return "unknown"
	case minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + "d"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "h"
	default:
		return strconv.Itoa(minutes) + "m"
	}
}

// ParseTime accepts Unix seconds (number or numeric string) or an ISO 8601
// string. A JSON null or empty value yields nil without an error.
func ParseTime(raw json.RawMessage) (*time.Time, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return unixSeconds(f), nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				t = t.UTC()
				return &t, nil
			}
		}
		return nil, fmt.Errorf("unrecognized time %q", s)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("unrecognized time %s", raw)
	}
	return unixSeconds(f), nil
}

func unixSeconds(f float64) *time.Time {
	// Some tools report milliseconds; anything this large cannot be seconds.
	if f > 1e11 {
		f /= 1000
	}
	t := time.Unix(int64(f), 0).UTC()
	return &t
}
