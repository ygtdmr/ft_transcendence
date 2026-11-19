package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// plainClient opens a fresh connection for every request. The default client keeps
// idle connections and may dial a spare one that the server sees as new but unused;
// net/http's Shutdown does not close such a connection for five seconds, which made
// these timing tests fail sporadically at exactly the shutdown timeout.
var plainClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func waitUp(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := plainClient.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never came up: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// order records the sequence of events across goroutines.
type order struct {
	mu     sync.Mutex
	events []string
}

func (o *order) add(e string) { o.mu.Lock(); o.events = append(o.events, e); o.mu.Unlock() }
func (o *order) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func index(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

func TestServeFinishesInFlightRequestsThenClosesComponentsInReverseOrder(t *testing.T) {
	addr := freeAddr(t)
	events := &order{}
	started, release := make(chan struct{}), make(chan struct{})
	health := lifecycle.NewHealth()

	mux := http.NewServeMux()
	mux.Handle("/readyz", health.Readiness())
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		events.add("request-finished")
		_, _ = w.Write([]byte("done"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- lifecycle.Serve(ctx, &http.Server{Addr: addr, Handler: mux}, lifecycle.Options{
			Health: health, ShutdownTimeout: 5 * time.Second, Logger: quiet,
			Components: []lifecycle.Component{
				lifecycle.Func("database", func() { events.add("closed:database") }),
				lifecycle.Func("worker", func() { events.add("closed:worker") }),
			},
		})
	}()
	waitUp(t, "http://"+addr+"/readyz")

	type reply struct {
		body string
		err  error
	}
	got := make(chan reply, 1)
	go func() {
		resp, err := plainClient.Get("http://" + addr + "/work")
		if err != nil {
			got <- reply{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- reply{body: string(b)}
	}()
	<-started
	cancel() // what SIGTERM does through signal.NotifyContext

	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	case <-time.After(300 * time.Millisecond):
	}
	if !health.Draining() {
		t.Error("health must be draining once shutdown started")
	}

	close(release)
	if r := <-got; r.err != nil || r.body != "done" {
		t.Fatalf("the in-flight request was not completed: %+v", r)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve = %v, want nil after a clean shutdown", err)
	}

	ev := events.list()
	if index(ev, "request-finished") < 0 || index(ev, "closed:worker") < index(ev, "request-finished") ||
		index(ev, "closed:database") < index(ev, "closed:worker") {
		t.Errorf("order = %v, want request-finished, closed:worker, closed:database", ev)
	}
	if _, err := plainClient.Get("http://" + addr + "/readyz"); err == nil {
		t.Error("the listener must be closed after shutdown")
	}
}

func TestServeDrainDelayKeepsServingWhileReportingNotReady(t *testing.T) {
	addr := freeAddr(t)
	health := lifecycle.NewHealth()
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Liveness())
	mux.Handle("/readyz", health.Readiness())

	// No keep-alive: the Go client otherwise leaves a spare idle-looking
	// connection that net/http treats as "new" for up to 5s, which would measure
	// the client rather than the shutdown sequence.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- lifecycle.Serve(ctx, &http.Server{Addr: addr, Handler: mux}, lifecycle.Options{
			Health: health, DrainDelay: 600 * time.Millisecond, Logger: quiet,
		})
	}()
	waitUp(t, "http://"+addr+"/healthz")

	begin := time.Now()
	cancel()
	time.Sleep(150 * time.Millisecond)

	ready, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatalf("during the drain delay the server must still accept connections: %v", err)
	}
	ready.Body.Close()
	live, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	live.Body.Close()
	if ready.StatusCode != 503 || live.StatusCode != 200 {
		t.Errorf("during drain: readyz=%d healthz=%d, want 503 and 200", ready.StatusCode, live.StatusCode)
	}

	if err := <-served; err != nil {
		t.Errorf("Serve = %v", err)
	}
	if elapsed := time.Since(begin); elapsed < 600*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("shutdown took %s, want about the 600ms drain delay", elapsed)
	}
}

func TestServeShutdownIsBoundedWhenRequestsNeverFinish(t *testing.T) {
	addr := freeAddr(t)
	events := &order{}
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // ends only when the server force-closes the connection
	})

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- lifecycle.Serve(ctx, &http.Server{Addr: addr, Handler: mux}, lifecycle.Options{
			ShutdownTimeout: 300 * time.Millisecond, Logger: quiet,
			Components: []lifecycle.Component{lifecycle.Func("database", func() { events.add("closed:database") })},
		})
	}()
	waitUp(t, "http://"+addr+"/nothing")

	clientErr := make(chan error, 1)
	go func() {
		resp, err := plainClient.Get("http://" + addr + "/hang")
		if err == nil {
			resp.Body.Close()
		}
		clientErr <- err
	}()
	<-started

	begin := time.Now()
	cancel()
	err := <-served
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s; it must be bounded by ShutdownTimeout (300ms)", elapsed)
	}
	if !errors.Is(err, lifecycle.ErrShutdownTimeout) {
		t.Errorf("Serve = %v, want ErrShutdownTimeout", err)
	}
	if e := <-clientErr; e == nil {
		t.Error("the stuck request's connection should have been closed by force")
	}
	if got := events.list(); len(got) != 1 {
		t.Errorf("components must still be closed after a forced shutdown, got %v", got)
	}
}

func TestServeClosesComponentsWhenTheListenerFails(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	events := &order{}
	err = lifecycle.Serve(context.Background(), &http.Server{Addr: occupied.Addr().String(), Handler: http.NewServeMux()}, lifecycle.Options{
		Logger:     quiet,
		Components: []lifecycle.Component{lifecycle.Func("database", func() { events.add("closed:database") })},
	})
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve = %v, want a listener error", err)
	}
	if len(events.list()) != 1 {
		t.Error("the pool must be closed even when the server never started")
	}
}

func TestServeBoundsAndIsolatesComponentFailures(t *testing.T) {
	addr := freeAddr(t)
	events := &order{}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- lifecycle.Serve(ctx, &http.Server{Addr: addr, Handler: http.NewServeMux()}, lifecycle.Options{
			CloseTimeout: 200 * time.Millisecond, Logger: quiet,
			Components: []lifecycle.Component{
				lifecycle.Func("first", func() { events.add("closed:first") }),
				{Name: "hangs", Close: func(context.Context) error { select {} }},
				{Name: "fails", Close: func(context.Context) error { return errors.New("dsn=postgres://u:hunter2@db/app") }},
				lifecycle.Func("last", func() { events.add("closed:last") }),
			},
		})
	}()
	waitUp(t, "http://"+addr+"/x")

	begin := time.Now()
	cancel()
	err := <-served
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Errorf("a hung component stalled shutdown for %s; CloseTimeout is 200ms", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "hangs") || !strings.Contains(err.Error(), "fails") {
		t.Errorf("failures must be reported by component name: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("a component's error text leaked: %v", err)
	}
	if ev := events.list(); len(ev) != 2 || ev[0] != "closed:last" || ev[1] != "closed:first" {
		t.Errorf("one failing component must not skip the others, got %v", ev)
	}
}

// TestServeHandlesARealSIGTERM runs the shutdown path in a real process: build
// a tiny service that wires Serve like a real main, start a slow request, send
// SIGTERM, and require the request to finish, components to close in reverse
// order, and a zero exit code.
func TestServeHandlesARealSIGTERM(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "lifecycle_server")
	build := exec.Command("go", "build", "-o", binary, "./tests/integration/testdata/lifecycle_server")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the test service: %v\n%s", err, out)
	}

	addr := freeAddr(t)
	var stdout bytes.Buffer
	var mu sync.Mutex
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "ADDR="+addr)
	cmd.Stdout = writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return stdout.Write(p) })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	snapshot := func() string { mu.Lock(); defer mu.Unlock(); return stdout.String() }

	waitUp(t, "http://"+addr+"/healthz")
	body := make(chan string, 1)
	go func() {
		resp, err := plainClient.Get("http://" + addr + "/slow")
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()
	for deadline := time.Now().Add(3 * time.Second); !strings.Contains(snapshot(), "slow-started"); {
		if time.Now().After(deadline) {
			t.Fatalf("slow request never started; output: %q", snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("the process must exit 0 after SIGTERM, got %v; output: %q", err, snapshot())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("the process did not exit after SIGTERM; output: %q", snapshot())
	}

	if got := <-body; got != "done" {
		t.Errorf("the in-flight request got %q, want it completed", got)
	}
	lines := strings.Fields(snapshot())
	if index(lines, "slow-finished") < 0 || index(lines, "closed:worker") < index(lines, "slow-finished") ||
		index(lines, "closed:database") < index(lines, "closed:worker") || index(lines, "exit-clean") < index(lines, "closed:database") {
		t.Errorf("markers out of order: %v", lines)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestReadinessTracksARealPostgresOutage(t *testing.T) {
	dsn := requireEnv(t, "POSTGRES_TEST_DATABASE_URL")
	container := requireEnv(t, "POSTGRES_TEST_CONTAINER")
	ctx := context.Background()

	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	health := lifecycle.NewHealth()
	health.Add(lifecycle.Check{Name: "postgres", Fn: func(ctx context.Context) error { return postgres.Check(ctx, pool, time.Second) }})
	status := func() int {
		rec := &statusRecorder{header: http.Header{}}
		health.Readiness().ServeHTTP(rec, httpRequest())
		return rec.status
	}

	if got := status(); got != 200 {
		t.Fatalf("readyz with a healthy database = %d", got)
	}
	docker(t, "stop", container)
	t.Cleanup(func() { _ = exec.Command("docker", "start", container).Run() })
	if got := status(); got != 503 {
		t.Fatalf("readyz during the outage = %d, want 503", got)
	}
	docker(t, "start", container)
	if err := eventually(t, 60*time.Second, func() error {
		if status() != 200 {
			return errors.New("not ready yet")
		}
		return nil
	}); err != nil {
		t.Fatalf("readiness did not recover after the database restarted: %v", err)
	}
}

type statusRecorder struct {
	header http.Header
	status int
}

func (r *statusRecorder) Header() http.Header         { return r.header }
func (r *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (r *statusRecorder) WriteHeader(code int)        { r.status = code }

func httpRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	return req
}
