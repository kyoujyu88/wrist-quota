package quota

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	want := time.Unix(1791339000, 0).UTC()
	for _, in := range []string{
		`1791339000`,
		`1791339000.0`,
		`"1791339000"`,
		`1791339000000`,
		`"` + want.Format(time.RFC3339) + `"`,
		`"` + want.In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339Nano) + `"`,
	} {
		got, err := ParseTime(json.RawMessage(in))
		if err != nil || got == nil || !got.Equal(want) {
			t.Errorf("ParseTime(%s) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{``, `null`, `""`} {
		if got, err := ParseTime(json.RawMessage(in)); got != nil || err != nil {
			t.Errorf("ParseTime(%q) = %v, %v; want nil, nil", in, got, err)
		}
	}
	for _, in := range []string{`"soon"`, `true`, `{}`} {
		if _, err := ParseTime(json.RawMessage(in)); err == nil {
			t.Errorf("ParseTime(%s) succeeded; want error", in)
		}
	}
}

func TestWindowName(t *testing.T) {
	for minutes, want := range map[int]string{300: "5h", 10080: "7d", 1440: "1d", 90: "90m", 0: "unknown"} {
		if got := WindowName(minutes); got != want {
			t.Errorf("WindowName(%d) = %q; want %q", minutes, got, want)
		}
	}
}

func TestStatusAt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	future, past := now.Add(time.Hour), now.Add(-time.Second)
	cases := []struct {
		name string
		w    Window
		want Status
	}{
		{"reset ahead", Window{ResetsAt: &future, ObservedAt: now.Add(-48 * time.Hour)}, StatusFresh},
		{"reset passed", Window{ResetsAt: &past, ObservedAt: now}, StatusResetEstimated},
		{"no reset, recent", Window{ObservedAt: now.Add(-time.Hour)}, StatusFresh},
		{"no reset, old", Window{ObservedAt: now.Add(-7 * time.Hour)}, StatusStale},
	}
	for _, c := range cases {
		if got := c.w.StatusAt(now); got != c.want {
			t.Errorf("%s: StatusAt = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestRemainingIsClamped(t *testing.T) {
	for used, want := range map[float64]float64{26: 74, 100: 0, 104: 0, -3: 100} {
		if got := (Window{UsedPercent: used}).Remaining(); got != want {
			t.Errorf("Remaining(used=%v) = %v; want %v", used, got, want)
		}
	}
}
