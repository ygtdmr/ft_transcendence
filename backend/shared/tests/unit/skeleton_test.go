package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/skeleton"
)

// skCredential is a 32+ byte caller credential, as `openssl rand -hex 32` makes.
var skCredential = strings.Repeat("c", 64)

// skBuffer is a concurrency-safe log sink.
type skBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *skBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *skBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func skFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func skLookup(values map[string]string) config.LookupFunc {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

func skCredentialFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caller-credential")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

func skGet(t *testing.T, url, credential string) (int, http.Header, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, resp.Header, body
}

func skCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// skStart runs skeleton.Main and waits until the public listener answers.
func skStart(t *testing.T, values map[string]string, logs io.Writer) (cancel func() error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- skeleton.Main(ctx, skLookup(values), logs) }()
	health := fmt.Sprintf("http://127.0.0.1:%s/healthz", values["PORT"])
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, err := http.Get(health); err == nil {
			resp.Body.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Main returned before listening: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("the skeleton did not start listening")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() error {
		stop()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Main did not return after cancellation")
			return nil
		}
	}
}

// TestSkeletonStartsIndependentlyWithValidConfiguration covers the #83
// criterion "each service can start independently with valid configuration"
// on the template every service copies.
func TestSkeletonStartsIndependentlyWithValidConfiguration(t *testing.T) {
	publicPort, internalPort := skFreePort(t), skFreePort(t)
	logs := &skBuffer{}
	stop := skStart(t, map[string]string{
		"APP_ENV":                      "production",
		"PORT":                         strconv.Itoa(publicPort),
		"INTERNAL_HOST":                "127.0.0.1",
		"INTERNAL_PORT":                strconv.Itoa(internalPort),
		"USER_SERVICE_CREDENTIAL_FILE": skCredentialFile(t, skCredential),
		"SHUTDOWN_TIMEOUT":             "2s",
	}, logs)

	public := fmt.Sprintf("http://127.0.0.1:%d", publicPort)
	private := fmt.Sprintf("http://127.0.0.1:%d", internalPort)
	for _, path := range []string{"/healthz", "/readyz"} {
		if status, header, _ := skGet(t, public+path, ""); status != 200 || header.Get("X-Request-ID") == "" {
			t.Errorf("%s: %d, request id %q", path, status, header.Get("X-Request-ID"))
		}
	}
	status, _, body := skGet(t, private+skeleton.ExampleRoute, skCredential)
	data, _ := body["data"].(map[string]any)
	if status != 200 || data["caller"] != skeleton.ExampleCaller {
		t.Errorf("private route with the caller's credential: %d %v", status, body)
	}
	if status, header, body := skGet(t, private+skeleton.ExampleRoute, ""); status != 401 ||
		skCode(body) != "SERVICE_UNAUTHENTICATED" || header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("private route without a credential: %d %v", status, body)
	}
	if status, _, body := skGet(t, public+skeleton.ExampleRoute, skCredential); status != 404 || skCode(body) != "NOT_FOUND" {
		t.Errorf("the public listener served a private route: %d %v", status, body)
	}
	if status, _, body := skGet(t, private+"/healthz", ""); status != 404 || skCode(body) != "NOT_FOUND" {
		t.Errorf("the private listener served a public route: %d %v", status, body)
	}
	req, _ := http.NewRequest(http.MethodPost, public+"/healthz", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 405 {
		t.Errorf("POST /healthz: %v %v, want 405", resp, err)
	} else {
		resp.Body.Close()
	}

	if err := stop(); err != nil {
		t.Fatalf("Main after shutdown: %v", err)
	}
	out := logs.String()
	for _, want := range []string{`"service":"skeleton-service"`, `"environment":"production"`, `"msg":"shutdown_complete"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s", want)
		}
	}
	if strings.Contains(out, skCredential) {
		t.Error("a credential reached the logs")
	}
}

// TestSkeletonRefusesInvalidConfigurationByNameOnly covers "missing or invalid
// required configuration causes startup to fail with a clear error" and
// "configuration errors do not print secret values".
func TestSkeletonRefusesInvalidConfigurationByNameOnly(t *testing.T) {
	shortSecret := "short-secret-value"
	secretPath := skCredentialFile(t, shortSecret)
	port := skFreePort(t)
	values := map[string]string{
		"APP_ENV":                      "staging",
		"LOG_LEVEL":                    "loud",
		"PORT":                         "99999",
		"INTERNAL_HOST":                "evil.example:9999",
		"INTERNAL_PORT":                strconv.Itoa(port),
		"USER_SERVICE_CREDENTIAL_FILE": secretPath,
		"SHUTDOWN_TIMEOUT":             "1h",
	}
	// A regression that starts the service anyway must fail here, not hang.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- skeleton.Main(ctx, skLookup(values), io.Discard) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("Main started serving with an invalid configuration")
	}
	if err == nil {
		t.Fatal("Main accepted an invalid configuration")
	}
	msg := err.Error()
	for _, name := range []string{"APP_ENV", "LOG_LEVEL", "PORT", "INTERNAL_HOST", "USER_SERVICE_CREDENTIAL_FILE", "SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(msg, name) {
			t.Errorf("the error does not name %s: %s", name, msg)
		}
	}
	for _, leaked := range []string{"staging", "loud", "99999", "evil.example", secretPath, shortSecret, "1h"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("the error leaks %q: %s", leaked, msg)
		}
	}
	// Nothing was started: the private port is still free.
	ln, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if listenErr != nil {
		t.Fatalf("a listener was opened despite the invalid configuration: %v", listenErr)
	}
	ln.Close()
}

// TestSkeletonFailsClosedWithoutCallerCredentials: an unconfigured caller is
// refused, the service still starts, and the missing credential is reported.
func TestSkeletonFailsClosedWithoutCallerCredentials(t *testing.T) {
	publicPort, internalPort := skFreePort(t), skFreePort(t)
	logs := &skBuffer{}
	stop := skStart(t, map[string]string{
		"APP_ENV":       "test",
		"LOG_FORMAT":    "json",
		"LOG_LEVEL":     "warn", // tests log at error by default
		"PORT":          strconv.Itoa(publicPort),
		"INTERNAL_HOST": "127.0.0.1",
		"INTERNAL_PORT": strconv.Itoa(internalPort),
	}, logs)
	defer func() { _ = stop() }()

	if status, _, body := skGet(t, fmt.Sprintf("http://127.0.0.1:%d%s", internalPort, skeleton.ExampleRoute), skCredential); status != 401 || skCode(body) != "SERVICE_UNAUTHENTICATED" {
		t.Errorf("an unconfigured caller was accepted: %d %v", status, body)
	}
	if !strings.Contains(logs.String(), "private caller without a credential is refused") {
		t.Error("the missing caller credential was not reported at startup")
	}
}

// TestInternalAddressConvention pins the private listener setting every service
// reads: INTERNAL_HOST (optional) and INTERNAL_PORT.
func TestInternalAddressConvention(t *testing.T) {
	cases := []struct {
		host, port string
		want       string
	}{
		{"", "", ":8081"},
		{"", "9100", ":9100"},
		{"10.0.0.5", "", "10.0.0.5:8081"},
		{"auth-service-internal", "9000", "auth-service-internal:9000"},
		{"auth_service", "", "auth_service:8081"},
		{"::1", "", "[::1]:8081"},
	}
	for _, tc := range cases {
		values := map[string]string{}
		if tc.host != "" {
			values["INTERNAL_HOST"] = tc.host
		}
		if tc.port != "" {
			values["INTERNAL_PORT"] = tc.port
		}
		r := config.NewReader(skLookup(values))
		if got := r.InternalAddress(8081); got != tc.want || r.Err() != nil {
			t.Errorf("host %q port %q: %q (err %v), want %q", tc.host, tc.port, got, r.Err(), tc.want)
		}
	}
	for _, bad := range []string{"host:9000", "a b", "http://x", "-leading", strings.Repeat("a", 254)} {
		r := config.NewReader(skLookup(map[string]string{"INTERNAL_HOST": bad}))
		r.InternalAddress(8081)
		err := r.Err()
		if err == nil || !strings.Contains(err.Error(), "INTERNAL_HOST") || strings.Contains(err.Error(), bad) {
			t.Errorf("INTERNAL_HOST %q: %v, want a problem naming the variable but not the value", bad, err)
		}
	}
}
