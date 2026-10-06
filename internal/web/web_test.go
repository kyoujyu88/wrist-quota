package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kyoujyu88/wrist-quota/internal/app"
)

const token = "test-token"

type fakeBackend struct {
	updates  int
	tests    []string
	actions  []string
	updateEr error
}

func (f *fakeBackend) View() app.View               { return app.View{SubscribeURL: "https://ntfy.sh/wq-secret"} }
func (f *fakeBackend) PublicConfig() app.ConfigView { return app.ConfigView{PollSeconds: 30} }
func (f *fakeBackend) UpdateConfig(app.ConfigUpdate) error {
	f.updates++
	return f.updateEr
}
func (f *fakeBackend) TestSend(_ context.Context, id string) error {
	f.tests = append(f.tests, id)
	return nil
}
func (f *fakeBackend) RegenerateTopic() error {
	f.actions = append(f.actions, "regenerate")
	return nil
}
func (f *fakeBackend) InstallStatusline() error { f.actions = append(f.actions, "install"); return nil }
func (f *fakeBackend) UninstallStatusline() error {
	f.actions = append(f.actions, "uninstall")
	return nil
}

type request struct {
	method, path, host, origin, token, body string
}

func do(t *testing.T, b Backend, r request) *http.Response {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	req.Host = r.host
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.token != "" {
		req.Header.Set(tokenHeader, r.token)
	}
	rec := httptest.NewRecorder()
	(&Server{Backend: b, Token: token}).Handler().ServeHTTP(rec, req)
	return rec.Result()
}

const (
	host   = "127.0.0.1:47821"
	origin = "http://127.0.0.1:47821"
)

func TestPageEmbedsTokenAndSetsHeaders(t *testing.T) {
	res := do(t, &fakeBackend{}, request{method: "GET", path: "/", host: host})
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), `content="`+token+`"`) {
		t.Fatalf("status %d; page does not carry the token", res.StatusCode)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS must not be allowed")
	}
	for _, path := range []string{"/app.js", "/style.css"} {
		if res := do(t, &fakeBackend{}, request{method: "GET", path: path, host: host}); res.StatusCode != 200 {
			t.Errorf("GET %s = %d", path, res.StatusCode)
		}
	}
}

func TestHostHeaderIsValidated(t *testing.T) {
	for h, want := range map[string]int{
		"127.0.0.1:47821":       200,
		"localhost:47821":       200,
		"[::1]:47821":           200,
		"evil.example:47821":    403,
		"127.0.0.1.evil.test":   403,
		"192.0.2.10:47821":      403,
		"localhost.evil.test:1": 403,
		"":                      403,
	} {
		res := do(t, &fakeBackend{}, request{method: "GET", path: "/api/state", host: h, token: token})
		if res.StatusCode != want {
			t.Errorf("Host %q: status %d; want %d", h, res.StatusCode, want)
		}
	}
}

func TestAPIRequiresToken(t *testing.T) {
	for _, tk := range []string{"", "wrong", token + "x"} {
		res := do(t, &fakeBackend{}, request{method: "GET", path: "/api/state", host: host, token: tk})
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 403 || strings.Contains(string(body), "wq-secret") {
			t.Errorf("token %q: status %d, body %s", tk, res.StatusCode, body)
		}
	}
}

func TestStateChangesRequireMatchingOrigin(t *testing.T) {
	cases := map[string]int{
		origin:                    200,
		"":                        403,
		"null":                    403,
		"http://evil.example":     403,
		"http://127.0.0.1:9999":   403,
		"https://127.0.0.1:47821": 403,
		"http://localhost:47821":  403,
	}
	for o, want := range cases {
		b := &fakeBackend{}
		res := do(t, b, request{method: "PUT", path: "/api/config", host: host, origin: o, token: token, body: `{}`})
		if res.StatusCode != want {
			t.Errorf("Origin %q: status %d; want %d", o, res.StatusCode, want)
		}
		if (b.updates == 1) != (want == 200) {
			t.Errorf("Origin %q: backend updated %d times", o, b.updates)
		}
	}
	// A valid origin does not replace the token.
	b := &fakeBackend{}
	if res := do(t, b, request{method: "POST", path: "/api/topic/regenerate", host: host, origin: origin}); res.StatusCode != 403 || len(b.actions) != 0 {
		t.Errorf("POST without token: status %d, actions %v", res.StatusCode, b.actions)
	}
}

func TestRoutes(t *testing.T) {
	b := &fakeBackend{}
	post := func(path, body string) *http.Response {
		return do(t, b, request{method: "POST", path: path, host: host, origin: origin, token: token, body: body})
	}
	if res := post("/api/test", `{"rule_id":"threshold"}`); res.StatusCode != 200 {
		t.Errorf("test send = %d", res.StatusCode)
	}
	post("/api/statusline", `{"action":"install"}`)
	post("/api/statusline", `{"action":"uninstall"}`)
	if res := post("/api/statusline", `{"action":"purge"}`); res.StatusCode != 400 {
		t.Errorf("unknown action = %d", res.StatusCode)
	}
	if res := post("/api/test", `not json`); res.StatusCode != 400 {
		t.Errorf("malformed body = %d", res.StatusCode)
	}
	if len(b.tests) != 1 || b.tests[0] != "threshold" || strings.Join(b.actions, ",") != "install,uninstall" {
		t.Errorf("tests %v, actions %v", b.tests, b.actions)
	}

	b.updateEr = errors.New("優先度は 1〜5 で指定してください")
	res := do(t, b, request{method: "PUT", path: "/api/config", host: host, origin: origin, token: token, body: `{}`})
	var out map[string]string
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 400 || out["error"] != b.updateEr.Error() {
		t.Errorf("validation error: status %d, body %v", res.StatusCode, out)
	}

	if res := do(t, b, request{method: "GET", path: "/api/unknown", host: host, token: token}); res.StatusCode != 404 {
		t.Errorf("unknown route = %d", res.StatusCode)
	}
	if res := do(t, b, request{method: "GET", path: "/static/index.html", host: host}); res.StatusCode != 404 {
		t.Errorf("raw template is reachable: %d", res.StatusCode)
	}
}

func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.0.2.10:0", ":0", "example.com:0", "nonsense"} {
		if ls, err := Listen(addr); err == nil {
			for _, l := range ls {
				l.Close()
			}
			t.Errorf("Listen(%q) succeeded", addr)
		}
	}
	ls, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		host, _, _ := strings.Cut(l.Addr().String(), ":")
		if host != "127.0.0.1" && !strings.HasPrefix(l.Addr().String(), "[::1]") {
			t.Errorf("listening on %s", l.Addr())
		}
		l.Close()
	}
}
