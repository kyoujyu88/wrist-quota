package rules

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/notifier/ntfy"
	"github.com/kyoujyu88/wrist-quota/internal/quota"
	"github.com/kyoujyu88/wrist-quota/internal/store"
)

type fakeNotifier struct {
	sent      []ntfy.Message
	cancelled []string
	fail      func(ntfy.Message) error
}

func (f *fakeNotifier) Send(_ context.Context, m ntfy.Message) error {
	if f.fail != nil {
		if err := f.fail(m); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeNotifier) Cancel(_ context.Context, id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeNotifier) immediate() []ntfy.Message {
	var out []ntfy.Message
	for _, m := range f.sent {
		if m.DeliverAt == nil {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeNotifier) scheduled() []ntfy.Message {
	var out []ntfy.Message
	for _, m := range f.sent {
		if m.DeliverAt != nil {
			out = append(out, m)
		}
	}
	return out
}

type harness struct {
	t   *testing.T
	now time.Time
	n   *fakeNotifier
	e   *Engine
}

var start = time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, now: start, n: &fakeNotifier{}}
	cfg := &store.Config{
		Rules:     store.DefaultRules(),
		Providers: map[string]store.ProviderConfig{"claude-code": {Enabled: true}, "codex": {Enabled: true}},
	}
	st := &store.State{
		Windows: map[string]quota.Window{}, Tracks: map[string]*store.Track{},
		Providers: map[string]store.ProviderStatus{},
	}
	h.e = &Engine{Config: cfg, State: st, Notifier: h.n, Now: func() time.Time { return h.now }, Location: time.UTC}
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

// observe reports the 5h Claude window with the given usage and reset time.
func (h *harness) observe(used float64, resetsAt time.Time) {
	h.observeWindow("claude-code", "5h", used, resetsAt)
}

func (h *harness) observeWindow(provider, window string, used float64, resetsAt time.Time) {
	h.e.Observe([]quota.Window{{
		Provider: provider, Window: window, UsedPercent: used,
		ResetsAt: &resetsAt, ObservedAt: h.now, Source: "test",
	}})
}

func (h *harness) wantImmediate(titles ...string) {
	h.t.Helper()
	got := h.n.immediate()
	if len(got) != len(titles) {
		h.t.Fatalf("immediate notifications = %d %v; want %d %v", len(got), titlesOf(got), len(titles), titles)
	}
	for i, m := range got {
		if m.Title != titles[i] {
			h.t.Errorf("notification %d title = %q; want %q", i, m.Title, titles[i])
		}
	}
}

func titlesOf(ms []ntfy.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Title
	}
	return out
}

func TestThresholdNotifiesOncePerLevel(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(104 * time.Minute)

	h.observe(50, reset)
	h.wantImmediate()

	h.observe(82, reset)
	h.wantImmediate("Claude 5h 残り18%")
	if got := h.n.immediate()[0]; got.Body != "リセットまで 1時間44分" || got.Priority != 4 {
		t.Errorf("message = %+v", got)
	}

	h.observe(83, reset)
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り18%")

	h.observe(91, reset)
	h.observe(96, reset)
	h.wantImmediate("Claude 5h 残り18%", "Claude 5h 残り9%", "Claude 5h 残り4%")
}

func TestSeveralLevelsCrossedAtOnceNotifyOnce(t *testing.T) {
	h := newHarness(t)
	h.observe(97, start.Add(time.Hour))
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り3%")
}

func TestNoDuplicateAfterRestart(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(time.Hour)
	h.observe(85, reset)
	h.wantImmediate("Claude 5h 残り15%")

	saved, err := json.Marshal(h.e.State)
	if err != nil {
		t.Fatal(err)
	}
	restored := &store.State{}
	if err := json.Unmarshal(saved, restored); err != nil {
		t.Fatal(err)
	}
	h.e.State = restored

	h.observe(86, reset)
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り15%")
	if len(h.n.scheduled()) != 1 {
		t.Errorf("scheduled = %d; want the single schedule from before the restart", len(h.n.scheduled()))
	}
}

func TestResetIsScheduledOnServerAfterThreshold(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(2 * time.Hour)
	h.observe(40, reset)
	if len(h.n.scheduled()) != 0 {
		t.Fatal("reset scheduled without a threshold notification")
	}

	h.observe(85, reset)
	sched := h.n.scheduled()
	if len(sched) != 1 {
		t.Fatalf("scheduled = %d; want 1", len(sched))
	}
	if !sched[0].DeliverAt.Equal(reset) || sched[0].SequenceID == "" || sched[0].Title != "Claude 5h リセット" {
		t.Errorf("scheduled message = %+v", sched[0])
	}

	// The machine was off across the reset. The server delivered the
	// scheduled message, so nothing more is sent, and the new cycle can
	// notify again.
	h.advance(3 * time.Hour)
	next := reset.Add(5 * time.Hour)
	h.observe(2, next)
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り15%")

	h.observe(90, next)
	h.wantImmediate("Claude 5h 残り15%", "Claude 5h 残り10%")
	if got := h.n.scheduled(); len(got) != 2 || got[1].SequenceID == got[0].SequenceID {
		t.Errorf("each cycle needs its own schedule and sequence ID: %+v", got)
	}
}

func TestResetBeyondServerLimitIsScheduledLater(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(5 * 24 * time.Hour)
	h.observeWindow("codex", "7d", 90, reset)
	h.wantImmediate("Codex 7d 残り10%")
	if len(h.n.scheduled()) != 0 {
		t.Fatal("scheduled beyond the 3-day limit")
	}

	h.advance(47 * time.Hour)
	h.e.Tick()
	if len(h.n.scheduled()) != 0 {
		t.Fatal("scheduled while still out of range")
	}

	h.advance(2 * time.Hour)
	h.e.Tick()
	h.e.Tick()
	if sched := h.n.scheduled(); len(sched) != 1 || !sched[0].DeliverAt.Equal(reset) {
		t.Fatalf("scheduled = %+v; want one message at the reset time", sched)
	}
}

func TestEarlyResetCancelsScheduleAndNotifiesNow(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(2 * time.Hour)
	h.observe(85, reset)
	seq := h.n.scheduled()[0].SequenceID

	h.advance(30 * time.Minute)
	h.observe(1, reset.Add(5*time.Hour))
	if len(h.n.cancelled) != 1 || h.n.cancelled[0] != seq {
		t.Errorf("cancelled = %v; want [%s]", h.n.cancelled, seq)
	}
	h.wantImmediate("Claude 5h 残り15%", "Claude 5h リセット")
}

func TestFailedScheduleFallsBackToSendingAtResetTime(t *testing.T) {
	h := newHarness(t)
	h.n.fail = func(m ntfy.Message) error {
		if m.DeliverAt != nil {
			return errors.New("delay not allowed")
		}
		return nil
	}
	reset := start.Add(time.Hour)
	h.observe(85, reset)
	for i := 0; i < 10; i++ {
		h.advance(time.Minute)
		h.e.Tick()
	}
	h.wantImmediate("Claude 5h 残り15%")

	h.advance(time.Hour)
	h.e.Tick()
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り15%", "Claude 5h リセット")
}

func TestFailedSendIsRetriedThenGivenUp(t *testing.T) {
	h := newHarness(t)
	failing := true
	h.n.fail = func(ntfy.Message) error {
		if failing {
			return errors.New("network down")
		}
		return nil
	}
	reset := start.Add(time.Hour)
	h.observe(85, reset)
	h.e.Tick()
	h.wantImmediate()

	failing = false
	h.e.Tick()
	h.wantImmediate("Claude 5h 残り15%")

	// A level that keeps failing is dropped after the retry limit, and no
	// reset notification follows a threshold that never arrived.
	h2 := newHarness(t)
	h2.n.fail = func(ntfy.Message) error { return errors.New("network down") }
	h2.observe(85, reset)
	for i := 0; i < 2*maxFailures; i++ {
		h2.e.Tick()
	}
	failures := 0
	for _, e := range h2.e.State.History {
		if !e.OK {
			failures++
		}
	}
	if failures != maxFailures {
		t.Errorf("failed attempts = %d; want %d", failures, maxFailures)
	}
	h2.n.fail = nil
	h2.advance(2 * time.Hour)
	h2.e.Tick()
	if len(h2.n.sent) != 0 {
		t.Errorf("sent = %v; want nothing", titlesOf(h2.n.sent))
	}
}

func TestSmallResetTimeDriftStaysInTheSameCycle(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(2 * time.Hour)
	h.observe(85, reset)

	h.observe(86, reset.Add(20*time.Second))
	h.wantImmediate("Claude 5h 残り15%")
	if len(h.n.scheduled()) != 1 {
		t.Fatalf("rescheduled for a drift below the limit: %d", len(h.n.scheduled()))
	}

	h.observe(86, reset.Add(5*time.Minute))
	sched := h.n.scheduled()
	if len(sched) != 2 || sched[1].SequenceID != sched[0].SequenceID || !sched[1].DeliverAt.Equal(reset.Add(5*time.Minute)) {
		t.Fatalf("scheduled = %+v; want a replacement with the same sequence ID", sched)
	}
	h.wantImmediate("Claude 5h 残り15%")
}

func TestDisabledRulesProvidersAndTargets(t *testing.T) {
	reset := start.Add(time.Hour)

	h := newHarness(t)
	h.e.Config.Rules[1].Enabled = false
	h.observe(85, reset)
	h.wantImmediate("Claude 5h 残り15%")
	if len(h.n.scheduled()) != 0 {
		t.Error("reset scheduled although the reset rule is disabled")
	}

	h = newHarness(t)
	h.e.Config.Providers["claude-code"] = store.ProviderConfig{Enabled: false}
	h.observe(85, reset)
	h.wantImmediate()

	h = newHarness(t)
	h.e.Config.Rules[0].Targets = []string{"codex/5h"}
	h.observe(85, reset)
	h.observeWindow("codex", "5h", 85, reset)
	h.wantImmediate("Codex 5h 残り15%")
}

func TestOlderObservationIsIgnored(t *testing.T) {
	h := newHarness(t)
	reset := start.Add(time.Hour)
	h.observe(50, reset)
	h.advance(-time.Minute)
	h.observe(99, reset)
	h.wantImmediate()
}

func TestDailyLimitStopsSending(t *testing.T) {
	h := newHarness(t)
	h.e.State.SentDay, h.e.State.SentCount = start.Format("2006-01-02"), DailyLimit
	h.observe(85, start.Add(time.Hour))
	h.wantImmediate()

	h.advance(24 * time.Hour)
	h.observe(85, start.Add(26*time.Hour))
	h.wantImmediate("Claude 5h 残り15%")
}

func TestRender(t *testing.T) {
	reset := start.Add(26*time.Hour + 30*time.Second)
	w := quota.Window{Provider: "codex", Window: "7d", UsedPercent: 81.6, ResetsAt: &reset}
	got := Render("{provider} {window} {remaining}/{used} {resets_in} {resets_at} {typo}", w, start, time.UTC)
	if want := "Codex 7d 18/82 1日2時間 10/8 05:00 {typo}"; got != want {
		t.Errorf("Render = %q; want %q", got, want)
	}
	if got := Render("{resets_in}/{resets_at}", quota.Window{}, start, time.UTC); got != "不明/不明" {
		t.Errorf("Render without reset time = %q", got)
	}
	for d, want := range map[time.Duration]string{
		0: "0分", 30 * time.Second: "1分", 59 * time.Minute: "59分", 104 * time.Minute: "1時間44分", 49 * time.Hour: "2日1時間",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q; want %q", d, got, want)
		}
	}
}
