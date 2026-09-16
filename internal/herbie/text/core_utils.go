package text

import (
	"fmt"
	"time"
)

// Plural returns the "s" suffix for a count.
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func FormatTokens(n int64) string {
	if n < 0 {
		return "?"
	}
	if n < 1024 {
		return fmt.Sprint(n)
	}
	if n < 10*1024 {
		return fmt.Sprintf("%.1fk", float64(n)/1024)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%dk", n/1024+(n%1024)/512)
	}
	if n < 10*1024*1024 {
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%dM", n/(1024*1024)+(n%(1024*1024))/(1024*1024/2))
}
func roundedSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d/time.Second + d%time.Second/(500*time.Millisecond))
}
func FormatDuration(d time.Duration) string {
	sec := roundedSeconds(d)
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 && sec%60 == 0 {
		return fmt.Sprintf("%dm", sec/60)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
	}
	if sec%3600 == 0 {
		return fmt.Sprintf("%dh", sec/3600)
	}
	return fmt.Sprintf("%dh %02dm", sec/3600, (sec%3600)/60)
}
func FormatContext(used, limit int64) string {
	u := FormatTokens(used)
	if limit <= 0 {
		return u
	}
	if used < 0 {
		return fmt.Sprintf("%s / %s", u, FormatTokens(limit))
	}
	p := min(max(used*100/limit, 0), 999)
	return fmt.Sprintf("%s / %s (%d%%)", u, FormatTokens(limit), p)
}
func FormatCost(v float64) string {
	if v <= 0 {
		return "$0.00"
	}
	if v < .01 {
		return fmt.Sprintf("$%.4f", v)
	}
	if v < 1 {
		return fmt.Sprintf("$%.3f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
