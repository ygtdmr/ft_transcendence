package unit_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

// env returns a LookupFunc backed by a fixed map.
func env(values map[string]string) config.LookupFunc {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

// problems extracts the problem list from an error returned by Reader.Err.
func problems(t *testing.T, err error) []config.Problem {
	t.Helper()
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want *config.ValidationError", err)
	}
	return validation.Problems
}

func TestReaderValidSettings(t *testing.T) {
	r := config.NewReader(env(map[string]string{
		"NAME":     "  svc  ",
		"MODE":     "fast",
		"PORT":     "9090",
		"TTL":      "15m",
		"FLAG":     "false",
		"DB":       "postgres://u:p@db:5432/app",
		"ADDR":     ":8443",
		"SECRET_K": strings.Repeat("k", 32),
	}))

	if got := r.String("NAME", "x"); got != "svc" {
		t.Errorf("String = %q, want trimmed value", got)
	}
	if got := r.Enum("MODE", "slow", "slow", "fast"); got != "fast" {
		t.Errorf("Enum = %q", got)
	}
	if got := r.Port("PORT", 1); got != 9090 {
		t.Errorf("Port = %d", got)
	}
	if got := r.Duration("TTL", time.Second, time.Second, time.Hour); got != 15*time.Minute {
		t.Errorf("Duration = %s", got)
	}
	if got := r.Bool("FLAG", true); got {
		t.Error("Bool = true, want false")
	}
	if got := r.URL("DB", "postgres", "postgresql"); got != "postgres://u:p@db:5432/app" {
		t.Errorf("URL = %q", got)
	}
	if got := r.Address("ADDR", ":1"); got != ":8443" {
		t.Errorf("Address = %q", got)
	}
	if got := r.Secret("SECRET_K", 32); got.Reveal() != strings.Repeat("k", 32) {
		t.Error("Secret did not round-trip through Reveal")
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
}

func TestReaderFallbacksWhenUnset(t *testing.T) {
	r := config.NewReader(env(nil))

	if r.String("A", "def") != "def" || r.Enum("B", "x", "x", "y") != "x" ||
		r.Port("C", 8080) != 8080 || r.Bool("E", true) != true ||
		r.Duration("D", time.Minute, 0, 0) != time.Minute || r.Address("F", ":80") != ":80" ||
		r.OptionalURL("G", "redis") != "" {
		t.Fatal("unset optional settings must return their fallbacks")
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v, want nil for unset optional settings", err)
	}
}

func TestReaderCollectsEveryProblemWithoutLeakingValues(t *testing.T) {
	const leak = "hunter2-do-not-print"
	r := config.NewReader(env(map[string]string{
		"MODE":   leak,
		"PORT":   leak,
		"TTL":    leak,
		"TTL2":   "1ms",
		"FLAG":   leak,
		"DB":     "mysql://user:" + leak + "@db/app",
		"BADURL": "://" + leak,
		"ADDR":   leak,
		"SHORT":  "abc",
	}))

	r.Enum("MODE", "slow", "slow", "fast")
	r.Port("PORT", 1)
	r.Duration("TTL", time.Second, time.Second, 0)
	r.Duration("TTL2", time.Second, time.Second, time.Hour)
	r.Bool("FLAG", false)
	r.URL("DB", "postgres")
	r.URL("BADURL", "postgres")
	r.Address("ADDR", ":1")
	r.Secret("SHORT", 32)
	r.Secret("MISSING", 32)
	r.Required("ALSO_MISSING")
	r.URL("NO_URL", "postgres")

	err := r.Err()
	got := problems(t, err)
	if len(got) != 12 {
		t.Fatalf("collected %d problems, want 12: %v", len(got), err)
	}
	msg := err.Error()
	if strings.Contains(msg, leak) {
		t.Fatalf("error message leaked a configuration value: %s", msg)
	}
	for _, want := range []string{"MODE must be one of: slow, fast", "PORT must be an integer between 1 and 65535",
		"SHORT must contain at least 32 bytes", "MISSING is required", "DB must be a URL with a host and scheme postgres"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "SHORT must contain at least 32 bytes (") {
		t.Error("message must not reveal the actual secret length")
	}
}

func TestReaderDurationBounds(t *testing.T) {
	cases := []struct {
		raw      string
		min, max time.Duration
		wantErr  string
	}{
		{"500ms", time.Second, 0, "at least 1s"},
		{"2h", time.Second, time.Hour, "between 1s and 1h0m0s"},
		{"90m", time.Second, 0, ""},
	}
	for _, tc := range cases {
		r := config.NewReader(env(map[string]string{"D": tc.raw}))
		r.Duration("D", time.Second, tc.min, tc.max)
		err := r.Err()
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.raw, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error = %v, want substring %q", tc.raw, err, tc.wantErr)
		}
	}
}

func TestReaderPortAndAddressBounds(t *testing.T) {
	for _, bad := range []string{"0", "65536", "-1", "http", "80.5"} {
		r := config.NewReader(env(map[string]string{"P": bad}))
		r.Port("P", 1)
		if r.Err() == nil {
			t.Errorf("Port accepted %q", bad)
		}
	}
	for _, bad := range []string{"localhost", "host:0", "host:70000", "host:port", ""} {
		if bad == "" {
			continue // blank means unset
		}
		r := config.NewReader(env(map[string]string{"A": bad}))
		r.Address("A", ":1")
		if r.Err() == nil {
			t.Errorf("Address accepted %q", bad)
		}
	}
}

func TestReaderBlankValueIsUnset(t *testing.T) {
	r := config.NewReader(env(map[string]string{"X": "   "}))
	r.Required("X")
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "X is required") {
		t.Fatalf("blank value must count as missing, got %v", err)
	}
}

func TestReaderReadsProcessEnvironmentWhenLookupIsNil(t *testing.T) {
	t.Setenv("CONFIG_TEST_PORT", "7777")
	if got := config.NewReader(nil).Port("CONFIG_TEST_PORT", 1); got != 7777 {
		t.Fatalf("Port = %d, want 7777 from the process environment", got)
	}
}
