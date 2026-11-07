package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/serviceauth"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const (
	deletionOperation = "3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53"
	otherOperation    = "a7c40e19-6b2d-4f3a-8e95-1d0b7c6f2a48"
)

// revocationRecorder records every revocation notification and its cause.
type revocationRecorder struct {
	mu     sync.Mutex
	causes map[string]string
}

func (r *revocationRecorder) CurrentMembership(context.Context, int64) (service.Membership, error) {
	return service.Membership{}, nil
}

func (r *revocationRecorder) EndSession(context.Context, int64, string) error { return nil }

func (r *revocationRecorder) SessionRevoked(_ context.Context, _ int64, sessionID, cause string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.causes[sessionID] = cause
	return nil
}

type deletionFixture struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	auth     *service.AuthService
	recorder *revocationRecorder
	original service.AuthResult
	internal http.Handler
	public   http.Handler
}

// newDeletionFixture registers one ACTIVE account with one session on PostgreSQL.
func newDeletionFixture(t *testing.T) deletionFixture {
	t.Helper()
	pool := registrationTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	recorder := &revocationRecorder{causes: map[string]string{}}
	auth := service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t), service.WithSessionLifecycle(recorder))
	original, err := auth.Register(ctx, testutil.ValidInput())
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := serviceauth.New(map[string][]string{
		"game-gateway": {gatewayCredential},
		"user-service": {userCredential},
	}, serviceauth.DefaultMatrix())
	internal := server.NewInternal("0", logger, controller.NewInternalReads(auth, services),
		controller.NewAccountDeletion(auth, services)).Handler
	public := server.New("0", controller.NewAuth(auth), logger).Handler

	return deletionFixture{ctx: ctx, pool: pool, auth: auth, recorder: recorder, original: original,
		internal: internal, public: public}
}

func deletionCall(handler http.Handler, path, body, credential string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(http.MethodPut, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func (f deletionFixture) begin(operationID string, userID int64) *httptest.ResponseRecorder {
	return deletionCall(f.internal, "/internal/v1/account-deletions/"+operationID,
		`{"user_id":`+strconv.FormatInt(userID, 10)+`}`, userCredential)
}

func (f deletionFixture) complete(operationID string) *httptest.ResponseRecorder {
	return deletionCall(f.internal, "/internal/v1/account-deletions/"+operationID+"/completion", "", userCredential)
}

func (f deletionFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func expectReceipt(t *testing.T, response *httptest.ResponseRecorder, status int, userID int64, state string) map[string]any {
	t.Helper()
	data, _, _ := internalBody(t, response)
	if response.Code != status || data["operation_id"] != deletionOperation || data["user_id"] != float64(userID) ||
		data["state"] != state || len(data) != 5 {
		t.Fatalf("receipt = %d %s, want %d %s", response.Code, response.Body, status, state)
	}
	if _, err := time.Parse(time.RFC3339Nano, data["begun_at"].(string)); err != nil {
		t.Fatalf("begun_at = %v", data["begun_at"])
	}
	if (state == "BEGUN") != (data["completed_at"] == nil) {
		t.Fatalf("completed_at = %v for state %s", data["completed_at"], state)
	}
	return data
}

func expectDeletionError(t *testing.T, response *httptest.ResponseRecorder, status int, code, key string, value any) {
	t.Helper()
	_, got, details := internalBody(t, response)
	if response.Code != status || got != code || details[key] != value {
		t.Fatalf("response = %d %s, want %d %s with %s=%v", response.Code, response.Body, status, code, key, value)
	}
}

// TestAccountDeletionBeginPostgres checks Begin on a real database.
//
// Expected output:
//
//   - 201 with a BEGUN receipt; the account is DELETING, every session and
//     refresh token is revoked, and each newly revoked session is queued and
//     notified with cause ACCOUNT_DELETED.
//   - An identical retry returns 200 with the same receipt.
//   - Login, refresh and session validation issue or accept nothing afterwards.
//   - Conflicting operations and unknown users get their distinct errors.
func TestAccountDeletionBeginPostgres(t *testing.T) {
	f := newDeletionFixture(t)
	userID := f.original.User.ID
	input := testutil.ValidInput()
	second, err := f.auth.Login(f.ctx, service.LoginInput{Email: input.Email, Password: input.Password})
	if err != nil {
		t.Fatal(err)
	}

	first := expectReceipt(t, f.begin(deletionOperation, userID), http.StatusCreated, userID, "BEGUN")
	if n := f.count(t, `SELECT count(*) FROM users WHERE status = 'DELETING'`); n != 1 {
		t.Fatalf("DELETING accounts = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`); n != 0 {
		t.Fatalf("open sessions = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM refresh_tokens WHERE revoked_at IS NULL`); n != 0 {
		t.Fatalf("open refresh tokens = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM session_revocations WHERE cause = 'ACCOUNT_DELETED'`); n != 2 {
		t.Fatalf("ACCOUNT_DELETED revocations = %d, want 2", n)
	}
	for _, id := range []string{f.original.Credentials.Session.ID, second.Credentials.Session.ID} {
		if f.recorder.causes[id] != service.RevocationAccountDeleted {
			t.Fatalf("session %s notified with %q", id, f.recorder.causes[id])
		}
	}

	replay := expectReceipt(t, f.begin(deletionOperation, userID), http.StatusOK, userID, "BEGUN")
	if replay["begun_at"] != first["begun_at"] {
		t.Fatalf("replay begun_at = %v, want %v", replay["begun_at"], first["begun_at"])
	}
	if n := f.count(t, `SELECT count(*) FROM session_revocations`); n != 2 {
		t.Fatalf("revocations after replay = %d, want 2", n)
	}

	if _, err := f.auth.Login(f.ctx, service.LoginInput{Email: input.Email, Password: input.Password}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("login after Begin = %v, want ErrInvalidCredentials", err)
	}
	if _, err := f.auth.Refresh(f.ctx, second.Credentials.RefreshToken); !errors.Is(err, service.ErrSessionExpired) {
		t.Fatalf("refresh after Begin = %v, want ErrSessionExpired", err)
	}
	if _, err := f.auth.Authenticate(f.ctx, second.Credentials.AccessToken); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("authenticate after Begin = %v, want ErrUnauthorized", err)
	}
	metadata, _, _ := internalBody(t, internalCall(f.internal, "/internal/v1/accounts/"+strconv.FormatInt(userID, 10), userCredential, ""))
	if metadata["status"] != "DELETING" {
		t.Fatalf("metadata status = %v, want DELETING", metadata["status"])
	}

	expectDeletionError(t, f.begin(otherOperation, userID), http.StatusConflict, "DELETION_CONFLICT", "user_id", float64(userID))
	expectDeletionError(t, f.begin(deletionOperation, 999999), http.StatusConflict, "OPERATION_CONFLICT", "operation_id", deletionOperation)
	expectDeletionError(t, f.begin("6B1F0C2E-9A4D-4C7E-8F3B-2D5E7A9C1B40", 999999), http.StatusNotFound,
		"USER_NOT_FOUND", "user_id", float64(999999))
	upper := expectReceipt(t, f.begin(strings.ToUpper(deletionOperation), userID), http.StatusOK, userID, "BEGUN")
	if upper["begun_at"] != first["begun_at"] {
		t.Fatal("an upper-case operation_id must match the stored lowercase operation")
	}
}

// TestAccountDeletionCompletePostgres checks Complete and the retained record.
//
// Expected output:
//
//   - Complete before Begin returns 404 DELETION_NOT_FOUND and changes nothing.
//   - Complete removes the account, sessions, refresh tokens and Google link,
//     keeps queued revocations, and returns a COMPLETED receipt; retries of
//     Complete and Begin return the same COMPLETED receipt.
//   - The email and username register again with a new, larger user_id.
func TestAccountDeletionCompletePostgres(t *testing.T) {
	f := newDeletionFixture(t)
	userID := f.original.User.ID
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO oauth_links (provider, subject, user_id) VALUES ('google', 'sub-1', $1)`, userID); err != nil {
		t.Fatal(err)
	}

	expectDeletionError(t, f.complete(deletionOperation), http.StatusNotFound, "DELETION_NOT_FOUND", "operation_id", deletionOperation)
	if n := f.count(t, `SELECT count(*) FROM users WHERE status = 'ACTIVE'`); n != 1 {
		t.Fatal("Complete before Begin must not change the account")
	}

	expectReceipt(t, f.begin(deletionOperation, userID), http.StatusCreated, userID, "BEGUN")
	done := expectReceipt(t, f.complete(deletionOperation), http.StatusOK, userID, "COMPLETED")
	for table, query := range map[string]string{
		"users":          `SELECT count(*) FROM users`,
		"sessions":       `SELECT count(*) FROM sessions`,
		"refresh_tokens": `SELECT count(*) FROM refresh_tokens`,
		"oauth_links":    `SELECT count(*) FROM oauth_links`,
	} {
		if n := f.count(t, query); n != 0 {
			t.Fatalf("%s rows after Complete = %d, want 0", table, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM session_revocations WHERE user_id = $1`, userID); n != 1 {
		t.Fatalf("queued revocations after Complete = %d, want 1", n)
	}

	again := expectReceipt(t, f.complete(deletionOperation), http.StatusOK, userID, "COMPLETED")
	retried := expectReceipt(t, f.begin(deletionOperation, userID), http.StatusOK, userID, "COMPLETED")
	if again["completed_at"] != done["completed_at"] || retried["completed_at"] != done["completed_at"] {
		t.Fatal("retries must return the stored completion time")
	}
	expectDeletionError(t, f.begin(otherOperation, userID), http.StatusConflict, "DELETION_CONFLICT", "user_id", float64(userID))
	missing := internalCall(f.internal, "/internal/v1/accounts/"+strconv.FormatInt(userID, 10), userCredential, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("metadata after Complete = %d, want 404", missing.Code)
	}

	again2, err := f.auth.Register(f.ctx, testutil.ValidInput())
	if err != nil {
		t.Fatalf("re-registration after deletion = %v", err)
	}
	if again2.User.ID <= userID {
		t.Fatalf("new user_id = %d, must be greater than deleted %d", again2.User.ID, userID)
	}
}

// TestAccountDeletionProvisioningPostgres checks Begin on a PROVISIONING account.
//
// Expected output: Begin succeeds, the account becomes DELETING, and pending
// provisioning can no longer activate it.
func TestAccountDeletionProvisioningPostgres(t *testing.T) {
	f := newDeletionFixture(t)
	userID := f.original.User.ID
	operation := "9e4a6c13-2b7f-4d8e-a105-6f3c8b2d7e94"
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET status = 'PROVISIONING'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO provisioning_operations (user_id, operation_id, username, account_created_at)
		SELECT id, $1, username, created_at FROM users`, operation); err != nil {
		t.Fatal(err)
	}

	expectReceipt(t, f.begin(deletionOperation, userID), http.StatusCreated, userID, "BEGUN")
	repo := repository.NewAuth(f.pool)
	if _, err := repo.ActivateProvisionedAccount(f.ctx, userID, operation); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("activation after Begin = %v, want ErrNotFound", err)
	}
	if n := f.count(t, `SELECT count(*) FROM users WHERE status = 'DELETING'`); n != 1 {
		t.Fatal("provisioning must not resurrect a deleting account")
	}
	if n := f.count(t, `SELECT count(*) FROM provisioning_operations WHERE state = 'PENDING'`); n != 1 {
		t.Fatal("a refused activation must not complete the provisioning operation")
	}
}

// TestAccountDeletionValidationPostgres checks request validation and authorization.
//
// Expected output:
//
//   - Invalid operation_id and user_id values get 422; malformed bodies, unknown
//     or missing fields and a Complete body get 400; none writes anything.
//   - Missing credentials get 401, other callers 403, and the public listener
//     does not serve the routes.
func TestAccountDeletionValidationPostgres(t *testing.T) {
	f := newDeletionFixture(t)
	path := "/internal/v1/account-deletions/" + deletionOperation
	id := strconv.FormatInt(f.original.User.ID, 10)

	for _, tt := range []struct {
		name, path, body string
		status           int
		field            string
	}{
		{"operation_id", "/internal/v1/account-deletions/not-a-uuid", `{"user_id":` + id + `}`, http.StatusUnprocessableEntity, "operation_id"},
		{"complete operation_id", "/internal/v1/account-deletions/not-a-uuid/completion", "", http.StatusUnprocessableEntity, "operation_id"},
		{"zero user_id", path, `{"user_id":0}`, http.StatusUnprocessableEntity, "user_id"},
		{"string user_id", path, `{"user_id":"` + id + `"}`, http.StatusUnprocessableEntity, "user_id"},
		{"fractional user_id", path, `{"user_id":1.5}`, http.StatusUnprocessableEntity, "user_id"},
		{"null user_id", path, `{"user_id":null}`, http.StatusUnprocessableEntity, "user_id"},
		{"missing body", path, "", http.StatusBadRequest, "body"},
		{"missing field", path, `{}`, http.StatusBadRequest, "body"},
		{"unknown field", path, `{"user_id":` + id + `,"email":"a@b.c"}`, http.StatusBadRequest, "body"},
		{"array", path, `[` + id + `]`, http.StatusBadRequest, "body"},
		{"two values", path, `{"user_id":` + id + `}{"user_id":` + id + `}`, http.StatusBadRequest, "body"},
		{"malformed", path, `{"user_id":`, http.StatusBadRequest, "body"},
		{"complete body", path + "/completion", `{}`, http.StatusBadRequest, "body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := deletionCall(f.internal, tt.path, tt.body, userCredential)
			expectDeletionError(t, response, tt.status, "VALIDATION_ERROR", "field", tt.field)
		})
	}
	if n := f.count(t, `SELECT count(*) FROM account_deletions`); n != 0 {
		t.Fatalf("invalid requests wrote %d deletion records", n)
	}

	body := `{"user_id":` + id + `}`
	if response := deletionCall(f.internal, path, body, ""); response.Code != http.StatusUnauthorized ||
		response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("no credential = %d %s", response.Code, response.Body)
	}
	for _, target := range []string{path, path + "/completion"} {
		requestBody := body
		if strings.HasSuffix(target, "/completion") {
			requestBody = ""
		}
		if response := deletionCall(f.internal, target, requestBody, gatewayCredential); response.Code != http.StatusForbidden {
			t.Fatalf("gateway on %s = %d, want 403", target, response.Code)
		}
		if response := deletionCall(f.public, target, requestBody, userCredential); response.Code != http.StatusNotFound {
			t.Fatalf("public listener on %s = %d, want 404", target, response.Code)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM users WHERE status = 'ACTIVE'`); n != 1 {
		t.Fatal("rejected requests must not change the account")
	}
}

// TestAccountDeletionConcurrencyPostgres checks races on a real database.
//
// Expected output:
//
//   - Concurrent identical Begins record one operation: one 201, the rest 200.
//   - Logins racing with Begin never leave an open session on the account.
//   - Concurrent Completes all return 200 and remove the account once.
func TestAccountDeletionConcurrencyPostgres(t *testing.T) {
	f := newDeletionFixture(t)
	userID := f.original.User.ID
	input := testutil.ValidInput()

	const workers = 8
	statuses := make(chan int, workers)
	loginErrors := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			statuses <- f.begin(deletionOperation, userID).Code
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := f.auth.Login(f.ctx, service.LoginInput{Email: input.Email, Password: input.Password})
			if err != nil && !errors.Is(err, service.ErrInvalidCredentials) {
				loginErrors <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(loginErrors)
	for err := range loginErrors {
		t.Fatalf("racing login = %v", err)
	}
	created := 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("concurrent Begin status = %d", status)
		}
	}
	if created != 1 {
		t.Fatalf("created Begins = %d, want 1", created)
	}
	if n := f.count(t, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`); n != 0 {
		t.Fatalf("open sessions after racing logins = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM refresh_tokens WHERE revoked_at IS NULL`); n != 0 {
		t.Fatalf("open refresh tokens after racing logins = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sessions s LEFT JOIN session_revocations r ON r.session_id = s.id WHERE r.session_id IS NULL`); n != 0 {
		t.Fatalf("revoked sessions without a queued notification = %d", n)
	}

	completions := make(chan int, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			completions <- f.complete(deletionOperation).Code
		}()
	}
	wg.Wait()
	close(completions)
	for status := range completions {
		if status != http.StatusOK {
			t.Fatalf("concurrent Complete status = %d", status)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM users`); n != 0 {
		t.Fatalf("accounts after Complete = %d, want 0", n)
	}
}
