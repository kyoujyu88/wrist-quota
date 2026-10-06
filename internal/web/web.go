// Package web serves the local settings screen and its API on the loopback
// interface only.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/kyoujyu88/wrist-quota/internal/app"
)

//go:embed static
var staticFiles embed.FS

const (
	tokenHeader    = "X-WQ-Token"
	maxRequestBody = 1 << 20
)

type Backend interface {
	View() app.View
	PublicConfig() app.ConfigView
	UpdateConfig(app.ConfigUpdate) error
	TestSend(ctx context.Context, ruleID string) error
	RegenerateTopic() error
	InstallStatusline() error
	UninstallStatusline() error
}

type Server struct {
	Backend Backend
	// Token is generated per start and embedded in the page. Requests to
	// the API must carry it, which other local web pages cannot read.
	Token string
}

// NewToken returns a fresh random API token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Listen opens the address on the loopback interface and refuses anything
// else, so the screen is unreachable from other machines. The same port is
// also opened on the other loopback family when available.
func Listen(addr string) ([]net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q is invalid: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("listen address must be a loopback address, got %q", host)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	first, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	listeners := []net.Listener{first}
	_, actualPort, _ := net.SplitHostPort(first.Addr().String())
	other := "::1"
	if strings.Contains(host, ":") {
		other = "127.0.0.1"
	}
	if second, err := net.Listen("tcp", net.JoinHostPort(other, actualPort)); err == nil {
		listeners = append(listeners, second)
	}
	return listeners, nil
}

func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		panic(err)
	}
	index = bytes.Replace(index, []byte("{{token}}"), []byte(html.EscapeString(s.Token)), 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	files := http.FileServerFS(static)
	mux.Handle("GET /app.js", files)
	mux.Handle("GET /style.css", files)

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Backend.View())
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Backend.PublicConfig())
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var u app.ConfigUpdate
		if !readJSON(w, r, &u) {
			return
		}
		if err := s.Backend.UpdateConfig(u); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Backend.PublicConfig())
	})
	mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RuleID string `json:"rule_id"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := s.Backend.TestSend(r.Context(), req.RuleID); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/topic/regenerate", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Backend.RegenerateTopic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Backend.PublicConfig())
	})
	mux.HandleFunc("POST /api/statusline", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		var err error
		switch req.Action {
		case "install":
			err = s.Backend.InstallStatusline()
		case "uninstall":
			err = s.Backend.UninstallStatusline()
		default:
			err = errors.New("action は install か uninstall を指定してください")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Backend.View().Statusline)
	})
	return s.guard(mux)
}

// guard enforces the access rules before any handler runs.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")

		// A page on another site that resolves its own name to 127.0.0.1
		// still sends that name as Host.
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, errors.New("この Host からのアクセスは受け付けません"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || origin.Scheme != "http" || origin.Host != r.Host {
				writeError(w, http.StatusForbidden, errors.New("この Origin からの操作は受け付けません"))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			got := r.Header.Get(tokenHeader)
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeError(w, http.StatusForbidden, errors.New("トークンが一致しません。ページを再読み込みしてください"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("リクエストの形式が正しくありません"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
