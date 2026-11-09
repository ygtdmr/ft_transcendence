package unit_test

import (
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// TestExponentialBackoff checks the retry delays: 1s doubling to a 5 minute cap.
func TestExponentialBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{
		0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second,
		9: 256 * time.Second, 10: 5 * time.Minute, 1000: 5 * time.Minute,
	} {
		if got := service.ExponentialBackoff(attempts); got != want {
			t.Fatalf("ExponentialBackoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}
