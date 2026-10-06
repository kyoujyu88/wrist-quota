package ntfy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const topic = "wq-0123456789abcdefABCDEF"

type recorded struct {
	method, path, auth, contentType string
	body                            map[string]any
}

func server(t *testing.T, status int, reply string) (*httptest.Server, *[]recorded) {
	t.Helper()
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &rec.body); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
		}
		got = append(got, rec)
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestSendPostsJSONToServerRoot(t *testing.T) {
	srv, got := server(t, 200, `{}`)
	c := &Client{Server: srv.URL + "/", Topic: topic, Token: "tk_secret"}
	at := time.Unix(1791339000, 0)
	err := c.Send(context.Background(), Message{
		Title: "Claude 5h 残り18%", Body: "リセットまで 1時間44分", Priority: 4,
		Tags: []string{"hourglass"}, DeliverAt: &at, SequenceID: "wq-reset-claude-code-5h",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != "POST" || r.path != "/" || r.auth != "Bearer tk_secret" || r.contentType != "application/json" {
		t.Errorf("request = %+v", r)
	}
	want := map[string]any{
		"topic": topic, "title": "Claude 5h 残り18%", "message": "リセットまで 1時間44分",
		"priority": float64(4), "delay": "1791339000", "sequence_id": "wq-reset-claude-code-5h",
	}
	for k, v := range want {
		if r.body[k] != v {
			t.Errorf("body[%q] = %v; want %v", k, r.body[k], v)
		}
	}
	if tags, _ := r.body["tags"].([]any); len(tags) != 1 || tags[0] != "hourglass" {
		t.Errorf("tags = %v", r.body["tags"])
	}
}

func TestSendOmitsOptionalFields(t *testing.T) {
	srv, got := server(t, 200, `{}`)
	c := &Client{Server: srv.URL, Topic: topic}
	if err := c.Send(context.Background(), Message{Title: "t", Priority: 9}); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.auth != "" {
		t.Errorf("Authorization sent without a token: %q", r.auth)
	}
	for _, k := range []string{"delay", "sequence_id", "priority", "tags", "message"} {
		if _, ok := r.body[k]; ok {
			t.Errorf("body contains %q: %v", k, r.body)
		}
	}
}

func TestCancelDeletesSequence(t *testing.T) {
	srv, got := server(t, 200, `{}`)
	c := &Client{Server: srv.URL, Topic: topic}
	if err := c.Cancel(context.Background(), "wq-reset-codex-7d"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.method != "DELETE" || r.path != "/"+topic+"/wq-reset-codex-7d" {
		t.Errorf("request = %+v", r)
	}
}

func TestErrorsNeverContainTopicOrToken(t *testing.T) {
	srv, _ := server(t, 429, `{"code":42901,"http":429,"error":"limit reached for topic `+topic+`"}`)
	c := &Client{Server: srv.URL, Topic: topic, Token: "tk_secret"}
	err := c.Send(context.Background(), Message{Title: "t"})
	if err == nil {
		t.Fatal("no error for a 429 answer")
	}
	if se, ok := err.(*StatusError); !ok || se.Code != 429 {
		t.Errorf("error = %#v; want a StatusError with code 429", err)
	}

	srv.Close()
	transportErr := c.Cancel(context.Background(), "seq")
	if transportErr == nil {
		t.Fatal("no error for an unreachable server")
	}
	for _, e := range []error{err, transportErr} {
		if strings.Contains(e.Error(), topic) || strings.Contains(e.Error(), "tk_secret") {
			t.Errorf("error leaks a secret: %v", e)
		}
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for name, c := range map[string]*Client{
		"no topic":   {Server: "https://ntfy.sh"},
		"no scheme":  {Server: "ntfy.sh", Topic: topic},
		"bad scheme": {Server: "ftp://ntfy.sh", Topic: topic},
	} {
		if err := c.Send(context.Background(), Message{Title: "t"}); err == nil {
			t.Errorf("%s: Send succeeded", name)
		}
	}
}

func TestLongTextIsCutOnCharacterBoundary(t *testing.T) {
	srv, got := server(t, 200, `{}`)
	c := &Client{Server: srv.URL, Topic: topic}
	if err := c.Send(context.Background(), Message{Title: strings.Repeat("残", 600), Body: strings.Repeat("量", 3000)}); err != nil {
		t.Fatal(err)
	}
	title, _ := (*got)[0].body["title"].(string)
	body, _ := (*got)[0].body["message"].(string)
	if len(title) > maxTitleBytes || len(body) > maxBodyBytes || !utf8.ValidString(title) || !utf8.ValidString(body) {
		t.Errorf("title %d bytes, body %d bytes", len(title), len(body))
	}
	if !strings.HasSuffix(title, "…") {
		t.Errorf("cut title does not end with an ellipsis")
	}
}
