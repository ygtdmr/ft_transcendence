package room

import (
	"math"
	"time"
)

type CountdownView struct {
	SecondsRemaining int `json:"seconds_remaining"`
}

const countdownDuration = 3 * time.Second

func (r *Room) startCountDown(now time.Time) {
	endsAt := now.Add(countdownDuration)
	r.CountdownEndsAt = &endsAt
	r.Status = StatusCountDown
}

func (r *Room) Countdown(now time.Time) *CountdownView {
	if r.Status != StatusCountDown || r.CountdownEndsAt == nil {
		return nil
	}

	remaining := r.CountdownEndsAt.Sub(now)
	remaining = min(remaining, countdownDuration)
	remaining = max(remaining, 0)

	return &CountdownView{
		SecondsRemaining: int(math.Ceil(remaining.Seconds())),
	}
}

func (r *Room) cancelCountDown() bool {
	if r.Status != StatusCountDown {
		return false
	}
	r.CountdownEndsAt = nil
	r.Status = StatusWaiting
	return true
}
