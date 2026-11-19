package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/metrics"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

const leakedSecret = "leak-me-if-you-can-4711"

// lockedBuffer is a bytes.Buffer safe for the server goroutine and the test.
type lockedBuffer struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func newLockedBuffer() *lockedBuffer { return &lockedBuffer{mu: make(chan struct{}, 1)} }
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.Write(p)
}
func (b *lockedBuffer) String() string {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.String()
}

// newConventionsServer assembles a service the way an adopter would: shared
// middleware, shared response helpers, the shared logger.
//
// Prometheus counters are process-global, so each server gets its own service
// label (derived from the test name); otherwise counts leak between tests.
func newConventionsServer(t *testing.T) (*httptest.Server, *lockedBuffer, string) {
	t.Helper()
	service := "itest-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	logs := newLockedBuffer()
	logger, err := logging.New(logging.Config{Format: "json", Level: "debug", Service: service, Environment: "test"}, logs)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), metrics.Middleware(service))

	router.GET("/metrics", metrics.Handler())
	router.GET("/api/v1/things/:id", func(c *gin.Context) {
		logger.InfoContext(c.Request.Context(), "loading thing", "thing_id", c.Param("id"),
			"password", leakedSecret, "Authorization", c.GetHeader("Authorization"))
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{
			"thing_id":   c.Param("id"),
			"created_at": httpresponse.Time(time.Date(2026, 11, 5, 19, 0, 0, 0, time.FixedZone("EET", 7200))),
			"state":      "IN_PROGRESS",
		})
	})
	router.POST("/api/v1/things", func(c *gin.Context) {
		httpresponse.WriteError(c.Writer, &httpresponse.Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "Invalid thing.", Details: map[string]any{"field": "name"}})
	})
	router.DELETE("/api/v1/things/:id", func(c *gin.Context) { httpresponse.WriteNoContent(c.Writer) })
	router.GET("/stream", func(c *gin.Context) {
		_, hijack := c.Writer.(http.Hijacker)
		_, flush := c.Writer.(http.Flusher)
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{"can_hijack": hijack, "can_flush": flush})
	})
	router.NoRoute(func(c *gin.Context) {
		httpresponse.WriteError(c.Writer, &httpresponse.Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "The requested endpoint was not found."})
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, logs, service
}

func do(t *testing.T, method, url string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func logRecords(t *testing.T, logs *lockedBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestEveryResponseFollowsTheConventions(t *testing.T) {
	srv, _, _ := newConventionsServer(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/things/abc", 200},
		{"POST", "/api/v1/things", 400},
		{"DELETE", "/api/v1/things/abc", 204},
		{"GET", "/no/such/route", 404},
	} {
		resp, body := do(t, tc.method, srv.URL+tc.path, nil)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
		if v := httpresponse.Violations(resp.StatusCode, body); len(v) != 0 {
			t.Errorf("%s %s violates the conventions: %v\nbody: %s", tc.method, tc.path, v, body)
		}
	}

	_, body := do(t, "GET", srv.URL+"/api/v1/things/abc", nil)
	var parsed struct {
		Data struct {
			CreatedAt string `json:"created_at"`
			State     string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data.CreatedAt != "2026-11-05T17:00:00Z" || parsed.Data.State != "IN_PROGRESS" {
		t.Errorf("timestamp must be normalised to UTC and enum kept UPPER_SNAKE: %s", body)
	}
}

func TestRequestIDCorrelatesResponseAndEveryLogLine(t *testing.T) {
	srv, logs, _ := newConventionsServer(t)

	resp, _ := do(t, "GET", srv.URL+"/api/v1/things/abc?token="+leakedSecret, map[string]string{requestid.Header: "trace-abc-1"})
	if got := resp.Header.Get(requestid.Header); got != "trace-abc-1" {
		t.Fatalf("response request id = %q, want the client's valid ID echoed", got)
	}

	var correlated int
	for _, rec := range logRecords(t, logs) {
		if rec["request_id"] == "trace-abc-1" {
			correlated++
		}
	}
	if correlated != 2 {
		t.Fatalf("%d log lines carry request_id trace-abc-1, want 2 (handler line + access line):\n%s", correlated, logs.String())
	}

	// A bad ID is replaced, not trusted and not rejected.
	resp, _ = do(t, "GET", srv.URL+"/api/v1/things/abc", map[string]string{requestid.Header: "bad id!"})
	if got := resp.Header.Get(requestid.Header); resp.StatusCode != 200 || got == "bad id!" || !requestid.Valid(got) {
		t.Errorf("invalid client ID: status=%d replaced=%q", resp.StatusCode, got)
	}
}

func TestLogsNeverContainSecrets(t *testing.T) {
	srv, logs, _ := newConventionsServer(t)
	do(t, "GET", srv.URL+"/api/v1/things/abc?access_token="+leakedSecret, map[string]string{
		"Authorization": "Bearer " + leakedSecret,
		"Cookie":        "ft_session=" + leakedSecret,
	})
	do(t, "POST", srv.URL+"/api/v1/things?password="+leakedSecret, nil)

	if out := logs.String(); strings.Contains(out, leakedSecret) {
		t.Fatalf("a secret reached the logs:\n%s", out)
	}
	for _, rec := range logRecords(t, logs) {
		if rec["msg"] == "http_request" {
			if p, _ := rec["path"].(string); strings.Contains(p, "?") {
				t.Errorf("access log path must not include the query string: %v", rec)
			}
		}
	}
}

func TestAccessLogCarriesStatusLevelAndErrorCode(t *testing.T) {
	srv, logs, service := newConventionsServer(t)
	do(t, "POST", srv.URL+"/api/v1/things", nil)
	do(t, "GET", srv.URL+"/api/v1/things/abc", nil)

	var sawError, sawOK bool
	for _, rec := range logRecords(t, logs) {
		if rec["msg"] != "http_request" {
			continue
		}
		switch rec["status"] {
		case float64(400):
			sawError = rec["error_code"] == "VALIDATION_ERROR" && rec["level"] == "WARN"
		case float64(200):
			_, hasCode := rec["error_code"]
			sawOK = !hasCode && rec["level"] == "INFO"
		}
		if rec["service"] != service || rec["environment"] != "test" {
			t.Errorf("record lacks service/environment: %v", rec)
		}
	}
	if !sawError || !sawOK {
		t.Errorf("access logs wrong (error ok=%v, success ok=%v):\n%s", sawError, sawOK, logs.String())
	}
}

func TestMetricsRecordLatencyAndErrorCodes(t *testing.T) {
	srv, _, service := newConventionsServer(t)
	do(t, "GET", srv.URL+"/api/v1/things/abc", nil)
	do(t, "GET", srv.URL+"/api/v1/things/def", nil)
	do(t, "POST", srv.URL+"/api/v1/things", nil)
	do(t, "GET", srv.URL+"/no/such/route", nil)

	_, body := do(t, "GET", srv.URL+"/metrics", nil)
	text := string(body)
	for _, want := range []string{
		`http_requests_total{method="GET",route="/api/v1/things/:id",service="` + service + `",status="200"} 2`,
		`http_requests_total{method="POST",route="/api/v1/things",service="` + service + `",status="400"} 1`,
		`http_requests_total{method="GET",route="unmatched",service="` + service + `",status="404"} 1`,
		`http_request_duration_seconds_count{route="/api/v1/things/:id",service="` + service + `"} 2`,
		`http_errors_total{code="VALIDATION_ERROR",service="` + service + `"} 1`,
		`http_errors_total{code="NOT_FOUND",service="` + service + `"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
	if strings.Contains(text, `route="/api/v1/things/abc"`) {
		t.Error("raw paths must not become route labels (unbounded cardinality)")
	}
}

func TestRecorderKeepsWebSocketCapabilities(t *testing.T) {
	srv, _, _ := newConventionsServer(t)
	_, body := do(t, "GET", srv.URL+"/stream", nil)
	var parsed struct {
		Data struct {
			CanHijack bool `json:"can_hijack"`
			CanFlush  bool `json:"can_flush"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || !parsed.Data.CanHijack || !parsed.Data.CanFlush {
		t.Fatalf("the error-code recorder must not hide Hijacker/Flusher (needed for WebSocket upgrades): %s", body)
	}
}
