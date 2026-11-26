package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
)

// rawCapture records the last request a receiver saw.
type rawCapture struct {
	mu     sync.Mutex
	header http.Header
	body   []byte
	hits   int
}

func newRawReceiver(t *testing.T) (*rawCapture, *privateclient.Client) {
	t.Helper()
	capture := &rawCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.header, capture.body = r.Header.Clone(), body
		capture.hits++
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"reply":{"type":"ack"},"closures":[]}}`)
	}))
	t.Cleanup(srv.Close)
	c, err := privateclient.New(privateclient.Config{BaseURL: srv.URL, Credential: config.NewSecret(strings.Repeat("k", 40)),
		Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	return capture, c
}

// TestPrivateClientForwardsARawBodyUnchanged: a client WebSocket message is
// forwarded byte for byte, even when it is not valid JSON; the receiver judges it.
func TestPrivateClientForwardsARawBodyUnchanged(t *testing.T) {
	capture, c := newRawReceiver(t)
	for _, raw := range []string{
		`{"type":"room.role.select","request_id":"req_1","payload":{"team":"RED"}}`,
		`{"type": "x",   "payload": {}}  `,
		`not json at all {`,
	} {
		var out struct {
			Reply map[string]any `json:"reply"`
		}
		err := c.Do(context.Background(), privateclient.Request{Method: http.MethodPost,
			Path: "/internal/v1/rooms/1001/commands", RawBody: []byte(raw)}, &out)
		if err != nil {
			t.Fatalf("Do(%q): %v", raw, err)
		}
		capture.mu.Lock()
		got, ct := string(capture.body), capture.header.Get("Content-Type")
		capture.mu.Unlock()
		if got != raw || ct != "application/json" {
			t.Errorf("receiver got body %q content type %q, want %q unchanged as JSON", got, ct, raw)
		}
	}
}

func TestPrivateClientSendsContractHeadersButNeverOverridesCredentialsOrTracing(t *testing.T) {
	capture, c := newRawReceiver(t)
	err := c.Do(context.Background(), privateclient.Request{Method: http.MethodPost, Path: "/internal/v1/rooms/1001/commands",
		RawBody: []byte(`{}`), Header: http.Header{"x-user-id": {"42"}, "X-Session-Id": {"3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	h := capture.header
	capture.mu.Unlock()
	if h.Get("X-User-Id") != "42" || h.Get("X-Session-Id") != "3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53" {
		t.Errorf("contract headers not sent: %v", h)
	}
	if h.Get("Authorization") != "Bearer "+strings.Repeat("k", 40) {
		t.Error("the service credential was not sent")
	}

	before := capture.hits
	refused := []http.Header{
		{"Authorization": {"Bearer forged"}},
		{"Cookie": {"ft_session=forged"}},
		{"Host": {"evil.example"}},
		{"Content-Type": {"text/plain"}},
		{"X-Request-ID": {"forged"}},
		{"X-Forwarded-For": {"10.0.0.1"}},
		{"X-Real-IP": {"10.0.0.1"}},
		{"X-": {"x"}},
		{"X-User-Id": {"42\r\nX-Injected: 1"}},
	}
	for _, header := range refused {
		err := c.Do(context.Background(), privateclient.Request{Method: http.MethodPost,
			Path: "/internal/v1/rooms/1001/commands", RawBody: []byte(`{}`), Header: header}, nil)
		if !errors.Is(err, privateclient.ErrInvalidRequest) {
			t.Errorf("header %v: %v, want ErrInvalidRequest", header, err)
		}
	}
	if err := c.Do(context.Background(), privateclient.Request{Method: http.MethodPost, Path: "/internal/v1/x",
		Body: map[string]any{}, RawBody: []byte(`{}`)}, nil); !errors.Is(err, privateclient.ErrInvalidRequest) {
		t.Errorf("Body with RawBody: %v, want ErrInvalidRequest", err)
	}
	if capture.hits != before {
		t.Errorf("%d refused requests were sent anyway", capture.hits-before)
	}
}
