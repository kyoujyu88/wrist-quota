package claudecode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Unix(1791323253, 0)

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "claudecode", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRealSample(t *testing.T) {
	ws, err := Parse(readSample(t, "statusline_after_first_response.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Fatalf("got %d windows; want 2", len(ws))
	}
	five, seven := ws[0], ws[1]
	if five.Window != "5h" || five.UsedPercent != 26 || five.ResetsAt.Unix() != 1791339000 {
		t.Errorf("5h window = %+v", five)
	}
	if seven.Window != "7d" || seven.UsedPercent != 11 || seven.ResetsAt.Unix() != 1791878400 {
		t.Errorf("7d window = %+v", seven)
	}
	if five.Provider != Provider || five.Source != Source || !five.ObservedAt.Equal(now) {
		t.Errorf("metadata = %+v", five)
	}
}

func TestParseWithoutRateLimits(t *testing.T) {
	ws, err := Parse(readSample(t, "statusline_before_first_request.json"), now)
	if err != nil || len(ws) != 0 {
		t.Fatalf("Parse = %v, %v; want no windows and no error", ws, err)
	}
}

func TestParseVariants(t *testing.T) {
	cases := map[string]struct {
		in      string
		windows int
		resets  bool
	}{
		"iso reset time":    {`{"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":"2026-10-07T03:30:00Z"}}}`, 1, true},
		"null reset time":   {`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":null}}}`, 1, false},
		"bad reset time":    {`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":"later"}}}`, 1, false},
		"missing usage":     {`{"rate_limits":{"five_hour":{"resets_at":1791339000}}}`, 0, false},
		"null rate limits":  {`{"rate_limits":null}`, 0, false},
		"null window":       {`{"rate_limits":{"five_hour":null}}`, 0, false},
		"unknown window":    {`{"rate_limits":{"seven_day_opus":{"used_percentage":5,"resets_at":1791878400}}}`, 1, true},
		"wrong window type": {`{"rate_limits":{"five_hour":"n/a"}}`, 0, false},
	}
	for name, c := range cases {
		ws, err := Parse([]byte(c.in), now)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(ws) != c.windows {
			t.Errorf("%s: got %d windows; want %d", name, len(ws), c.windows)
			continue
		}
		if c.windows == 1 && (ws[0].ResetsAt != nil) != c.resets {
			t.Errorf("%s: resets_at = %v", name, ws[0].ResetsAt)
		}
	}
	if _, err := Parse([]byte("not json"), now); err == nil {
		t.Error("Parse accepted non-JSON input")
	}
}
