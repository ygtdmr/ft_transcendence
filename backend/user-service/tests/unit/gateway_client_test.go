package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/gatewayclient"
)

// TestGatewayConnected checks the request shape and each response outcome.
//
// Expected output: repeated user_id parameters and the bearer credential are
// sent; a subset answer is returned; malformed, unrequested, 5xx and redirect
// answers are ErrUnavailable; 401/403 are ErrMisconfigured.
func TestGatewayConnected(t *testing.T) {
	credential := strings.Repeat("g", 40)
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   []int64
		err    error
	}{
		{"subset", http.StatusOK, `{"data":{"connected_user_ids":[42]}}`, []int64{42}, nil},
		{"none", http.StatusOK, `{"data":{"connected_user_ids":[]}}`, []int64{}, nil},
		{"missing field", http.StatusOK, `{"data":{}}`, nil, gatewayclient.ErrUnavailable},
		{"unrequested user", http.StatusOK, `{"data":{"connected_user_ids":[99]}}`, nil, gatewayclient.ErrUnavailable},
		{"malformed", http.StatusOK, `not json`, nil, gatewayclient.ErrUnavailable},
		{"server error", http.StatusInternalServerError, `{}`, nil, gatewayclient.ErrUnavailable},
		{"redirect", http.StatusFound, `{}`, nil, nil},
		{"unauthenticated", http.StatusUnauthorized, `{}`, nil, gatewayclient.ErrMisconfigured},
		{"forbidden", http.StatusForbidden, `{}`, nil, gatewayclient.ErrMisconfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/connections" || r.Header.Get("Authorization") != "Bearer "+credential ||
					!slices.Equal(r.URL.Query()["user_id"], []string{"42", "43"}) {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				if tt.status == http.StatusFound {
					w.Header().Set("Location", "http://example.invalid/")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := gatewayclient.New(server.URL, credential, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Connected(context.Background(), []int64{42, 43})
			if tt.status == http.StatusFound {
				if err == nil || errors.Is(err, gatewayclient.ErrUnavailable) {
					t.Fatalf("redirect = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.err) || (tt.err == nil && err != nil) || !slices.Equal(got, tt.want) {
				t.Fatalf("Connected = %v, %v; want %v, %v", got, err, tt.want, tt.err)
			}
		})
	}
}

// TestGatewayConnectedFailures checks network failure, timeout and bad input.
//
// Expected output: an unreachable or slow instance is ErrUnavailable; an empty
// or oversized ID list and a bad origin are refused without a request.
func TestGatewayConnectedFailures(t *testing.T) {
	credential := strings.Repeat("g", 40)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer slow.Close()
	client, err := gatewayclient.New(slow.URL, credential, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connected(context.Background(), []int64{1}); !errors.Is(err, gatewayclient.ErrUnavailable) {
		t.Fatalf("timeout = %v", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client, _ = gatewayclient.New(closed.URL, credential, time.Second)
	if _, err := client.Connected(context.Background(), []int64{1}); !errors.Is(err, gatewayclient.ErrUnavailable) {
		t.Fatalf("unreachable = %v", err)
	}
	if _, err := client.Connected(context.Background(), nil); err == nil || errors.Is(err, gatewayclient.ErrUnavailable) {
		t.Fatalf("empty = %v", err)
	}
	if _, err := client.Connected(context.Background(), make([]int64, 101)); err == nil {
		t.Fatal("oversized list accepted")
	}

	for _, origin := range []string{"ftp://gateway", "http://gateway/path", ""} {
		if _, err := gatewayclient.New(origin, credential, time.Second); err == nil {
			t.Fatalf("origin %q accepted", origin)
		}
	}
	if _, err := gatewayclient.New("http://gateway", "", time.Second); err == nil {
		t.Fatal("empty credential accepted")
	}
}
