package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// TestCanonicalUsernameAfterRenamePostgres follows a username change made in User Service.
//
// Expected output:
//
//   - Login and current-session return the name User Service holds now.
//   - An outage is ErrServiceUnavailable: no stored name and no new session.
//   - Another account can register the name given up by the rename.
func TestCanonicalUsernameAfterRenamePostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	client, err := userclient.New(stub.server.URL, provisioningCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	auth := service.NewAuth(repository.NewAuth(pool), testutil.TokenIssuer(t),
		service.WithProfileProvisioner(client), service.WithProfileReader(client))
	ctx := context.Background()
	input := testutil.ValidInput()

	registered, err := auth.Register(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	login := service.LoginInput{Email: input.Email, Password: input.Password}
	if _, err := auth.Login(ctx, login); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("Login() during outage = %v, want ErrServiceUnavailable", err)
	}
	var sessions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("sessions after failed read = %d, %v", sessions, err)
	}

	stub.setProfile(registered.User.ID, "renamed_one")
	result, err := auth.Login(ctx, login)
	if err != nil || result.User.Username != "renamed_one" {
		t.Fatalf("Login() = %+v, %v", result.User, err)
	}
	identity, err := auth.Authenticate(ctx, result.Credentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	current, err := auth.CurrentSession(ctx, identity)
	if err != nil || current.User.Username != "renamed_one" {
		t.Fatalf("CurrentSession() = %+v, %v", current.User, err)
	}

	second := testutil.ValidInput()
	second.Email = "second@example.com"
	if _, err := auth.Register(ctx, second); err != nil {
		t.Fatalf("Register() with the released name = %v", err)
	}
}
