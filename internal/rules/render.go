package rules

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/quota"
)

var providerNames = map[string]string{
	"claude-code": "Claude",
	"codex":       "Codex",
}

// ProviderName returns the short display name used in notifications.
func ProviderName(provider string) string {
	if n, ok := providerNames[provider]; ok {
		return n
	}
	return provider
}

// Render fills a message template. Unknown placeholders are left as they are
// so that a typo stays visible in the test notification.
func Render(tmpl string, w quota.Window, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	resetsIn, resetsAt := "不明", "不明"
	if w.ResetsAt != nil {
		resetsIn = FormatDuration(w.ResetsAt.Sub(now))
		resetsAt = formatClock(*w.ResetsAt, now, loc)
	}
	return strings.NewReplacer(
		"{provider}", ProviderName(w.Provider),
		"{window}", w.Window,
		"{remaining}", strconv.Itoa(int(math.Round(w.Remaining()))),
		"{used}", strconv.Itoa(int(math.Round(100-w.Remaining()))),
		"{resets_in}", resetsIn,
		"{resets_at}", resetsAt,
	).Replace(tmpl)
}

// FormatDuration renders a time span the way a watch face has room for,
// rounding up to whole minutes.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0分"
	}
	minutes := int(math.Ceil(d.Minutes()))
	days, hours, mins := minutes/1440, minutes%1440/60, minutes%60
	switch {
	case days > 0:
		return strconv.Itoa(days) + "日" + strconv.Itoa(hours) + "時間"
	case hours > 0:
		return strconv.Itoa(hours) + "時間" + strconv.Itoa(mins) + "分"
	default:
		return strconv.Itoa(mins) + "分"
	}
}

func formatClock(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("1/2 15:04")
}
