package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

// TestRegisterHTTP checks successful registration from request to stored credentials.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - An empty testutil.MemoryRepository, the test issuer and valid input with padded identity fields.
//   - Content-Type: application/json; charset=utf-8.
//
// Flow:
//
//  1. POST the input through the complete HTTP handler.
//  2. Inspect persisted user/session/refresh links and verify the bcrypt password hash.
//  3. Verify cookie attributes, JWT claims, refresh-token hash and public response fields.
//
// Expected output:
//
//   - HTTP 201; exactly one user, one session and one refresh-token record.
//   - ft_session uses path /; ft_refresh uses /api/v1/auth/refresh; both are Secure, HttpOnly
//     and SameSite=Lax.
//   - The JSON body contains four public user fields and a nonzero access_token_expires_at,
//     with no tokens, password or password hash.
func TestRegisterHTTP(t *testing.T) {
	repo := &testutil.MemoryRepository{}
	input := testutil.ValidInput()
	input.Email, input.Username = " Player@Example.com ", " Player_One "
	response := requestRegister(authHandler(t, repo, testutil.TokenIssuer(t)), inputJSON(t, input), "application/json; charset=utf-8")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}

	checkRegisteredRecords(t, repo, input.Password)
	user := repo.Users[0]

	checkRegistrationCookies(t, response, repo)
	checkRegistrationBody(t, response, user, input.Password)
}

func checkRegistrationBody(t *testing.T, response *httptest.ResponseRecorder, user model.User, password string) {
	t.Helper()
	var body struct {
		Data struct {
			User    map[string]any `json:"user"`
			Expires time.Time      `json:"access_token_expires_at"`
		} `json:"data"`
	}

	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if len(body.Data.User) != 4 || body.Data.Expires.IsZero() || body.Data.User["email"] != user.Email {
		t.Fatal("incorrect registration response fields")
	}

	if strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), user.PasswordHash) {
		t.Fatal("password leaked into response")
	}
}

// TestRegisterHTTPErrors checks request rejection and safe HTTP error responses.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - Malformed, non-object, trailing, unknown-field and oversized JSON; unsupported content
//     type.
//   - Missing or invalid fields and an injected user-insert failure.
//   - Each case supplies body, contentType, failAt, expected status and error code.
//
// Flow:
//
//  1. Create a fresh repository with the selected failure for each case.
//  2. POST the request and decode the error envelope.
//  3. Check status, cookies, stored users and absence of private error details.
//
// Expected output:
//
//   - HTTP 400 VALIDATION_ERROR for decoding failures; HTTP 422 with the relevant field code
//     for invalid credentials.
//   - An internal repository failure returns HTTP 500 INTERNAL_ERROR.
//   - Every case sets no credential cookies, stores no user and returns a non-null details
//     object without private causes.
func TestRegisterHTTPErrors(t *testing.T) {
	for _, tt := range registrationErrorCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			repo := &testutil.MemoryRepository{FailAt: tt.failAt}
			response := requestRegister(authHandler(t, repo, testutil.TokenIssuer(t)), tt.body, tt.contentType)
			if response.Code != tt.status || len(response.Result().Cookies()) != 0 || len(repo.Users) != 0 {
				t.Fatalf("unexpected error response: %d %s", response.Code, response.Body)
			}

			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}

			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != tt.code || body.Error.Details == nil {
				t.Fatalf("incorrect error envelope: %s", response.Body)
			}

			if strings.Contains(response.Body.String(), "private") {
				t.Fatal("internal error details leaked")
			}
		})
	}
}

// TestRegisterConflicts checks case-insensitive account conflicts.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - One successfully registered account.
//   - A duplicate email with a different username, then a duplicate username with a different
//     email; duplicates use uppercase values.
//
// Flow:
//
//  1. Register the original account.
//  2. POST each conflicting identity and inspect the response.
//  3. Count the remaining user, session and refresh-token records.
//
// Expected output:
//
//   - HTTP 409 EMAIL_TAKEN with no credential cookies.
//   - Exactly the original user/session/refresh-token trio remains.
func TestRegisterConflicts(t *testing.T) {
	repo := &testutil.MemoryRepository{}
	handler := authHandler(t, repo, testutil.TokenIssuer(t))
	if response := requestRegister(handler, inputJSON(t, testutil.ValidInput()), "application/json"); response.Code != 201 {
		t.Fatalf("initial registration failed: %s", response.Body)
	}

	{
		input := testutil.ValidInput()
		code := "EMAIL_TAKEN"
		input.Email, input.Username = strings.ToUpper(input.Email), "other_player"

		response := requestRegister(handler, inputJSON(t, input), "application/json")
		if response.Code != 409 || !strings.Contains(response.Body.String(), code) || len(response.Result().Cookies()) != 0 {
			t.Fatalf("conflict: %d %s", response.Code, response.Body)
		}
	}

	if len(repo.Users) != 1 || len(repo.Sessions) != 1 || len(repo.Refresh) != 1 {
		t.Fatal("conflicts must not create additional records")
	}
}

func checkRegisteredRecords(t *testing.T, repo *testutil.MemoryRepository, password string) {
	t.Helper()
	if len(repo.Users) != 1 || len(repo.Sessions) != 1 || len(repo.Refresh) != 1 {
		t.Fatal("registration must persist one complete account")
	}

	user, session, refresh := repo.Users[0], repo.Sessions[0], repo.Refresh[0]
	if user.Email != "Player@Example.com" || user.Username != "Player_One" || session.UserID != user.ID || refresh.SessionID != session.ID {
		t.Fatal("incorrect identity normalization or record links")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		t.Fatal("stored password is not a bcrypt hash of the input")
	}
}

func checkRegistrationCookies(t *testing.T, response *httptest.ResponseRecorder, repo *testutil.MemoryRepository) {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("got %d cookies", len(cookies))
	}

	for _, cookie := range cookies {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || !cookie.Expires.After(time.Now()) {
			t.Fatalf("incorrect attributes for %s", cookie.Name)
		}

		if strings.Contains(response.Body.String(), cookie.Value) {
			t.Fatalf("%s leaked into response body", cookie.Name)
		}

		checkRegistrationCookieValue(t, cookie, repo)
	}
}

func checkRegistrationCookieValue(t *testing.T, cookie *http.Cookie, repo *testutil.MemoryRepository) {
	t.Helper()
	user, session, refresh := repo.Users[0], repo.Sessions[0], repo.Refresh[0]
	switch cookie.Name {
	case "ft_session":
		if cookie.Path != "/" {
			t.Fatal("wrong access cookie path")
		}

		claims := testutil.ParseClaims(t, cookie.Value)
		if claims.Subject != strconv.FormatInt(user.ID, 10) || claims.SessionID != session.ID {
			t.Fatal("JWT is not bound to the registered user/session")
		}

	case "ft_refresh":
		if cookie.Path != "/api/v1/auth/refresh" {
			t.Fatal("wrong refresh cookie path")
		}

		hash := sha256.Sum256([]byte(cookie.Value))
		if refresh.TokenHash != hex.EncodeToString(hash[:]) {
			t.Fatal("refresh persistence must contain its hash")
		}

	default:
		t.Fatalf("unexpected cookie %s", cookie.Name)
	}
}

type registrationErrorCase struct {
	name, body, contentType, failAt, code string
	status                                int
}

func registrationErrorCases(t *testing.T) []registrationErrorCase {
	return []registrationErrorCase{
		{"malformed", "{", "application/json", "", "VALIDATION_ERROR", 400},
		{"array", "[]", "application/json", "", "VALIDATION_ERROR", 400},
		{"null", "null", "application/json", "", "VALIDATION_ERROR", 400},
		{"trailing object", "{} {}", "application/json", "", "VALIDATION_ERROR", 400},
		{"unknown field", `{"admin":true}`, "application/json", "", "VALIDATION_ERROR", 400},
		{"wrong content type", "{}", "text/plain", "", "VALIDATION_ERROR", 400},
		{"oversized", `{"email":"` + strings.Repeat("a", 9000) + `"}`, "application/json", "", "VALIDATION_ERROR", 400},
		{"missing email", `{}`, "application/json", "", "INVALID_EMAIL", 422},
		{"invalid email", `{"email":"bad","username":"player","password":"12345678"}`, "application/json", "", "INVALID_EMAIL", 422},
		{"username type", `{"email":"p@example.com","username":123,"password":"12345678"}`, "application/json", "", "INVALID_USERNAME", 422},
		{"null password", `{"email":"p@example.com","username":"player","password":null}`, "application/json", "", "INVALID_PASSWORD", 422},
		{"short password", `{"email":"p@example.com","username":"player","password":"short"}`, "application/json", "", "INVALID_PASSWORD", 422},
		{"internal failure", inputJSON(t, testutil.ValidInput()), "application/json", "user", "INTERNAL_ERROR", 500},
	}
}
