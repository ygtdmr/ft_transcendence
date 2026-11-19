// Package lifecycle gives every service the same liveness, readiness and
// graceful-shutdown behaviour.
//
// Liveness answers "is the process serving HTTP" and never touches a dependency,
// so an orchestrator does not restart a healthy service because its database is
// down. Readiness answers "can this instance take traffic now": it fails while a
// required dependency is unavailable and from the moment shutdown starts
// (draining), so a load balancer stops sending new requests before the listener
// closes. Serve runs the HTTP server and performs the bounded shutdown sequence.
//
// Typical wiring in a service's main:
//
//	health := lifecycle.NewHealth()
//	health.Add(lifecycle.Check{Name: "postgres", Fn: func(ctx context.Context) error {
//		return postgres.Check(ctx, pool, 0)
//	}})
//	health.Add(lifecycle.Check{Name: "redis", Optional: true, Fn: func(ctx context.Context) error {
//		return redisclient.Check(ctx, cache)
//	}})
//	router.GET("/healthz", gin.WrapH(health.Liveness()))
//	router.GET("/readyz", gin.WrapH(health.Readiness()))
//
//	err := lifecycle.Serve(ctx, server, lifecycle.Options{
//		Health: health,
//		Components: []lifecycle.Component{ // closed in reverse order, after HTTP stops
//			lifecycle.Func("postgres", pool.Close),
//			lifecycle.Func("rabbitmq", func() { _ = broker.Close() }),
//		},
//	})
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

const defaultCheckTimeout = 2 * time.Second

// Check probes one dependency. Fn returns nil when the dependency is available.
type Check struct {
	// Name identifies the dependency in readiness responses ("postgres").
	// Names are public; never put hosts or credentials in them.
	Name string
	Fn   func(ctx context.Context) error
	// Optional dependencies are reported when down ("degraded") but do not make
	// the service not ready.
	Optional bool
}

// NotReadyError reports why Ready failed. It carries dependency names only,
// never the underlying errors, so it is safe to expose.
type NotReadyError struct {
	Draining     bool
	Dependencies []string
}

func (e *NotReadyError) Error() string {
	if e.Draining {
		return "service is shutting down"
	}
	return fmt.Sprintf("required dependencies unavailable: %v", e.Dependencies)
}

// Health aggregates dependency checks and the draining flag. The zero value is
// not usable; call NewHealth. All methods are safe for concurrent use.
type Health struct {
	mu           sync.RWMutex
	checks       []*entry
	checkTimeout time.Duration
	draining     atomic.Bool
}

// entry pairs a check with a guard that stops probes from piling up behind a
// check that never returns.
type entry struct {
	Check
	running atomic.Bool
}

// NewHealth returns a Health with a 2s per-check timeout.
func NewHealth() *Health { return &Health{checkTimeout: defaultCheckTimeout} }

// SetCheckTimeout bounds each dependency check (default 2s).
func (h *Health) SetCheckTimeout(d time.Duration) {
	if d > 0 {
		h.mu.Lock()
		h.checkTimeout = d
		h.mu.Unlock()
	}
}

// Add registers a dependency check. Register only the dependencies this service
// actually requires.
func (h *Health) Add(check Check) {
	h.mu.Lock()
	h.checks = append(h.checks, &entry{Check: check})
	h.mu.Unlock()
}

// SetDraining marks the service as shutting down; readiness fails from now on.
func (h *Health) SetDraining() { h.draining.Store(true) }

// Draining reports whether shutdown has started.
func (h *Health) Draining() bool { return h.draining.Load() }

// result is the outcome of running all checks.
type result struct {
	failedRequired []string
	degraded       []string
}

// run executes every check concurrently, each bounded by the check timeout. A
// panicking or nil check counts as a failure.
func (h *Health) run(ctx context.Context) result {
	h.mu.RLock()
	checks := append([]*entry(nil), h.checks...)
	timeout := h.checkTimeout
	h.mu.RUnlock()

	failed := make([]bool, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failed[i] = !probe(ctx, check, timeout)
		}()
	}
	wg.Wait()

	var out result
	for i, check := range checks {
		if !failed[i] {
			continue
		}
		if check.Optional {
			out.degraded = append(out.degraded, check.Name)
		} else {
			out.failedRequired = append(out.failedRequired, check.Name)
		}
	}
	sort.Strings(out.failedRequired)
	sort.Strings(out.degraded)
	return out
}

// probe runs one check. If the previous probe of the same check has not returned
// (the check ignores its context and is stuck), it reports failure without
// starting another goroutine, so a stuck dependency leaks at most one goroutine
// per check instead of one per readiness request.
func probe(ctx context.Context, e *entry, timeout time.Duration) bool {
	if e.Fn == nil || !e.running.CompareAndSwap(false, true) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer e.running.Store(false)
		defer func() {
			if recover() != nil {
				done <- errors.New("check panicked")
			}
		}()
		done <- e.Fn(ctx)
	}()
	select {
	case err := <-done:
		return err == nil
	case <-ctx.Done():
		return false
	}
}

// Ready returns nil when the service can take traffic, or a *NotReadyError.
func (h *Health) Ready(ctx context.Context) error {
	if h.Draining() {
		return &NotReadyError{Draining: true}
	}
	if res := h.run(ctx); len(res.failedRequired) > 0 {
		return &NotReadyError{Dependencies: res.failedRequired}
	}
	return nil
}

// Liveness serves 200 {"data":{"status":"OK"}} whenever the process can answer.
// It never runs a dependency check and keeps answering 200 while draining.
func (h *Health) Liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpresponse.WriteData(w, http.StatusOK, map[string]any{"status": "OK"})
	})
}

// Readiness serves 200 {"data":{"status":"OK"}} when ready (plus "degraded"
// names when an optional dependency is down) and 503 otherwise:
//
//   - draining: SERVICE_UNAVAILABLE
//   - a required dependency is down: DEPENDENCY_UNAVAILABLE with
//     details.dependencies listing the names
//
// Responses contain dependency names only, never error text.
func (h *Health) Readiness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Draining() {
			httpresponse.WriteError(w, &httpresponse.Error{
				Status:  http.StatusServiceUnavailable,
				Code:    "SERVICE_UNAVAILABLE",
				Message: "The service is shutting down.",
			})
			return
		}
		res := h.run(r.Context())
		if len(res.failedRequired) > 0 {
			httpresponse.WriteError(w, &httpresponse.Error{
				Status:  http.StatusServiceUnavailable,
				Code:    "DEPENDENCY_UNAVAILABLE",
				Message: "A required dependency is unavailable.",
				Details: map[string]any{"dependencies": res.failedRequired},
			})
			return
		}
		data := map[string]any{"status": "OK"}
		if len(res.degraded) > 0 {
			data["degraded"] = res.degraded
		}
		httpresponse.WriteData(w, http.StatusOK, data)
	})
}

// Contract serves the public GET /api/v1/health document from the API contract:
// process availability plus the contract revision this build implements. Like
// liveness it never touches a dependency. The caller passes the version string
// so this package does not own the contract.
func (h *Health) Contract(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpresponse.WriteData(w, http.StatusOK, map[string]any{
			"status":           "OK",
			"contract_version": version,
			"server_time":      httpresponse.Time(time.Now()),
		})
	})
}
