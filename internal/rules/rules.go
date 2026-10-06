// Package rules decides which notifications an observation calls for and
// keeps the bookkeeping that stops them from repeating.
package rules

import (
	"context"
	"sort"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/notifier/ntfy"
	"github.com/kyoujyu88/wrist-quota/internal/quota"
	"github.com/kyoujyu88/wrist-quota/internal/store"
)

const (
	// Reset times of one cycle may differ slightly between observations.
	// A reset time further away than this belongs to another cycle.
	cycleTolerance = 10 * time.Minute
	// A scheduled reset notification is replaced only when the reset time
	// moved by more than this.
	rescheduleDrift = time.Minute
	// Keeps a schedule request safely inside the server's maximum delay.
	scheduleMargin = time.Minute
	// Attempts per notification before giving up. Retries happen on later
	// ticks, which spaces them by the polling interval.
	maxFailures = 5
	// Stays below the 250 messages a day ntfy.sh accepts from one visitor.
	DailyLimit = 200

	sendTimeout = 20 * time.Second

	failThreshold = "threshold"
	failReset     = "reset"
	failSchedule  = "schedule"
)

type Notifier interface {
	Send(ctx context.Context, m ntfy.Message) error
	Cancel(ctx context.Context, sequenceID string) error
}

// Engine is not safe for concurrent use; the caller serializes access and
// saves State after each call.
type Engine struct {
	Config   *store.Config
	State    *store.State
	Notifier Notifier
	Now      func() time.Time
	Location *time.Location
}

// Observe records new observations and evaluates the rules for them.
func (e *Engine) Observe(ws []quota.Window) {
	for _, w := range ws {
		key := w.Key()
		if prev, ok := e.State.Windows[key]; ok && w.ObservedAt.Before(prev.ObservedAt) {
			continue
		}
		e.State.Windows[key] = w
		e.syncCycle(key, w)
		e.evaluate(key)
	}
}

// Tick re-evaluates every known window against the clock: it retries failed
// sends, schedules resets that came into range and reports resets that
// happened while nothing was observed.
func (e *Engine) Tick() {
	keys := make([]string, 0, len(e.State.Windows))
	for key := range e.State.Windows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if e.State.Tracks[key] == nil {
			e.State.Tracks[key] = newTrack(e.State.Windows[key])
		}
		e.evaluate(key)
	}
}

func newTrack(w quota.Window) *store.Track {
	t := &store.Track{ResetsAt: w.ResetsAt}
	t.SequenceID = sequenceID(w, t.ResetsAt)
	return t
}

// sequenceID is fixed for a cycle, so rescheduling replaces the pending
// message, and differs between cycles, so a new reset notification is not
// treated as an update of an old one.
func sequenceID(w quota.Window, resetsAt *time.Time) string {
	id := "wq-reset-" + w.Provider + "-" + w.Window
	if resetsAt != nil {
		id += "-" + resetsAt.UTC().Format("20060102T150405")
	}
	return id
}

func (e *Engine) syncCycle(key string, w quota.Window) {
	t := e.State.Tracks[key]
	if t == nil {
		e.State.Tracks[key] = newTrack(w)
		return
	}
	if w.ResetsAt == nil {
		return
	}
	if t.ResetsAt == nil {
		t.ResetsAt = w.ResetsAt
		t.SequenceID = sequenceID(w, w.ResetsAt)
		return
	}
	drift := w.ResetsAt.Sub(*t.ResetsAt)
	if drift > cycleTolerance {
		e.finishCycle(key, t, w)
		e.State.Tracks[key] = newTrack(w)
		return
	}
	if drift == 0 {
		return
	}
	t.ResetsAt = w.ResetsAt
	if t.ResetScheduledFor != nil && absDuration(w.ResetsAt.Sub(*t.ResetScheduledFor)) > rescheduleDrift {
		// evaluate schedules again with the same sequence ID, which replaces
		// the pending message on the server.
		t.ResetScheduledFor = nil
	}
}

// finishCycle runs when an observation shows a later cycle. It delivers the
// reset notification unless the server already has or will have done so.
func (e *Engine) finishCycle(key string, t *store.Track, next quota.Window) {
	if !t.Notified || t.ResetDone {
		return
	}
	rule := e.rule(store.KindReset, key)
	now := e.Now()
	if t.ResetScheduledFor != nil {
		if !now.Before(*t.ResetScheduledFor) {
			return
		}
		// The window reset before the scheduled time.
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		err := e.Notifier.Cancel(ctx, t.SequenceID)
		cancel()
		if err != nil {
			e.State.AddHistory(store.HistoryEntry{Time: now, Kind: "reset_cancel", Error: err.Error()})
		}
	}
	if rule == nil {
		return
	}
	e.send(t, failReset, "reset", e.resetMessage(*rule, next))
}

func (e *Engine) evaluate(key string) {
	w := e.State.Windows[key]
	t := e.State.Tracks[key]
	if p, ok := e.Config.Providers[w.Provider]; ok && !p.Enabled {
		return
	}
	ended := w.ResetsAt != nil && !e.Now().Before(*w.ResetsAt)
	if !ended {
		e.checkThresholds(key, w, t)
	}
	e.checkReset(key, w, t, ended)
}

func (e *Engine) checkThresholds(key string, w quota.Window, t *store.Track) {
	levels := map[int]store.Rule{}
	for _, r := range e.Config.Rules {
		if r.Kind != store.KindThreshold || !r.Enabled || !r.AppliesTo(key) {
			continue
		}
		for _, l := range r.Levels {
			if _, taken := levels[l]; !taken {
				levels[l] = r
			}
		}
	}
	remaining := w.Remaining()
	var crossed []int
	for l := range levels {
		if remaining <= float64(l) && !contains(t.SentLevels, l) {
			crossed = append(crossed, l)
		}
	}
	if len(crossed) == 0 {
		return
	}
	sort.Ints(crossed)
	// Several levels crossed at once produce one notification, for the
	// lowest of them.
	rule := levels[crossed[0]]
	msg := ntfy.Message{
		Title:    Render(rule.Title, w, e.Now(), e.Location),
		Body:     Render(rule.Body, w, e.Now(), e.Location),
		Priority: rule.Priority,
	}
	delivered, settled := e.send(t, failThreshold, "threshold", msg)
	if settled {
		t.SentLevels = append(t.SentLevels, crossed...)
		sort.Ints(t.SentLevels)
	}
	if delivered {
		t.Notified = true
	}
}

func (e *Engine) checkReset(key string, w quota.Window, t *store.Track, ended bool) {
	if !t.Notified || t.ResetDone || t.ResetsAt == nil {
		return
	}
	rule := e.rule(store.KindReset, key)
	if rule == nil {
		return
	}
	now := e.Now()
	if ended {
		if t.ResetScheduledFor != nil {
			// The server delivers the scheduled message.
			t.ResetDone = true
			return
		}
		if _, settled := e.send(t, failReset, "reset", e.resetMessage(*rule, w)); settled {
			t.ResetDone = true
		}
		return
	}
	if t.ResetScheduledFor != nil || t.Failures[failSchedule] >= maxFailures {
		return
	}
	until := t.ResetsAt.Sub(now)
	if until < ntfy.MinDelay || until > ntfy.MaxDelay-scheduleMargin {
		return
	}
	msg := e.resetMessage(*rule, w)
	msg.DeliverAt = t.ResetsAt
	msg.SequenceID = t.SequenceID
	if delivered, _ := e.send(t, failSchedule, "reset_scheduled", msg); delivered {
		at := *t.ResetsAt
		t.ResetScheduledFor = &at
	}
}

func (e *Engine) resetMessage(rule store.Rule, w quota.Window) ntfy.Message {
	// A reset notification describes the window after the reset.
	w.UsedPercent = 0
	return ntfy.Message{
		Title:    Render(rule.Title, w, e.Now(), e.Location),
		Body:     Render(rule.Body, w, e.Now(), e.Location),
		Priority: rule.Priority,
	}
}

// send delivers one message and records the outcome. settled is true when
// the message was delivered or the retries are used up.
func (e *Engine) send(t *store.Track, failKey, kind string, m ntfy.Message) (delivered, settled bool) {
	now := e.Now()
	entry := store.HistoryEntry{Time: now, Kind: kind, Title: m.Title, Body: m.Body}

	var err error
	if day := now.UTC().Format("2006-01-02"); e.State.SentDay != day {
		e.State.SentDay, e.State.SentCount = day, 0
	}
	if e.State.SentCount >= DailyLimit {
		err = errDailyLimit
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		err = e.Notifier.Send(ctx, m)
		cancel()
		e.State.SentCount++
	}

	if err == nil {
		delete(t.Failures, failKey)
		entry.OK = true
		e.State.AddHistory(entry)
		return true, true
	}
	if t.Failures == nil {
		t.Failures = map[string]int{}
	}
	t.Failures[failKey]++
	entry.Error = err.Error()
	settled = t.Failures[failKey] >= maxFailures
	if settled {
		entry.Error += "（再試行の上限に達したため送信を諦めました）"
	}
	e.State.AddHistory(entry)
	return false, settled
}

type limitError struct{}

func (limitError) Error() string { return "1 日の送信上限に達しました" }

var errDailyLimit = limitError{}

func (e *Engine) rule(kind, key string) *store.Rule {
	for i, r := range e.Config.Rules {
		if r.Kind == kind && r.Enabled && r.AppliesTo(key) {
			return &e.Config.Rules[i]
		}
	}
	return nil
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
