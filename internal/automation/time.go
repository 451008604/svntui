package automation

import "time"

func timeUntil(deadline, now time.Time) time.Duration {
	return deadline.Sub(now)
}
