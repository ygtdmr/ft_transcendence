package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

type profileReaderStub struct {
	username string
	err      error
}

func (s *profileReaderStub) CanonicalUsername(context.Context, int64) (string, error) {
	return s.username, s.err
}

func TestUserClientCanonicalUsername(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		match  error
	}{
		{"current name", http.StatusOK, `{"data":{"user_id":42,"username":"new_name"}}`, "new_name", nil},
		{"other user", http.StatusOK, `{"data":{"user_id":7,"username":"x_name"}}`, "", service.ErrProfileUnavailable},
		{"empty body", http.StatusOK, `{}`, "", service.ErrProfileUnavailable},
		{"outage", http.StatusServiceUnavailable, `{}`, "", service.ErrProfileUnavailable},
		{"deleted", http.StatusConflict, `{"error":{"code":"USER_DELETED"}}`, "", service.ErrProfileDeleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/profiles/42" ||
					r.Header.Get("Authorization") != "Bearer "+authToUserCredential {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, _ := userclient.New(server.URL, authToUserCredential, time.Second)
			got, err := client.CanonicalUsername(context.Background(), 42)
			if got != tc.want || !errors.Is(err, tc.match) {
				t.Fatalf("CanonicalUsername() = %q, %v", got, err)
			}
		})
	}
}

func TestUserClientCanonicalUsernameMissingProfileIsNotOutage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"USER_NOT_FOUND"}}`))
	}))
	defer server.Close()
	client, _ := userclient.New(server.URL, authToUserCredential, time.Second)
	_, err := client.CanonicalUsername(context.Background(), 42)
	if err == nil || errors.Is(err, service.ErrProfileUnavailable) || errors.Is(err, service.ErrProfileDeleted) {
		t.Fatalf("CanonicalUsername() = %v", err)
	}
}

func TestLoginAndSessionCanonicalUsernameFailures(t *testing.T) {
	cases := []struct {
		name         string
		read         error
		login, check error
	}{
		{"outage", service.ErrProfileUnavailable, service.ErrServiceUnavailable, service.ErrServiceUnavailable},
		{"deleted", service.ErrProfileDeleted, service.ErrInvalidCredentials, service.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &profileReaderStub{username: "current_name"}
			auth, repo, registered := registeredSession(t, service.WithProfileReader(reader))
			input := testutil.ValidInput()
			login := service.LoginInput{Email: input.Email, Password: input.Password}
			identity, err := auth.Authenticate(context.Background(), registered.Credentials.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := auth.CurrentSession(context.Background(), identity); err != nil || result.User.Username != "current_name" {
				t.Fatalf("CurrentSession() = %+v, %v", result.User, err)
			}

			reader.err = tc.read
			sessions := len(repo.Sessions)
			if _, err := auth.Login(context.Background(), login); !errors.Is(err, tc.login) {
				t.Fatalf("Login() = %v, want %v", err, tc.login)
			}
			if len(repo.Sessions) != sessions {
				t.Fatal("failed profile read created a session")
			}
			if _, err := auth.CurrentSession(context.Background(), identity); !errors.Is(err, tc.check) {
				t.Fatalf("CurrentSession() = %v, want %v", err, tc.check)
			}
		})
	}
}

func TestRegisteredUsernameIsNotUniqueInAuth(t *testing.T) {
	auth, _, _ := registeredSession(t)
	input := testutil.ValidInput()
	input.Email = "second@example.com"
	if _, err := auth.Register(context.Background(), input); err != nil {
		t.Fatalf("Register() = %v", err)
	}
}
