package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/statsclient"
)

// TestStatsClientUserStats checks the request and each response outcome.
//
// Expected output: the bearer credential and path are sent; a valid body is
// decoded; invalid counters, a wrong user, 5xx and network failures are
// ErrUnavailable; 401/403 are ErrMisconfigured.
func TestStatsClientUserStats(t *testing.T) {
	credential := strings.Repeat("s", 40)
	valid := `{"data":{"user_id":42,"level":4,"wins":12,"losses":8,"matches_played":20,"penalty_points":3}}`
	for _, tt := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"valid", http.StatusOK, valid, nil},
		{"wrong user", http.StatusOK, strings.Replace(valid, `"user_id":42`, `"user_id":43`, 1), statsclient.ErrUnavailable},
		{"missing field", http.StatusOK, `{"data":{"user_id":42,"level":1,"wins":0,"losses":0,"matches_played":0}}`, statsclient.ErrUnavailable},
		{"inconsistent total", http.StatusOK, strings.Replace(valid, `"matches_played":20`, `"matches_played":21`, 1), statsclient.ErrUnavailable},
		{"level zero", http.StatusOK, strings.Replace(valid, `"level":4`, `"level":0`, 1), statsclient.ErrUnavailable},
		{"malformed", http.StatusOK, `{`, statsclient.ErrUnavailable},
		{"unavailable", http.StatusServiceUnavailable, `{}`, statsclient.ErrUnavailable},
		{"forbidden", http.StatusForbidden, `{}`, statsclient.ErrMisconfigured},
		{"unauthenticated", http.StatusUnauthorized, `{}`, statsclient.ErrMisconfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/user-stats/42" || r.Header.Get("Authorization") != "Bearer "+credential {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := statsclient.New(server.URL, credential, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := client.UserStats(context.Background(), 42)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			want := statsclient.UserStats{UserID: 42, Level: 4, Wins: 12, Losses: 8, MatchesPlayed: 20, PenaltyPoints: 3}
			if err != nil || stats != want {
				t.Fatalf("stats = %+v, %v", stats, err)
			}
		})
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client, _ := statsclient.New(closed.URL, credential, time.Second)
	if _, err := client.UserStats(context.Background(), 42); !errors.Is(err, statsclient.ErrUnavailable) {
		t.Fatalf("unreachable = %v", err)
	}
	for _, origin := range []string{"ftp://stats", "http://stats/path"} {
		if _, err := statsclient.New(origin, credential, time.Second); err == nil {
			t.Fatalf("origin %q accepted", origin)
		}
	}
}
