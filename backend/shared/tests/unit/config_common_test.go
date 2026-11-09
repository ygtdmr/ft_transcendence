package unit_test

import (
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

func TestCommonDefaultsFollowEnvironment(t *testing.T) {
	cases := []struct {
		appEnv     string
		wantEnv    config.Environment
		wantFormat string
		wantLevel  string
	}{
		{"", config.Development, "text", "debug"},
		{"development", config.Development, "text", "debug"},
		{"production", config.Production, "json", "info"},
		{"test", config.Test, "text", "error"},
	}
	for _, tc := range cases {
		r := config.NewReader(env(map[string]string{"APP_ENV": tc.appEnv}))
		got := r.Common("demo", 8080)
		if err := r.Err(); err != nil {
			t.Fatalf("APP_ENV=%q: %v", tc.appEnv, err)
		}
		if got.Environment != tc.wantEnv || got.Logging.Format != tc.wantFormat ||
			got.Logging.Level != tc.wantLevel || got.Port != 8080 || got.Service != "demo" ||
			got.Logging.Service != "demo" {
			t.Errorf("APP_ENV=%q: got %+v", tc.appEnv, got)
		}
		if err := got.Logging.Validate(); err != nil {
			t.Errorf("APP_ENV=%q produced an invalid logging config: %v", tc.appEnv, err)
		}
	}
}

func TestCommonExplicitValuesOverrideDefaults(t *testing.T) {
	r := config.NewReader(env(map[string]string{
		"APP_ENV": "production", "LOG_FORMAT": "text", "LOG_LEVEL": "warn", "PORT": "9000",
	}))
	got := r.Common("demo", 8080)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if got.Logging.Format != "text" || got.Logging.Level != "warn" || got.Port != 9000 {
		t.Errorf("got %+v", got)
	}
}

func TestCommonRejectsUnsupportedValuesTogether(t *testing.T) {
	r := config.NewReader(env(map[string]string{
		"APP_ENV": "staging", "LOG_FORMAT": "xml", "LOG_LEVEL": "trace", "PORT": "0",
	}))
	r.Common("demo", 8080)
	err := r.Err()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, name := range []string{"APP_ENV", "LOG_FORMAT", "LOG_LEVEL", "PORT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
	for _, leaked := range []string{"staging", "xml", "trace"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaked value %q: %v", leaked, err)
		}
	}
}

// TestCommonLoggingBuildsALogger proves the resolved settings feed logging.New directly.
func TestCommonLoggingBuildsALogger(t *testing.T) {
	r := config.NewReader(env(map[string]string{"APP_ENV": "production"}))
	common := r.Common("demo", 8080)
	var out strings.Builder
	logger, err := logging.New(common.Logging, &out)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("started")
	if !strings.Contains(out.String(), `"service":"demo"`) {
		t.Errorf("production logger did not emit JSON with the service field: %s", out.String())
	}
}
