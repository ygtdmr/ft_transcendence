package integration_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// TestLoginBcryptLimitsPostgres checks the 72-byte bcrypt input boundary.
//
// Expected output:
//
//   - A registered 72-byte password logs in.
//   - The same password with one more byte is rejected without a session.
func TestLoginBcryptLimitsPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	auth := service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t))
	ctx := context.Background()
	input := testutil.ValidInput()
	input.Password = strings.Repeat("p", 72)
	if _, err := auth.Register(ctx, input); err != nil {
		t.Fatal(err)
	}

	if _, err := auth.Login(ctx, service.LoginInput{Email: input.Email, Password: input.Password}); err != nil {
		t.Fatalf("72-byte login = %v", err)
	}
	over := service.LoginInput{Email: input.Email, Password: input.Password + "p"}
	if _, err := auth.Login(ctx, over); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("73-byte login = %v, want ErrInvalidCredentials", err)
	}
	if _, _, sessions := countRegistrationRows(t, pool); sessions != 2 {
		t.Fatalf("sessions = %d, want 2", sessions)
	}
}

// TestLoginRefusesPasswordlessAccountPostgres checks a Google-only account.
//
// Expected output:
//
//   - The account has no password hash, so any password is rejected without a session.
func TestLoginRefusesPasswordlessAccountPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	auth := service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t))
	ctx := context.Background()
	input := testutil.ValidInput()
	if _, err := pool.Exec(ctx, `INSERT INTO users (email, username, password_hash, status)
		VALUES ($1, $2, NULL, 'ACTIVE')`, input.Email, input.Username); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(ctx, service.LoginInput{Email: input.Email, Password: input.Password}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("Login() = %v, want ErrInvalidCredentials", err)
	}
	if _, _, sessions := countRegistrationRows(t, pool); sessions != 0 {
		t.Fatalf("sessions = %d, want 0", sessions)
	}
}

// TestLoginWrongPasswordDoesNotResumeProvisioningPostgres checks an incomplete account.
//
// Expected output:
//
//   - A wrong password is rejected and sends nothing to User Service.
//   - The account stays PROVISIONING with no session.
func TestLoginWrongPasswordDoesNotResumeProvisioningPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	auth := provisioningAuth(t, pool, stub)
	ctx := context.Background()
	input := testutil.ValidInput()
	stub.answer(stubAnswer{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"})
	if _, err := auth.Register(ctx, input); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("Register() = %v", err)
	}
	calls := len(stub.sent())

	if _, err := auth.Login(ctx, service.LoginInput{Email: input.Email, Password: "wrong-password"}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("Login() = %v, want ErrInvalidCredentials", err)
	}
	state, _ := readAccount(t, pool)
	if len(stub.sent()) != calls || state.status != "PROVISIONING" || state.sessions != 0 {
		t.Fatalf("calls = %d, state = %+v", len(stub.sent())-calls, state)
	}
}

// TestLoginExposesNoSecretsPostgres inspects responses and logs of real logins.
//
// Expected output:
//
//   - Success sets only the two credential cookies.
//   - Neither the JSON bodies nor the logs hold the password, its hash or a token.
func TestLoginExposesNoSecretsPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	auth := service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t))
	ctx := context.Background()
	input := testutil.ValidInput()
	if _, err := auth.Register(ctx, input); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM users").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler := server.New("0", controller.NewAuth(auth, controller.WithSessions(auth)), testLogger(t, "json", "debug", &logs)).Handler

	success := requestAuth(handler, http.MethodPost, "/login", loginJSON(input.Email, input.Password))
	if success.Code != http.StatusOK {
		t.Fatalf("login = %d %s", success.Code, success.Body)
	}
	secrets := []string{input.Password, hash}
	cookies := credentialCookies(t, success)
	if len(cookies) != 2 || cookies["ft_session"] == nil || cookies["ft_refresh"] == nil {
		t.Fatalf("cookies = %v", cookies)
	}
	secrets = append(secrets, cookies["ft_session"].Value, cookies["ft_refresh"].Value)

	failure := requestAuth(handler, http.MethodPost, "/login", loginJSON(input.Email, "wrong-password"))
	checkAuthError(t, failure, http.StatusUnauthorized, "INVALID_CREDENTIALS")
	secrets = append(secrets, "wrong-password")
	if len(failure.Result().Cookies()) != 0 {
		t.Fatal("a rejected login set cookies")
	}
	for _, secret := range secrets {
		for _, text := range []string{success.Body.String(), failure.Body.String(), logs.String()} {
			if strings.Contains(text, secret) {
				t.Fatal("a credential reached a response body or a log")
			}
		}
	}
	if logs.Len() == 0 {
		t.Fatal("no log records were captured")
	}
}
