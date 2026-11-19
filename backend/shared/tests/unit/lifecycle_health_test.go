package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
)

func get(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func okCheck(string) func(context.Context) error { return func(context.Context) error { return nil } }

func TestReadinessWithNoChecksIsReady(t *testing.T) {
	h := lifecycle.NewHealth()
	rec := get(t, h.Readiness())
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"data":{"status":"OK"}}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if err := h.Ready(context.Background()); err != nil {
		t.Fatalf("Ready = %v", err)
	}
}

func TestReadinessFailsWhenARequiredDependencyIsDown(t *testing.T) {
	const leak = "password=hunter2@db.internal:5432"
	h := lifecycle.NewHealth()
	h.Add(lifecycle.Check{Name: "rabbitmq", Fn: func(context.Context) error { return errors.New("dial " + leak) }})
	h.Add(lifecycle.Check{Name: "postgres", Fn: okCheck("")})
	h.Add(lifecycle.Check{Name: "cache", Optional: true, Fn: func(context.Context) error { return errors.New(leak) }})

	rec := get(t, h.Readiness())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Dependencies []string `json:"dependencies"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "DEPENDENCY_UNAVAILABLE" || len(body.Error.Details.Dependencies) != 1 || body.Error.Details.Dependencies[0] != "rabbitmq" {
		t.Errorf("only the failed required dependency must be named: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "internal") {
		t.Errorf("the check's error text leaked into the response: %s", rec.Body.String())
	}
	if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
		t.Errorf("readiness error violates the conventions: %v", v)
	}

	var notReady *lifecycle.NotReadyError
	if err := h.Ready(context.Background()); !errors.As(err, &notReady) || notReady.Draining || len(notReady.Dependencies) != 1 {
		t.Errorf("Ready = %v", err)
	}
	if strings.Contains(h.Ready(context.Background()).Error(), "hunter2") {
		t.Error("NotReadyError leaked the check error")
	}
}

func TestOptionalDependencyIsReportedButDoesNotFailReadiness(t *testing.T) {
	h := lifecycle.NewHealth()
	h.Add(lifecycle.Check{Name: "redis", Optional: true, Fn: func(context.Context) error { return errors.New("down") }})
	rec := get(t, h.Readiness())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"degraded":["redis"]`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if err := h.Ready(context.Background()); err != nil {
		t.Errorf("an optional outage must not make the service not ready: %v", err)
	}
}

func TestHungPanickingAndNilChecksCountAsFailures(t *testing.T) {
	h := lifecycle.NewHealth()
	h.SetCheckTimeout(150 * time.Millisecond)
	h.Add(lifecycle.Check{Name: "hung", Fn: func(context.Context) error { select {} }}) // ignores its context
	h.Add(lifecycle.Check{Name: "panics", Fn: func(context.Context) error { panic("boom") }})
	h.Add(lifecycle.Check{Name: "nil"})

	start := time.Now()
	rec := get(t, h.Readiness())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("readiness took %s; a check that ignores its context must not hold it hostage", elapsed)
	}
	for _, name := range []string{"hung", "panics", "nil"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("%q must be reported as unavailable: %s", name, rec.Body.String())
		}
	}
	if rec.Code != 503 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestChecksRunConcurrently(t *testing.T) {
	h := lifecycle.NewHealth()
	slow := func(context.Context) error { time.Sleep(300 * time.Millisecond); return nil }
	for _, name := range []string{"a", "b", "c"} {
		h.Add(lifecycle.Check{Name: name, Fn: slow})
	}
	start := time.Now()
	if rec := get(t, h.Readiness()); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > 700*time.Millisecond {
		t.Errorf("3 x 300ms checks took %s; they must run in parallel", elapsed)
	}
}

func TestDrainingFailsReadinessButNotLiveness(t *testing.T) {
	var runs atomic.Int32
	h := lifecycle.NewHealth()
	h.Add(lifecycle.Check{Name: "postgres", Fn: func(context.Context) error { runs.Add(1); return nil }})

	h.SetDraining()
	ready := get(t, h.Readiness())
	if ready.Code != 503 || !strings.Contains(ready.Body.String(), `"code":"SERVICE_UNAVAILABLE"`) {
		t.Errorf("readiness while draining: %d %s", ready.Code, ready.Body.String())
	}
	if live := get(t, h.Liveness()); live.Code != 200 {
		t.Errorf("liveness must stay 200 while draining, got %d", live.Code)
	}
	var notReady *lifecycle.NotReadyError
	if err := h.Ready(context.Background()); !errors.As(err, &notReady) || !notReady.Draining {
		t.Errorf("Ready while draining = %v", err)
	}
	if runs.Load() != 0 {
		t.Error("draining must short-circuit; dependencies should not be probed")
	}
}

func TestLivenessNeverProbesDependencies(t *testing.T) {
	var runs atomic.Int32
	h := lifecycle.NewHealth()
	h.Add(lifecycle.Check{Name: "postgres", Fn: func(context.Context) error { runs.Add(1); return errors.New("down") }})
	rec := get(t, h.Liveness())
	if rec.Code != 200 || runs.Load() != 0 {
		t.Fatalf("liveness: status=%d probes=%d; a dependency outage must not fail liveness", rec.Code, runs.Load())
	}
	if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
		t.Errorf("%v", v)
	}
}

func TestContractHealthDocument(t *testing.T) {
	rec := get(t, lifecycle.NewHealth().Contract("9.9.9-test"))
	var body struct {
		Data struct {
			Status          string `json:"status"`
			ContractVersion string `json:"contract_version"`
			ServerTime      string `json:"server_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.Data.Status != "OK" || body.Data.ContractVersion != "9.9.9-test" {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	if ts, err := time.Parse(time.RFC3339Nano, body.Data.ServerTime); err != nil || !strings.HasSuffix(body.Data.ServerTime, "Z") || time.Since(ts) > time.Minute {
		t.Errorf("server_time = %q, want a recent UTC timestamp", body.Data.ServerTime)
	}
	if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
		t.Errorf("%v", v)
	}
}

func TestLifecycleOptionsFromEnv(t *testing.T) {
	lookup := func(values map[string]string) *config.Reader {
		return config.NewReader(func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	}
	r := lookup(nil)
	opts := lifecycle.OptionsFromEnv(r)
	if r.Err() != nil || opts.ShutdownTimeout != 10*time.Second || opts.DrainDelay != 0 || opts.CloseTimeout != 5*time.Second {
		t.Errorf("defaults: %+v (%v)", opts, r.Err())
	}
	r = lookup(map[string]string{"SHUTDOWN_TIMEOUT": "30s", "SHUTDOWN_DRAIN_DELAY": "5s", "SHUTDOWN_CLOSE_TIMEOUT": "2s"})
	opts = lifecycle.OptionsFromEnv(r)
	if r.Err() != nil || opts.ShutdownTimeout != 30*time.Second || opts.DrainDelay != 5*time.Second || opts.CloseTimeout != 2*time.Second {
		t.Errorf("explicit: %+v (%v)", opts, r.Err())
	}
	r = lookup(map[string]string{"SHUTDOWN_TIMEOUT": "0s", "SHUTDOWN_DRAIN_DELAY": "-1s", "SHUTDOWN_CLOSE_TIMEOUT": "never"})
	lifecycle.OptionsFromEnv(r)
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT") ||
		!strings.Contains(err.Error(), "SHUTDOWN_DRAIN_DELAY") || !strings.Contains(err.Error(), "SHUTDOWN_CLOSE_TIMEOUT") {
		t.Errorf("invalid timings must each be named: %v", err)
	}
}

func TestStuckCheckDoesNotLeakOneGoroutinePerProbe(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	h := lifecycle.NewHealth()
	h.SetCheckTimeout(50 * time.Millisecond)
	h.Add(lifecycle.Check{Name: "stuck", Fn: func(context.Context) error {
		started.Add(1)
		<-release // ignores ctx, like a driver call stuck in a syscall
		return nil
	}})

	for i := 0; i < 20; i++ {
		if rec := get(t, h.Readiness()); rec.Code != 503 {
			t.Fatalf("probe %d: status %d, want 503", i, rec.Code)
		}
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d probes started the stuck check %d times; later probes must not pile up behind it", 20, n)
	}

	close(release) // the dependency recovers: probing resumes and succeeds
	deadline := time.Now().Add(2 * time.Second)
	for {
		if rec := get(t, h.Readiness()); rec.Code == 200 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness never recovered after the stuck check returned")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
