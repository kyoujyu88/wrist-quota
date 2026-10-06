// Package ntfy sends notifications through an ntfy server using its JSON
// publishing API.
package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// Limits of the publishing API. Longer values are cut rather than
	// rejected by the server.
	maxTitleBytes = 1024
	maxBodyBytes  = 4096

	// Bounds the server accepts for scheduled delivery by default.
	MinDelay = 10 * time.Second
	MaxDelay = 3 * 24 * time.Hour
)

type Message struct {
	Title    string
	Body     string
	Priority int
	Tags     []string
	// DeliverAt schedules the message on the server instead of sending now.
	DeliverAt *time.Time
	// SequenceID lets a scheduled message be replaced or cancelled later.
	SequenceID string
}

type Client struct {
	Server string
	Topic  string
	Token  string
	HTTP   *http.Client
}

type payload struct {
	Topic      string   `json:"topic"`
	Title      string   `json:"title,omitempty"`
	Message    string   `json:"message,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Delay      string   `json:"delay,omitempty"`
	SequenceID string   `json:"sequence_id,omitempty"`
}

// Send publishes one message. Non-ASCII text travels in the JSON body, which
// avoids the header encoding problems of the plain publishing API.
func (c *Client) Send(ctx context.Context, m Message) error {
	base, err := c.base()
	if err != nil {
		return err
	}
	p := payload{
		Topic:      c.Topic,
		Title:      truncate(m.Title, maxTitleBytes),
		Message:    truncate(m.Body, maxBodyBytes),
		Tags:       m.Tags,
		SequenceID: m.SequenceID,
	}
	if m.Priority >= 1 && m.Priority <= 5 {
		p.Priority = m.Priority
	}
	if m.DeliverAt != nil {
		p.Delay = strconv.FormatInt(m.DeliverAt.Unix(), 10)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return c.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// Cancel removes a scheduled message that has not been delivered yet.
func (c *Client) Cancel(ctx context.Context, sequenceID string) error {
	base, err := c.base()
	if err != nil {
		return err
	}
	u := base.JoinPath(url.PathEscape(c.Topic), url.PathEscape(sequenceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return c.scrub(err)
	}
	return c.do(req)
}

func (c *Client) base() (*url.URL, error) {
	if c.Topic == "" {
		return nil, fmt.Errorf("ntfy topic is not set")
	}
	u, err := url.Parse(strings.TrimRight(c.Server, "/") + "/")
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("ntfy server URL is invalid")
	}
	return u, nil
}

func (c *Client) do(req *http.Request) error {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	var apiErr struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	json.Unmarshal(b, &apiErr)
	return &StatusError{Code: resp.StatusCode, Detail: c.redact(apiErr.Error)}
}

// StatusError is a non-2xx answer from the server.
type StatusError struct {
	Code   int
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("ntfy server answered %d: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("ntfy server answered %d", e.Code)
}

// redact keeps the topic and the token out of anything that may be logged or
// shown: the topic is the only thing protecting the subscription.
func (c *Client) redact(msg string) string {
	for _, secret := range []string{c.Topic, url.PathEscape(c.Topic), c.Token} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "***")
		}
	}
	return msg
}

func (c *Client) scrub(err error) error {
	return errors.New(c.redact(err.Error()))
}

// truncate cuts s to at most max bytes without splitting a character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
