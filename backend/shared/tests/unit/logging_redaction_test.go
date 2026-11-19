package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

func newJSONLogger(t *testing.T, cfg logging.Config) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg.Format = "json"
	logger, err := logging.New(cfg, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return logger, &buf
}

func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v: %s", err, buf.String())
	}
	return rec
}

func TestLoggerRedactsSensitiveKeysAtAnyDepth(t *testing.T) {
	const secret = "s3cr3t-value-123"
	logger, buf := newJSONLogger(t, logging.Config{Service: "svc"})

	logger.Info("login",
		"password", secret, "Authorization", secret, "refresh_token", secret, "Set-Cookie", secret,
		"jwt", secret, "x_api_key", secret,
		slog.Group("request", slog.String("cookie", secret), slog.String("path", "/api/v1/login")),
		slog.Any("secret_cfg", config.NewSecret(secret)),
	)
	logger.With("client_secret", secret).Info("with-attrs")

	if strings.Contains(buf.String(), secret) {
		t.Fatalf("a secret reached the log output:\n%s", buf.String())
	}
	logger.Info("ok", "path", "/healthz", "status", 200)
	rec := lastRecord(t, buf)
	if rec["path"] != "/healthz" || rec["status"] != float64(200) {
		t.Errorf("non-sensitive attributes must pass through untouched: %v", rec)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	for _, key := range []string{"password", "PASSWORD", "access_token", "Authorization", "cookie", "set-cookie", "csrf_token", "client_secret", "private_key", "session_id"} {
		if !logging.IsSensitiveKey(key) {
			t.Errorf("%q must be sensitive", key)
		}
	}
	for _, key := range []string{"request_id", "path", "status", "latency", "user_id", "error_code", "method"} {
		if logging.IsSensitiveKey(key) {
			t.Errorf("%q must not be redacted", key)
		}
	}
}

func TestLoggerAddsRequestIDFromContextOnce(t *testing.T) {
	logger, buf := newJSONLogger(t, logging.Config{Service: "svc", Environment: "production"})
	ctx := requestid.WithID(context.Background(), "req-42")

	logger.InfoContext(ctx, "work done")
	rec := lastRecord(t, buf)
	if rec["request_id"] != "req-42" || rec["service"] != "svc" || rec["environment"] != "production" {
		t.Errorf("record = %v, want request_id, service and environment", rec)
	}

	// An explicit request_id (the access log passes one) is not duplicated.
	buf.Reset()
	logger.InfoContext(ctx, "explicit", "request_id", "req-42")
	if n := strings.Count(buf.String(), `"request_id"`); n != 1 {
		t.Errorf("request_id appeared %d times, want 1: %s", n, buf.String())
	}

	// Without a request context nothing is invented.
	buf.Reset()
	logger.Info("startup")
	if _, has := lastRecord(t, buf)["request_id"]; has {
		t.Error("request_id must be absent outside a request")
	}
}

func TestLoggerOmitsEnvironmentWhenUnset(t *testing.T) {
	logger, buf := newJSONLogger(t, logging.Config{Service: "svc"})
	logger.Info("x")
	if _, has := lastRecord(t, buf)["environment"]; has {
		t.Error("environment must be omitted when not configured")
	}
}

func TestAccessLogIncludesErrorCodeOnlyWhenPresent(t *testing.T) {
	logger, buf := newJSONLogger(t, logging.Config{Service: "svc"})
	logging.LogAccess(context.Background(), logger, logging.AccessLog{RequestID: "r1", Status: 400, Method: "POST", Path: "/p", ErrorCode: "VALIDATION_ERROR"})
	if lastRecord(t, buf)["error_code"] != "VALIDATION_ERROR" {
		t.Error("error_code missing from error access log")
	}
	buf.Reset()
	logging.LogAccess(context.Background(), logger, logging.AccessLog{RequestID: "r2", Status: 200, Method: "GET", Path: "/p"})
	if _, has := lastRecord(t, buf)["error_code"]; has {
		t.Error("error_code must be absent on success")
	}
}

func TestCommonSetsLoggingEnvironment(t *testing.T) {
	r := config.NewReader(func(name string) (string, bool) {
		if name == "APP_ENV" {
			return "production", true
		}
		return "", false
	})
	if got := r.Common("svc", 8080).Logging.Environment; got != "production" {
		t.Errorf("Logging.Environment = %q", got)
	}
}
