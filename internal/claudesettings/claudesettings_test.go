package claudesettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ours = `"/opt/wrist-quota" statusline`

const existing = `{
  "language": "japanese",
  "env": {
    "A": "1"
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh", "padding": 0},
  "note": "日本語のまま <保持>"
}
`

func setup(t *testing.T, content string) (path, backup string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "settings.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path, filepath.Join(dir, "backup.json")
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallWrapsExistingStatusLineAndUninstallRestoresBytes(t *testing.T) {
	path, backup := setup(t, existing)
	cfg, err := Install(path, ours, backup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wrapped != "~/.claude/statusline.sh" || !cfg.Installed {
		t.Errorf("config = %+v", cfg)
	}
	if got, _ := Current(path); got != ours {
		t.Errorf("Current = %q; want %q", got, ours)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("written file is not JSON: %v", err)
	}
	var sl map[string]any
	json.Unmarshal(doc["statusLine"], &sl)
	if sl["padding"] != float64(0) || sl["type"] != "command" {
		t.Errorf("statusLine = %v; other options must be kept", sl)
	}
	written := read(t, path)
	if !strings.Contains(written, `"note": "日本語のまま <保持>"`) {
		t.Errorf("unrelated value was rewritten:\n%s", written)
	}
	if strings.Index(written, `"language"`) > strings.Index(written, `"env"`) || strings.Index(written, `"statusLine"`) > strings.Index(written, `"note"`) {
		t.Errorf("key order changed:\n%s", written)
	}

	if err := Uninstall(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != existing {
		t.Errorf("file after uninstall differs from the original:\n%s", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup was left behind")
	}
}

func TestUninstallKeepsEditsMadeAfterInstall(t *testing.T) {
	path, backup := setup(t, existing)
	cfg, err := Install(path, ours, backup)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(read(t, path), `"language": "japanese"`, `"language": "english"`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(path, cfg); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, `"language": "english"`) {
		t.Errorf("later edit was lost:\n%s", got)
	}
	if cmd, _ := Current(path); cmd != "~/.claude/statusline.sh" {
		t.Errorf("status line after uninstall = %q", cmd)
	}
}

func TestInstallWithoutPreviousStatusLine(t *testing.T) {
	original := "{\n  \"theme\": \"dark\"\n}\n"
	path, backup := setup(t, original)
	cfg, err := Install(path, ours, backup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wrapped != "" || len(cfg.Previous) != 0 {
		t.Errorf("config = %+v", cfg)
	}
	if err := Uninstall(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != original {
		t.Errorf("file after uninstall = %q; want %q", got, original)
	}
}

func TestInstallWithoutSettingsFile(t *testing.T) {
	path, backup := setup(t, "")
	cfg, err := Install(path, ours, backup)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Current(path); got != ours {
		t.Errorf("Current = %q", got)
	}
	if err := Uninstall(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("settings file did not exist before and must not exist after")
	}
}

func TestInstallTwiceIsRefused(t *testing.T) {
	path, backup := setup(t, existing)
	if _, err := Install(path, ours, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, ours, backup); err == nil {
		t.Error("second Install succeeded; it would wrap itself")
	}
}

func TestInvalidSettingsFileIsLeftAlone(t *testing.T) {
	for _, bad := range []string{`[1, 2]`, `{"a": `} {
		path, backup := setup(t, bad)
		if _, err := Install(path, ours, backup); err == nil {
			t.Errorf("Install accepted %q", bad)
		}
		if got := read(t, path); got != bad {
			t.Errorf("invalid file was modified: %q", got)
		}
	}
}
