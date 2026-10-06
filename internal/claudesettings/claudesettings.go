// Package claudesettings registers and removes wrist-quota as Claude Code's
// status line command, leaving the rest of the settings file untouched.
package claudesettings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kyoujyu88/wrist-quota/internal/store"
)

const key = "statusLine"

// DefaultPath returns Claude Code's user settings file.
func DefaultPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

type statusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// Current returns the configured status line command, or "" when none is set.
func Current(path string) (string, error) {
	obj, _, err := load(path)
	if err != nil {
		return "", err
	}
	raw, ok := obj.get(key)
	if !ok {
		return "", nil
	}
	var sl statusLine
	if err := json.Unmarshal(raw, &sl); err != nil {
		return "", nil
	}
	return sl.Command, nil
}

// Install points the status line at command and returns what is needed to
// undo it. The previous file is copied to backupPath first. An existing
// status line keeps running: its command is returned as Wrapped and its
// other options (padding and the like) stay in place.
func Install(path, command, backupPath string) (store.StatuslineConfig, error) {
	obj, original, err := load(path)
	if err != nil {
		return store.StatuslineConfig{}, err
	}
	cfg := store.StatuslineConfig{Installed: true}

	next := map[string]json.RawMessage{}
	if raw, ok := obj.get(key); ok {
		var sl statusLine
		if json.Unmarshal(raw, &sl) == nil && sl.Command == command {
			return store.StatuslineConfig{}, errors.New("wrist-quota is already registered as the status line")
		}
		cfg.Previous = append(json.RawMessage(nil), raw...)
		if json.Unmarshal(raw, &next) == nil && sl.Type == "command" {
			cfg.Wrapped = sl.Command
		} else {
			next = map[string]json.RawMessage{}
		}
	}
	next["type"] = json.RawMessage(`"command"`)
	next["command"], _ = json.Marshal(command)
	value, err := json.Marshal(next)
	if err != nil {
		return store.StatuslineConfig{}, err
	}
	obj.set(key, value)

	if original != nil {
		if err := store.WriteFileAtomic(backupPath, original); err != nil {
			return store.StatuslineConfig{}, fmt.Errorf("cannot back up %s: %w", filepath.Base(path), err)
		}
		cfg.BackupPath = backupPath
	}
	written := obj.marshal()
	if err := store.WriteFileAtomic(path, written); err != nil {
		return store.StatuslineConfig{}, err
	}
	cfg.WrittenHash = hash(written)
	return cfg, nil
}

// Uninstall undoes Install. If the file is exactly as Install left it, the
// backup is restored byte for byte. If it was edited since, only the status
// line entry is put back so that the later edits survive.
func Uninstall(path string, cfg store.StatuslineConfig) error {
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if current != nil && hash(current) == cfg.WrittenHash {
		if cfg.BackupPath == "" {
			// There was no settings file before registration.
			return os.Remove(path)
		}
		backup, err := os.ReadFile(cfg.BackupPath)
		if err == nil {
			if err := store.WriteFileAtomic(path, backup); err != nil {
				return err
			}
			os.Remove(cfg.BackupPath)
			return nil
		}
	}

	obj, _, err := load(path)
	if err != nil {
		return err
	}
	if len(cfg.Previous) > 0 {
		obj.set(key, cfg.Previous)
	} else {
		obj.remove(key)
	}
	if err := store.WriteFileAtomic(path, obj.marshal()); err != nil {
		return err
	}
	if cfg.BackupPath != "" {
		os.Remove(cfg.BackupPath)
	}
	return nil
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// object is a JSON object that keeps the order and the exact text of its
// members, so that rewriting the file changes nothing but the edited key.
type object struct {
	keys   []string
	values map[string]json.RawMessage
}

func load(path string) (obj *object, original []byte, err error) {
	original, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return &object{values: map[string]json.RawMessage{}}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	obj = &object{values: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(original)) == 0 {
		return obj, original, nil
	}
	dec := json.NewDecoder(bytes.NewReader(original))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, fmt.Errorf("%s is not a JSON object", filepath.Base(path))
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("%s is not valid JSON: %w", filepath.Base(path), err)
		}
		name, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, fmt.Errorf("%s is not valid JSON: %w", filepath.Base(path), err)
		}
		obj.set(name, raw)
	}
	return obj, original, nil
}

func (o *object) get(name string) (json.RawMessage, bool) {
	v, ok := o.values[name]
	return v, ok
}

func (o *object) set(name string, v json.RawMessage) {
	if _, ok := o.values[name]; !ok {
		o.keys = append(o.keys, name)
	}
	o.values[name] = v
}

func (o *object) remove(name string) {
	if _, ok := o.values[name]; !ok {
		return
	}
	delete(o.values, name)
	for i, k := range o.keys {
		if k == name {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) marshal() []byte {
	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  ")
		name, _ := json.Marshal(k)
		buf.Write(name)
		buf.WriteString(": ")
		var value bytes.Buffer
		if err := json.Indent(&value, o.values[k], "  ", "  "); err != nil {
			value.Reset()
			value.Write(o.values[k])
		}
		buf.Write(value.Bytes())
	}
	if len(o.keys) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes()
}
