package unit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

type probeFunc func(context.Context) error

func (f probeFunc) Ping(ctx context.Context) error { return f(ctx) }

// TestHealthServiceReady checks the readiness verdict of the service.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - A database probe that answers, one that fails, and one that records its context.
//
// Flow:
//
//  1. Call Ready with each probe.
//  2. Inspect the error chain and the context handed to the probe.
//
// Expected output:
//
//   - nil when the probe answers.
//   - service.ErrUnavailable, with the driver error kept in the chain for logs, when it fails.
//   - A probe context bounded to at most two seconds, even if the caller's has no deadline.
func TestHealthServiceReady(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		health := service.NewHealth(probeFunc(func(context.Context) error { return nil }))
		if err := health.Ready(context.Background()); err != nil {
			t.Fatalf("Ready() error = %v", err)
		}
	})

	t.Run("unavailable keeps the driver error for logs", func(t *testing.T) {
		driver := errors.New("connection refused")
		health := service.NewHealth(probeFunc(func(context.Context) error { return driver }))
		err := health.Ready(context.Background())
		if !errors.Is(err, service.ErrUnavailable) || !errors.Is(err, driver) {
			t.Fatalf("Ready() error = %v, want ErrUnavailable wrapping the driver error", err)
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("the log message lost the driver error: %v", err)
		}
	})

	t.Run("the probe is bounded", func(t *testing.T) {
		var deadline time.Time
		var ok bool
		health := service.NewHealth(probeFunc(func(ctx context.Context) error {
			deadline, ok = ctx.Deadline()
			return nil
		}))
		start := time.Now()
		if err := health.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !ok || deadline.Sub(start) > 2*time.Second+100*time.Millisecond {
			t.Fatalf("probe deadline = %v (set: %v), want at most two seconds ahead", deadline.Sub(start), ok)
		}
	})
}
