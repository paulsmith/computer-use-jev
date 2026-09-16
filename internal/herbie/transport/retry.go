package transport

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type RetryPolicy struct {
	MaxAttempts         int
	BaseDelay, MaxDelay time.Duration
	IdleTimeout         time.Duration
	Jitter              func(time.Duration) time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second, IdleTimeout: 10 * time.Minute, Jitter: func(d time.Duration) time.Duration {
		percent := time.Duration(75 + time.Now().UnixMilli()%51)
		return d/100*percent + d%100*percent/100
	}}
}
func ShouldRetry(status int, body []byte, streamFailure, emitted bool) bool {
	if status >= 200 && status < 300 {
		return streamFailure
	}
	if emitted {
		return false
	}
	if status == 0 {
		return true
	}
	if status == 408 || status >= 500 {
		return true
	}
	if status != 429 {
		return false
	}
	var x struct{ Error struct{ Type, Code string } }
	if json.Unmarshal(body, &x) == nil {
		for _, s := range []string{x.Error.Type, x.Error.Code} {
			switch strings.ToLower(s) {
			case "usage_limit_reached", "usage_not_included", "insufficient_quota", "quota_exceeded", "gousagelimiterror":
				return false
			}
		}
	}
	return true
}
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if p.BaseDelay <= 0 || p.MaxDelay <= 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	d := min(p.BaseDelay, p.MaxDelay)
	for ; attempt > 0; attempt-- {
		if d > p.MaxDelay/2 {
			d = p.MaxDelay
			break
		}
		d *= 2
	}
	if p.Jitter != nil {
		d = p.Jitter(d)
	}
	if d > p.MaxDelay {
		return p.MaxDelay
	}
	if d < 0 {
		return 0
	}
	return d
}
func Sleep(ctx context.Context, d time.Duration, tick func()) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			if tick != nil {
				tick()
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
}
