// Package timex provides the real wall-clock implementations of the
// domain.Clock and domain.Sleeper ports.
package timex

import (
	"context"
	"time"
)

// Clock reads real time.
type Clock struct{}

func (Clock) Now() time.Time                  { return time.Now() }
func (Clock) Since(t time.Time) time.Duration { return time.Since(t) }

// Sleeper sleeps for real, respecting context cancellation.
type Sleeper struct{}

func (Sleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
