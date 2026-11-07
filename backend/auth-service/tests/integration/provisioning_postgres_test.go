package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const provisioningCredential = "auth-to-user-credential-0123456789abcdef"

// userStub imitates User Service's provisioning operations.
//
// It answers each PUT with the next scripted response, or with the
// contract's normal behavior once the script is used up: 201 for a new
// operation and 200 with the same receipt for a replay.
type userStub struct {
	mu         sync.Mutex
	script     []stubAnswer
	operations []string
	receipts   map[int64]string
	lookup     string
	server     *httptest.Server
}

type stubAnswer struct {
	status int
	code   string
}

func newUserStub(t *testing.T) *userStub {
	t.Helper()
	stub := &userStub{receipts: map[int64]string{}}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)

	return stub
}

func (s *userStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+provisioningCredential {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"operation_id": s.lookup, "user_id": 1, "outcome": "PROVISIONED", "completed_at": "2026-11-05T09:30:00.000000Z",
		}})
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/internal/v1/provisioning-operations/")
	s.operations = append(s.operations, operation)
	var body struct {
		UserID int64 `json:"user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(s.script) > 0 {
		answer := s.script[0]
		s.script = s.script[1:]
		w.WriteHeader(answer.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": answer.code, "message": "x", "details": map[string]any{}}})
		return
	}
	status := http.StatusCreated
	if s.receipts[body.UserID] == operation {
		status = http.StatusOK
	}
	s.receipts[body.UserID] = operation
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"operation_id": operation, "user_id": body.UserID, "outcome": "PROVISIONED", "completed_at": "2026-11-05T09:30:00.000000Z",
	}})
}

func (s *userStub) answer(answers ...stubAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, answers...)
}

func (s *userStub) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.operations...)
}

// provisioningAuth builds an AuthService over pool that provisions through stub.
func provisioningAuth(t *testing.T, pool *pgxpool.Pool, stub *userStub) *service.AuthService {
	t.Helper()
	client, err := userclient.New(stub.server.URL, provisioningCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t), service.WithProfileProvisioner(client))
}

type accountState struct {
	status, operationState string
	attempts               int
	operationID            string
	sessions               int
}

func readAccount(t *testing.T, pool *pgxpool.Pool) (accountState, bool) {
	t.Helper()
	var state accountState
	err := pool.QueryRow(context.Background(), `
		SELECT u.status, p.state, p.attempts, p.operation_id::text,
		       (SELECT count(*) FROM sessions s WHERE s.user_id = u.id)
		FROM users u JOIN provisioning_operations p ON p.user_id = u.id`).
		Scan(&state.status, &state.operationState, &state.attempts, &state.operationID, &state.sessions)
	if err != nil {
		var count int
		_ = pool.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&count)
		if count == 0 {
			return accountState{}, false
		}
		t.Fatalf("read account: %v", err)
	}

	return state, true
}

// TestProvisioningRegistrationPostgres checks registration with confirmed provisioning.
//
// Expected output:
//
//   - The account is created PROVISIONING with a stored operation, sent once,
//     then becomes ACTIVE with a COMPLETED operation and one session.
func TestProvisioningRegistrationPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	auth := provisioningAuth(t, pool, stub)

	result, err := auth.Register(context.Background(), testutil.ValidInput())
	if err != nil || result.Credentials.AccessToken == "" || result.User.Status != "ACTIVE" {
		t.Fatalf("Register() = %+v, %v", result.User, err)
	}
	state, _ := readAccount(t, pool)
	if state.status != "ACTIVE" || state.operationState != "COMPLETED" || state.sessions != 1 {
		t.Fatalf("state = %+v", state)
	}
	if sent := stub.sent(); len(sent) != 1 || sent[0] != state.operationID {
		t.Fatalf("sent operations = %v, want [%s]", sent, state.operationID)
	}
}

// TestProvisioningOutageAndResumePostgres checks outages, restarts and resumption.
//
// Expected output:
//
//   - An outage answers ErrServiceUnavailable, issues no session and keeps the
//     account PROVISIONING with its operation PENDING.
//   - A restarted service resumes through a repeated registration, and later
//     through login, always resending the original operation id.
//   - A lost response after User committed (replay 200) still activates once.
func TestProvisioningOutageAndResumePostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	stub.answer(stubAnswer{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"}, stubAnswer{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"})
	input := testutil.ValidInput()

	if _, err := provisioningAuth(t, pool, stub).Register(context.Background(), input); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("Register() during outage error = %v", err)
	}
	state, _ := readAccount(t, pool)
	if state.status != "PROVISIONING" || state.operationState != "PENDING" || state.attempts != 1 || state.sessions != 0 {
		t.Fatalf("state after outage = %+v", state)
	}
	original := state.operationID

	restarted := provisioningAuth(t, pool, stub)
	if _, err := restarted.Register(context.Background(), input); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("repeated Register() during outage error = %v", err)
	}
	other := input
	other.Username = "someone_else"
	if _, err := restarted.Register(context.Background(), other); !errors.Is(err, service.ErrEmailTaken) {
		t.Fatalf("Register() with another username error = %v, want ErrEmailTaken", err)
	}
	wrong := input
	wrong.Password = "Different-Passw0rd!"
	if _, err := restarted.Register(context.Background(), wrong); !errors.Is(err, service.ErrEmailTaken) {
		t.Fatalf("Register() with another password error = %v, want ErrEmailTaken", err)
	}

	result, err := restarted.Login(context.Background(), service.LoginInput{Email: input.Email, Password: input.Password})
	if err != nil || result.Credentials.AccessToken == "" {
		t.Fatalf("Login() resume = %v", err)
	}
	state, _ = readAccount(t, pool)
	if state.status != "ACTIVE" || state.operationState != "COMPLETED" || state.operationID != original || state.sessions != 1 {
		t.Fatalf("state after resume = %+v", state)
	}
	for _, sent := range stub.sent() {
		if sent != original {
			t.Fatalf("sent operations = %v, want only %s", stub.sent(), original)
		}
	}

	// A response lost after User committed: the replay is 200 and changes nothing.
	if _, err := restarted.Login(context.Background(), service.LoginInput{Email: input.Email, Password: input.Password}); err != nil {
		t.Fatalf("Login() on ACTIVE account = %v", err)
	}
	if got := len(stub.sent()); got != 3 {
		t.Fatalf("%d provisioning calls, want 3 (an ACTIVE login sends none)", got)
	}
}

// TestProvisioningTerminalAnswersPostgres checks answers that must not issue sessions.
//
// Expected output:
//
//   - USERNAME_TAKEN and INVALID_USERNAME discard the provisional account.
//   - USER_DELETED, OPERATION_CONFLICT and a different operation of record
//     mark the operation FAILED, keep the account PROVISIONING and are never retried.
//   - USER_ALREADY_PROVISIONED with this operation of record activates the account.
func TestProvisioningTerminalAnswersPostgres(t *testing.T) {
	for _, tt := range []struct {
		name    string
		answer  stubAnswer
		lookup  string
		want    error
		removed bool
	}{
		{"username taken", stubAnswer{http.StatusConflict, "USERNAME_TAKEN"}, "", service.ErrUsernameTaken, true},
		{"invalid username", stubAnswer{http.StatusUnprocessableEntity, "INVALID_USERNAME"}, "", service.ErrInvalidUsername, true},
		{"user deleted", stubAnswer{http.StatusConflict, "USER_DELETED"}, "", service.ErrProvisioningFailed, false},
		{"operation conflict", stubAnswer{http.StatusConflict, "OPERATION_CONFLICT"}, "", service.ErrProvisioningFailed, false},
		{"other operation of record", stubAnswer{http.StatusConflict, "USER_ALREADY_PROVISIONED"}, "11111111-1111-4111-8111-111111111111", service.ErrProvisioningFailed, false},
		{"same operation of record", stubAnswer{http.StatusConflict, "USER_ALREADY_PROVISIONED"}, "self", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := registrationTestPool(t)
			stub := newUserStub(t)
			stub.answer(tt.answer)
			if tt.lookup == "self" {
				// The stub reports the operation Auth sent as the operation of record.
				stub.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						sent := stub.sent()
						stub.mu.Lock()
						stub.lookup = sent[len(sent)-1]
						stub.mu.Unlock()
					}
					stub.serve(w, r)
				})
			} else {
				stub.lookup = tt.lookup
			}
			auth := provisioningAuth(t, pool, stub)
			input := testutil.ValidInput()

			_, err := auth.Register(context.Background(), input)
			if tt.want == nil {
				state, _ := readAccount(t, pool)
				if err != nil || state.status != "ACTIVE" || state.sessions != 1 {
					t.Fatalf("Register() = %v, state %+v", err, state)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Register() error = %v, want %v", err, tt.want)
			}
			state, exists := readAccount(t, pool)
			if tt.removed {
				if exists {
					t.Fatalf("provisional account kept: %+v", state)
				}
				return
			}
			if state.status != "PROVISIONING" || state.operationState != "FAILED" || state.sessions != 0 {
				t.Fatalf("state = %+v, want PROVISIONING with a FAILED operation", state)
			}
			calls := len(stub.sent())
			if _, err := auth.Login(context.Background(), service.LoginInput{Email: input.Email, Password: input.Password}); !errors.Is(err, service.ErrProvisioningFailed) {
				t.Fatalf("Login() after a terminal answer = %v", err)
			}
			if len(stub.sent()) != calls {
				t.Fatal("a FAILED operation was retried")
			}
		})
	}
}

// TestProvisioningDeletingAccountLoginPostgres checks that DELETING accounts get no session.
func TestProvisioningDeletingAccountLoginPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	auth := provisioningAuth(t, pool, newUserStub(t))
	input := testutil.ValidInput()
	if _, err := auth.Register(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE users SET status = 'DELETING'"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(context.Background(), service.LoginInput{Email: input.Email, Password: input.Password}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("Login() on DELETING account = %v, want ErrInvalidCredentials", err)
	}
}
