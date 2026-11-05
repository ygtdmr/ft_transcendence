package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestcontext"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

const (
	downstreamSecret = "downstream-secret-token-9931"
	traceID          = "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpan       = "00f067aa0ba902b7"
)

// upstream is a slow private service that records what it was sent and whether
// the caller's cancellation reached it.
type upstream struct {
	srv *httptest.Server

	mu           sync.Mutex
	requestIDs   []string
	traceparents []string
	cancelled    chan time.Duration // time from arrival to observing cancellation
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{cancelled: make(chan time.Duration, 64)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requestIDs = append(u.requestIDs, r.Header.Get(requestid.Header))
		u.traceparents = append(u.traceparents, r.Header.Get(tracecontext.Header))
		u.mu.Unlock()
		arrived := time.Now()
		select {
		case <-r.Context().Done():
			u.cancelled <- time.Since(arrived)
		case <-time.After(5 * time.Second):
			_, _ = w.Write([]byte("late"))
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) first() (id, traceparent string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requestIDs) == 0 {
		return "", ""
	}
	return u.requestIDs[0], u.traceparents[0]
}

// frontService is a service wired like an adopter would: shared middleware,
// a per-route timeout, and a private client built on requestcontext.Transport.
func frontService(t *testing.T, up *upstream, timeout time.Duration) (*httptest.Server, *lockedBuffer) {
	t.Helper()
	logs := newLockedBuffer()
	logger, err := logging.New(logging.Config{Format: "json", Level: "debug", Service: "front"}, logs)
	if err != nil {
		t.Fatal(err)
	}
	private := &http.Client{Transport: requestcontext.Transport(&http.Transport{DisableKeepAlives: true})}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger))
	api := router.Group("/api", ginhttp.Timeout(timeout))
	api.GET("/slow", func(c *gin.Context) {
		ctx := c.Request.Context()
		logger.InfoContext(ctx, "calling upstream")
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, up.srv.URL+"/work?token="+downstreamSecret, nil)
		resp, err := private.Do(req)
		if err != nil {
			// Deliberately say nothing about err: it embeds the URL and its token.
			// The Timeout middleware answers when the deadline caused it.
			return
		}
		resp.Body.Close()
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{"ok": true})
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, logs
}

func TestTimeoutCancelsTheDownstreamCallAndTheClientGetsA503(t *testing.T) {
	up := newUpstream(t)
	front, logs := frontService(t, up, 300*time.Millisecond)

	begin := time.Now()
	resp, body := do(t, "GET", front.URL+"/api/slow", map[string]string{
		requestid.Header:    "client-req-77",
		tracecontext.Header: "00-" + traceID + "-" + parentSpan + "-01",
	})
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("the client waited %s; the 300ms timeout was not enforced", elapsed)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"code":"SERVICE_UNAVAILABLE"`) {
		t.Fatalf("got %d %s, want 503 SERVICE_UNAVAILABLE", resp.StatusCode, body)
	}
	if v := httpresponse.Violations(resp.StatusCode, body); len(v) != 0 {
		t.Errorf("timeout response violates the conventions: %v", v)
	}
	if resp.Header.Get(requestid.Header) != "client-req-77" {
		t.Errorf("response request id = %q", resp.Header.Get(requestid.Header))
	}

	select {
	case after := <-up.cancelled:
		if after > 2*time.Second {
			t.Errorf("the upstream only noticed the cancellation after %s", after)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the timeout never reached the downstream call; it ran on")
	}

	id, traceparent := up.first()
	sent, ok := tracecontext.Parse(traceparent)
	if id != "client-req-77" || !ok || sent.TraceID != traceID || sent.SpanID == parentSpan {
		t.Errorf("upstream got request id %q and traceparent %q; want the client's id and the same trace in a new span", id, traceparent)
	}

	for _, secret := range []string{downstreamSecret} {
		if strings.Contains(string(body), secret) || strings.Contains(logs.String(), secret) {
			t.Errorf("the downstream URL's token leaked into the response or logs")
		}
	}
	var sawAccess bool
	for _, rec := range logRecords(t, logs) {
		if rec["trace_id"] != traceID || rec["request_id"] != "client-req-77" {
			t.Errorf("log line lacks the request/trace ids: %v", rec)
		}
		if rec["msg"] == "http_request" && rec["status"] == float64(503) && rec["error_code"] == "SERVICE_UNAVAILABLE" {
			sawAccess = true
		}
	}
	if !sawAccess {
		t.Errorf("no 503 access-log record with its error code:\n%s", logs.String())
	}
}

func TestClientDisconnectCancelsTheDownstreamCall(t *testing.T) {
	up := newUpstream(t)
	front, _ := frontService(t, up, 30*time.Second) // long timeout: only the disconnect can cancel

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/api/slow", nil)
	if resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to give up")
	}

	select {
	case <-up.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the client's disconnect was not propagated to the downstream call")
	}
}

func TestEveryRequestIsTracedEndToEndWhenTheClientSendsNothing(t *testing.T) {
	up := newUpstream(t)
	front, logs := frontService(t, up, 200*time.Millisecond)

	resp, _ := do(t, "GET", front.URL+"/api/slow", nil)
	generated := resp.Header.Get(requestid.Header)
	if !requestid.Valid(generated) {
		t.Fatalf("a request id must be generated, got %q", generated)
	}
	select {
	case <-up.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never saw the call")
	}
	id, traceparent := up.first()
	if id != generated {
		t.Errorf("upstream request id %q != response request id %q", id, generated)
	}
	if _, ok := tracecontext.Parse(traceparent); !ok {
		t.Errorf("upstream traceparent %q is not valid", traceparent)
	}
	for _, rec := range logRecords(t, logs) {
		if rec["request_id"] != generated {
			t.Errorf("log line has request_id %v, want %s", rec["request_id"], generated)
		}
	}
}

func TestTimedOutRequestsDoNotLeakGoroutines(t *testing.T) {
	up := newUpstream(t)
	front, _ := frontService(t, up, 100*time.Millisecond)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	settle := func() int {
		time.Sleep(300 * time.Millisecond)
		runtime.GC()
		return runtime.NumGoroutine()
	}
	baseline := settle()

	const requests = 40
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(front.URL + "/api/slow")
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	var after int
	for deadline := time.Now().Add(5 * time.Second); ; {
		after = settle()
		if after <= baseline+8 || time.Now().After(deadline) {
			break
		}
	}
	if after > baseline+8 {
		t.Errorf("goroutines: baseline %d, after %d timed-out requests; downstream work is leaking", baseline, after)
	}
}

func TestRequestTimeoutStopsARealDatabaseQuery(t *testing.T) {
	dsn := requireEnv(t, "POSTGRES_TEST_DATABASE_URL")
	pool, err := postgres.Open(context.Background(), postgres.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	handler := requestcontext.Middleware(250 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := pool.Exec(r.Context(), "SELECT pg_sleep(10)"); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusServiceUnavailable
			}
			w.WriteHeader(status)
		}
	}))

	begin := time.Now()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Errorf("a 10s query under a 250ms request timeout took %s", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 from the deadline", rec.Code)
	}

	assertPoolReleased(t, pool)
	if err := postgres.Check(context.Background(), pool, 2*time.Second); err != nil {
		t.Errorf("the pool is unusable after a cancelled query: %v", err)
	}
}

// assertPoolReleased waits for the cancelled query's connection to be returned.
func assertPoolReleased(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := eventually(t, 5*time.Second, func() error {
		if n := pool.Stat().AcquiredConns(); n != 0 {
			return errors.New("connections still acquired")
		}
		return nil
	}); err != nil {
		t.Errorf("a cancelled query left a connection checked out: %v", err)
	}
}
