package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleLines(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "codex", "token_count_events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSpace(b), []byte("\n"))
}

func TestParseEventLineRealSamples(t *testing.T) {
	lines := sampleLines(t)

	ws, ok := ParseEventLine(lines[0])
	if !ok || len(ws) != 2 {
		t.Fatalf("line 1: ok=%v windows=%d", ok, len(ws))
	}
	if ws[0].Window != "5h" || ws[0].UsedPercent != 0 || ws[0].ResetsAt.Unix() != 1790938533 {
		t.Errorf("5h window = %+v", ws[0])
	}
	if ws[1].Window != "7d" || ws[1].UsedPercent != 32 || ws[1].ResetsAt.Unix() != 1791407845 {
		t.Errorf("7d window = %+v", ws[1])
	}
	if want := time.Date(2026, 10, 2, 6, 15, 50, 74_000_000, time.UTC); !ws[0].ObservedAt.Equal(want) {
		t.Errorf("observed_at = %v; want %v", ws[0].ObservedAt, want)
	}

	if _, ok := ParseEventLine(lines[2]); ok {
		t.Error("line 3 has null windows and must be skipped")
	}
}

func TestParseEventLineVariants(t *testing.T) {
	const head = `{"timestamp":"2026-10-02T06:15:50.074Z","type":"event_msg","payload":{"type":"token_count","rate_limits":`
	swapped := head + `{"primary":{"used_percent":19,"window_minutes":10080,"resets_at":2},"secondary":{"used_percent":90,"window_minutes":300,"resets_at":1}}}}`
	ws, ok := ParseEventLine([]byte(swapped))
	if !ok || ws[0].Window != "7d" || ws[1].Window != "5h" || ws[1].UsedPercent != 90 {
		t.Errorf("swapped positions: ok=%v windows=%+v", ok, ws)
	}

	onlyOne := head + `{"primary":null,"secondary":{"used_percent":7,"window_minutes":10080,"resets_at":null}}}}`
	ws, ok = ParseEventLine([]byte(onlyOne))
	if !ok || len(ws) != 1 || ws[0].ResetsAt != nil {
		t.Errorf("one window: ok=%v windows=%+v", ok, ws)
	}

	for name, line := range map[string]string{
		"conversation row":   `{"timestamp":"2026-10-02T06:15:50Z","type":"response_item","payload":{"text":"about \"token_count\" events"}}`,
		"null rate limits":   head + `null}}`,
		"broken json":        head + `{"primary":`,
		"missing timestamp":  `{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":1,"window_minutes":300}}}}`,
		"other event":        `{"timestamp":"2026-10-02T06:15:50Z","type":"event_msg","payload":{"type":"agent_message"}}`,
		"zero window length": head + `{"primary":{"used_percent":1,"window_minutes":0}}}}`,
	} {
		if _, ok := ParseEventLine([]byte(line)); ok {
			t.Errorf("%s: line was accepted", name)
		}
	}
}

func writeLog(t *testing.T, home, day, name string, mod time.Time, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectSkipsTrailingNullEvent(t *testing.T) {
	lines := sampleLines(t)
	home := t.TempDir()
	conversation := `{"timestamp":"2026-10-02T06:00:00Z","type":"response_item","payload":{"text":"` + strings.Repeat("x", 3*chunkSize) + `"}}`
	// Real logs end with a null "premium" event after valued "codex" events.
	writeLog(t, home, "02", "rollout-a.jsonl", time.Unix(2000, 0),
		string(lines[1]), conversation, string(lines[0]), conversation, string(lines[2]))

	ws, err := (&Collector{Home: home}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[1].UsedPercent != 32 {
		t.Fatalf("windows = %+v; want the values of the last valued event", ws)
	}
}

func TestCollectPrefersNewestEventAcrossFiles(t *testing.T) {
	lines := sampleLines(t)
	home := t.TempDir()
	// The older event sits in the more recently modified file.
	writeLog(t, home, "01", "rollout-old-event.jsonl", time.Unix(3000, 0), string(lines[1]))
	writeLog(t, home, "02", "rollout-new-event.jsonl", time.Unix(2000, 0), string(lines[0]))

	ws, err := (&Collector{Home: home}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[1].UsedPercent != 32 {
		t.Fatalf("windows = %+v; want the event with the latest timestamp", ws)
	}
}

func TestCollectWithoutData(t *testing.T) {
	lines := sampleLines(t)
	home := t.TempDir()
	c := &Collector{Home: home}
	if ws, err := c.Collect(); ws != nil || err != nil {
		t.Fatalf("empty home: %v, %v", ws, err)
	}
	writeLog(t, home, "02", "rollout-a.jsonl", time.Unix(2000, 0), string(lines[2]))
	if ws, err := c.Collect(); ws != nil || err != nil {
		t.Fatalf("only null events: %v, %v", ws, err)
	}
}

func TestCollectSkipsOversizedLines(t *testing.T) {
	lines := sampleLines(t)
	home := t.TempDir()
	huge := `{"type":"response_item","payload":{"text":"` + strings.Repeat("y", maxLine+chunkSize) + `"}}`
	writeLog(t, home, "02", "rollout-a.jsonl", time.Unix(2000, 0), string(lines[0]), huge)

	ws, err := (&Collector{Home: home}).Collect()
	if err != nil || len(ws) != 2 {
		t.Fatalf("Collect = %+v, %v", ws, err)
	}
}

func TestCollectRereadsChangedFile(t *testing.T) {
	lines := sampleLines(t)
	home := t.TempDir()
	c := &Collector{Home: home}
	writeLog(t, home, "02", "rollout-a.jsonl", time.Unix(2000, 0), string(lines[1]))
	if ws, _ := c.Collect(); len(ws) != 2 || ws[1].UsedPercent != 33 {
		t.Fatalf("first read = %+v", ws)
	}
	writeLog(t, home, "02", "rollout-a.jsonl", time.Unix(2100, 0), string(lines[1]), string(lines[0]))
	if ws, _ := c.Collect(); len(ws) != 2 || ws[1].UsedPercent != 32 {
		t.Fatalf("after append = %+v", ws)
	}
}
