package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

// TestRegisterPostgres checks registration against real PostgreSQL transactions.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - AUTH_TEST_DATABASE_URL pointing to PostgreSQL with permission to create/drop schemas.
//   - The auth migration, valid registration input, injected issuer/session/refresh failures
//     and duplicate identities.
//
// Flow:
//
//  1. Skip if the database URL is unset; otherwise create an isolated schema and apply the
//     migration.
//  2. Inject each failure and verify rollback leaves all three tables empty.
//  3. Register through HTTP and verify row counts, bcrypt and the refresh-cookie hash.
//  4. Exercise case-insensitive constraints, then concurrently register the same email twice.
//  5. Close pools and drop the isolated schema during cleanup.
//
// Expected output:
//
//   - Rollback cases leave zero rows; successful HTTP registration returns 201 and one row per
//     table.
//   - Duplicate identities return 409 EMAIL_TAKEN or USERNAME_TAKEN without adding rows.
//   - Concurrent requests produce one success and one ErrEmailTaken, ending with two rows per
//     table.
func TestRegisterPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	repo := repository.NewAuth(pool)
	issuer := testutil.TokenIssuer(t)
	checkPostgresRollbacks(t, pool, repo, issuer)
	handler := authHandler(t, repo, issuer)
	checkPostgresRegistration(t, pool, handler)
	checkPostgresConflicts(t, pool, handler)
	checkConcurrentRegistration(t, pool, service.NewAuth(repo, issuer))
}

// registrationTestPool scopes all test records to a disposable schema.
func registrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AUTH_TEST_DATABASE_URL to run isolated PostgreSQL integration tests")
	}
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := createRegistrationTestSchema(t, admin)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	applyRegistrationMigration(t, pool)
	return pool
}

func createRegistrationTestSchema(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	schema := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	return schema
}

func applyRegistrationMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, name := range []string{"000001_create_auth_tables.up.sql", "000002_session_revocations.up.sql", "000003_account_status.up.sql",
		"000004_provisioning_operations.up.sql", "000005_oauth_states.up.sql", "000006_google_links.up.sql",
		"000007_account_deletions.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(migration)); err != nil {
			t.Fatal(err)
		}
	}
}

func checkPostgresRollbacks(t *testing.T, pool *pgxpool.Pool, repo service.AuthRepository, issuer *security.TokenIssuer) {
	t.Helper()
	for _, stage := range []string{"issuer", "session", "refresh"} {
		t.Run("rollback "+stage, func(t *testing.T) {
			failing := failingPostgresIssuer(issuer, stage)
			if _, err := service.NewAuth(repo, failing).Register(context.Background(), testutil.ValidInput()); err == nil {
				t.Fatal("expected a transaction failure")
			}
			assertCounts(t, pool, 0)
		})
	}
}

func failingPostgresIssuer(issuer *security.TokenIssuer, stage string) testutil.IssuerFunc {
	return func(id int64) (security.Credentials, error) {
		if stage == "issuer" {
			return security.Credentials{}, errors.New("signing failed")
		}
		credentials, err := issuer.Issue(id)
		if stage == "session" {
			credentials.Session.ID = "invalid-uuid"
		} else {
			credentials.Refresh.ExpiresAt = credentials.Refresh.CreatedAt
		}
		return credentials, err
	}
}

func checkPostgresRegistration(t *testing.T, pool *pgxpool.Pool, handler http.Handler) {
	t.Helper()
	response := requestRegister(handler, inputJSON(t, testutil.ValidInput()), "application/json")
	if response.Code != 201 {
		t.Fatalf("registration: %d %s", response.Code, response.Body)
	}
	assertCounts(t, pool, 1)
	var storedHash, passwordHash string
	if err := pool.QueryRow(context.Background(), "SELECT token_hash FROM refresh_tokens").Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT password_hash FROM users").Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(testutil.ValidInput().Password)); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "ft_refresh" {
			hash := sha256.Sum256([]byte(cookie.Value))
			if storedHash != hex.EncodeToString(hash[:]) {
				t.Fatal("database contains an incorrect refresh hash")
			}
		}
	}
}

func checkPostgresConflicts(t *testing.T, pool *pgxpool.Pool, handler http.Handler) {
	t.Helper()
	for _, field := range []string{"email", "username"} {
		input := testutil.ValidInput()
		code := "EMAIL_TAKEN"
		if field == "email" {
			input.Email, input.Username = strings.ToUpper(input.Email), "other_player"
		} else {
			input.Email, input.Username = "other@example.com", strings.ToUpper(input.Username)
			code = "USERNAME_TAKEN"
		}
		response := requestRegister(handler, inputJSON(t, input), "application/json")
		if response.Code != 409 || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("constraint mapping: %d %s", response.Code, response.Body)
		}
		assertCounts(t, pool, 1)
	}
}

func checkConcurrentRegistration(t *testing.T, pool *pgxpool.Pool, authService *service.AuthService) {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := testutil.ValidInput()
			input.Email, input.Username = "concurrent@example.com", fmt.Sprintf("concurrent_%d", n)
			_, err := authService.Register(context.Background(), input)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	checkConcurrentResults(t, results)
	assertCounts(t, pool, 2)
}

func checkConcurrentResults(t *testing.T, results <-chan error) {
	t.Helper()
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, service.ErrEmailTaken) {
			conflict++
		} else {
			t.Fatalf("concurrent registration: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected one success and one conflict, got %d/%d", success, conflict)
	}
}

// assertCounts checks all auth tables have the same expected row count.
//
// Parameters:
//
//   - t: Go test runner used to report query/count failures.
//   - pool: PostgreSQL pool scoped to the integration-test schema.
//   - expected: Required number of rows in each table.
//
// Flow:
//
//  1. Query count(*) from users, sessions and refresh_tokens using sanitized identifiers.
//  2. Fail the test on a query error or count mismatch.
//
// Expected output:
//
//   - No return value; the test continues only if every table count equals expected.
func assertCounts(t *testing.T, pool *pgxpool.Pool, expected int) {
	t.Helper()

	for _, table := range []string{"users", "sessions", "refresh_tokens"} {
		var count int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
			t.Fatal(err)
		}

		if count != expected {
			t.Fatalf("%s count=%d, want %d", table, count, expected)
		}
	}
}
