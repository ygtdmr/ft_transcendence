package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const testTimeout = 5 * time.Second

// fakeGoogle returns a scripted identity for each authorization code.
type fakeGoogle struct {
	mu         sync.Mutex
	identities map[string]service.GoogleIdentity
	verifiers  []string
}

func (f *fakeGoogle) Identify(_ context.Context, code, verifier string) (service.GoogleIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifiers = append(f.verifiers, verifier)
	identity, ok := f.identities[code]
	if !ok {
		return service.GoogleIdentity{}, service.ErrGoogleRejected
	}
	return identity, nil
}

type callbackFixture struct {
	pool    *pgxpool.Pool
	handler http.Handler
	google  *fakeGoogle
	user    *userStub
	auth    *service.AuthService
}

func newCallbackFixture(t *testing.T) callbackFixture {
	t.Helper()
	pool := registrationTestPool(t)
	gin.SetMode(gin.TestMode)
	stub := newUserStub(t)
	client, err := userclient.New(stub.server.URL, provisioningCredential, testTimeout)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewAuth(pool)
	auth := service.NewAuth(repo, testutil.TokenIssuer(t), service.WithProfileProvisioner(client))
	sealer, _ := security.NewOAuthStateSealer([]byte(testutil.TestSigningKey))
	google := &fakeGoogle{identities: map[string]service.GoogleIdentity{}}
	oauth := service.NewGoogleOAuth(service.GoogleOAuthConfig{ClientID: "client-123", RedirectURI: googleRedirectURI}, repo, sealer).
		EnableCallback(google, auth)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := server.New("0", controller.NewAuth(auth, controller.WithGoogleOAuth(oauth, logger)), logger).Handler

	return callbackFixture{pool: pool, handler: handler, google: google, user: stub, auth: auth}
}

// authorize starts sign-in and returns the ft_oauth cookie and issued state.
func (f callbackFixture) authorize(t *testing.T, returnTo string) (*http.Cookie, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/oauth/google/authorize?return_to="+url.QueryEscape(returnTo), nil))
	location, _ := url.Parse(recorder.Header().Get("Location"))
	return recorder.Result().Cookies()[0], location.Query().Get("state")
}

// callback sends Google's redirect and returns Location and the cookies set.
func (f callbackFixture) callback(t *testing.T, cookie *http.Cookie, query url.Values) (string, map[string]*http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/callback?"+query.Encode(), nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d %s", recorder.Code, recorder.Body)
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range recorder.Result().Cookies() {
		cookies[c.Name] = c
	}
	if c, ok := cookies["ft_oauth"]; !ok || c.MaxAge >= 0 {
		t.Fatal("callback did not clear ft_oauth")
	}
	return recorder.Header().Get("Location"), cookies
}

// signIn runs authorize and callback for code.
func (f callbackFixture) signIn(t *testing.T, code string) (string, map[string]*http.Cookie) {
	t.Helper()
	cookie, state := f.authorize(t, "/rooms/9")
	return f.callback(t, cookie, url.Values{"state": {state}, "code": {code}})
}

func (f callbackFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func wantSession(t *testing.T, location string, cookies map[string]*http.Cookie) {
	t.Helper()
	if location != "/rooms/9" || cookies["ft_session"] == nil || cookies["ft_session"].Value == "" ||
		cookies["ft_refresh"] == nil || cookies["ft_refresh"].Path != "/api/v1/auth/refresh" ||
		strings.Contains(location, "token") {
		t.Fatalf("location=%q cookies=%v, want a session and return_to", location, cookies)
	}
}

func wantOAuthError(t *testing.T, location string, cookies map[string]*http.Cookie, code string) {
	t.Helper()
	if location != "/?oauth_error="+code || cookies["ft_session"] != nil || cookies["ft_refresh"] != nil {
		t.Fatalf("location=%q cookies=%v, want oauth_error=%s without a session", location, cookies, code)
	}
}

var ada = service.GoogleIdentity{Subject: "sub-ada", Email: "ada@example.com", EmailVerified: true, Name: "Ada Lovelace"}

// TestGoogleCallbackNewAccountPostgres checks first sign-in and repeat sign-in.
//
// Expected output:
//
//   - A new password-less ACTIVE account is linked and provisioned with a
//     generated username; the session cookies are set and the browser goes to return_to.
//   - The PKCE verifier from ft_oauth reaches Google.
//   - A repeat sign-in, even after the Google email changes, reuses the account.
//   - Password login on the Google-only account answers INVALID_CREDENTIALS.
func TestGoogleCallbackNewAccountPostgres(t *testing.T) {
	f := newCallbackFixture(t)
	f.google.identities["code-1"] = ada

	location, cookies := f.signIn(t, "code-1")
	wantSession(t, location, cookies)
	var username, status string
	var hasPassword bool
	if err := f.pool.QueryRow(context.Background(), `SELECT username, status, password_hash IS NOT NULL FROM users`).
		Scan(&username, &status, &hasPassword); err != nil || username != "Ada_Lovelace" || status != "ACTIVE" || hasPassword {
		t.Fatalf("account = %q %q password=%t, %v", username, status, hasPassword, err)
	}
	if f.count(t, `SELECT count(*) FROM oauth_links WHERE subject = 'sub-ada'`) != 1 || len(f.user.sent()) != 1 {
		t.Fatal("link or provisioning missing")
	}
	if len(f.google.verifiers) != 1 || len(f.google.verifiers[0]) != 43 {
		t.Fatalf("verifiers = %v", f.google.verifiers)
	}

	changed := ada
	changed.Email = "ada.new@example.com"
	f.google.identities["code-2"] = changed
	location, cookies = f.signIn(t, "code-2")
	wantSession(t, location, cookies)
	if f.count(t, `SELECT count(*) FROM users`) != 1 || f.count(t, `SELECT count(*) FROM sessions`) != 2 {
		t.Fatal("repeat sign-in created another account or no session")
	}
	if _, err := f.auth.Login(context.Background(), service.LoginInput{Email: "ada@example.com", Password: "Correct-Horse-1"}); err != service.ErrInvalidCredentials {
		t.Fatalf("password login on Google-only account = %v", err)
	}
}

// TestGoogleCallbackLinksExistingAccountPostgres checks email-based linking.
//
// Expected output:
//
//   - A verified Google email matching a password account (any case) links it
//     and signs in without a new account; the password keeps working.
//   - An account already linked to another Google identity is not taken over.
func TestGoogleCallbackLinksExistingAccountPostgres(t *testing.T) {
	f := newCallbackFixture(t)
	input := testutil.ValidInput()
	if _, err := f.auth.Register(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	f.google.identities["code-1"] = service.GoogleIdentity{Subject: "sub-1", Email: strings.ToUpper(input.Email), EmailVerified: true, Name: "Player"}
	location, cookies := f.signIn(t, "code-1")
	wantSession(t, location, cookies)
	if f.count(t, `SELECT count(*) FROM users`) != 1 || f.count(t, `SELECT count(*) FROM oauth_links`) != 1 {
		t.Fatal("linking created another account")
	}
	if _, err := f.auth.Login(context.Background(), service.LoginInput{Email: input.Email, Password: input.Password}); err != nil {
		t.Fatalf("password login after linking = %v", err)
	}

	f.google.identities["code-2"] = service.GoogleIdentity{Subject: "sub-2", Email: input.Email, EmailVerified: true, Name: "Player"}
	location, cookies = f.signIn(t, "code-2")
	wantOAuthError(t, location, cookies, "OAUTH_PROVIDER_ERROR")
	if f.count(t, `SELECT count(*) FROM oauth_links`) != 1 {
		t.Fatal("a second Google identity took over the account")
	}
}

// TestGoogleCallbackFailuresPostgres checks every documented failure redirect.
//
// Expected output:
//
//   - Missing or mismatched state, a missing cookie and a reused state are
//     OAUTH_STATE_INVALID; access_denied is OAUTH_CANCELLED; an unverified
//     email is OAUTH_EMAIL_UNVERIFIED; a refused code is OAUTH_PROVIDER_ERROR.
//   - None of them creates an account or a session.
func TestGoogleCallbackFailuresPostgres(t *testing.T) {
	f := newCallbackFixture(t)
	f.google.identities["unverified"] = service.GoogleIdentity{Subject: "sub-u", Email: "u@example.com", Name: "U"}
	f.google.identities["good"] = ada

	cookie, state := f.authorize(t, "/rooms/9")
	location, cookies := f.callback(t, cookie, url.Values{"state": {"forged"}, "code": {"good"}})
	wantOAuthError(t, location, cookies, "OAUTH_STATE_INVALID")
	location, cookies = f.callback(t, nil, url.Values{"state": {state}, "code": {"good"}})
	wantOAuthError(t, location, cookies, "OAUTH_STATE_INVALID")

	location, cookies = f.callback(t, cookie, url.Values{"state": {state}, "error": {"access_denied"}})
	wantOAuthError(t, location, cookies, "OAUTH_CANCELLED")
	location, cookies = f.callback(t, cookie, url.Values{"state": {state}, "code": {"good"}})
	wantOAuthError(t, location, cookies, "OAUTH_STATE_INVALID")

	cookie, state = f.authorize(t, "/rooms/9")
	location, cookies = f.callback(t, cookie, url.Values{"state": {state}, "code": {"unverified"}})
	wantOAuthError(t, location, cookies, "OAUTH_EMAIL_UNVERIFIED")
	cookie, state = f.authorize(t, "/rooms/9")
	location, cookies = f.callback(t, cookie, url.Values{"state": {state}, "code": {"refused"}})
	wantOAuthError(t, location, cookies, "OAUTH_PROVIDER_ERROR")

	if f.count(t, `SELECT count(*) FROM users`) != 0 || f.count(t, `SELECT count(*) FROM sessions`) != 0 {
		t.Fatal("a failed callback created an account or session")
	}
}

// TestGoogleCallbackUsernamesAndLifecyclePostgres checks username collisions,
// User outages and deleting accounts.
//
// Expected output:
//
//   - A username taken at User Service moves to the next suffix.
//   - A User outage redirects with OAUTH_PROVIDER_ERROR and leaves the account
//     PROVISIONING; the next sign-in resumes the same operation.
//   - A DELETING account never receives a session.
func TestGoogleCallbackUsernamesAndLifecyclePostgres(t *testing.T) {
	f := newCallbackFixture(t)
	f.user.answer(stubAnswer{http.StatusConflict, "USERNAME_TAKEN"})
	f.google.identities["code-1"] = ada
	location, cookies := f.signIn(t, "code-1")
	wantSession(t, location, cookies)
	var username string
	_ = f.pool.QueryRow(context.Background(), `SELECT u.username FROM users u JOIN oauth_links l ON l.user_id = u.id`).Scan(&username)
	if username != "Ada_Lovelace_2" {
		t.Fatalf("username = %q, want Ada_Lovelace_2 (User took _1)", username)
	}

	grace := service.GoogleIdentity{Subject: "sub-grace", Email: "grace@example.com", EmailVerified: true, Name: "Grace Hopper"}
	f.google.identities["grace"] = grace
	f.user.answer(stubAnswer{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"})
	location, cookies = f.signIn(t, "grace")
	wantOAuthError(t, location, cookies, "OAUTH_PROVIDER_ERROR")
	var status, operation string
	_ = f.pool.QueryRow(context.Background(), `SELECT u.status, p.operation_id::text FROM users u
		JOIN provisioning_operations p ON p.user_id = u.id WHERE u.email = 'grace@example.com'`).Scan(&status, &operation)
	if status != "PROVISIONING" {
		t.Fatalf("status after outage = %q", status)
	}
	location, cookies = f.signIn(t, "grace")
	wantSession(t, location, cookies)
	sent := f.user.sent()
	if sent[len(sent)-1] != operation || sent[len(sent)-2] != operation {
		t.Fatal("resume did not resend the original operation")
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE users SET status = 'DELETING' WHERE email = 'grace@example.com'`); err != nil {
		t.Fatal(err)
	}
	location, cookies = f.signIn(t, "grace")
	wantOAuthError(t, location, cookies, "OAUTH_PROVIDER_ERROR")
}

// TestGoogleCallbackConcurrentFirstSignInPostgres checks racing first sign-ins.
//
// Expected output:
//
//   - Eight callbacks for one new Google identity all sign in to one account
//     with one link and one username.
func TestGoogleCallbackConcurrentFirstSignInPostgres(t *testing.T) {
	f := newCallbackFixture(t)
	f.google.identities["code"] = ada
	const callers = 8
	type started struct {
		cookie *http.Cookie
		state  string
	}
	starts := make([]started, callers)
	for i := range starts {
		starts[i].cookie, starts[i].state = f.authorize(t, "/rooms/9")
	}
	locations := make([]string, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/callback?"+
				url.Values{"state": {starts[i].state}, "code": {"code"}}.Encode(), nil)
			request.AddCookie(starts[i].cookie)
			recorder := httptest.NewRecorder()
			f.handler.ServeHTTP(recorder, request)
			locations[i] = recorder.Header().Get("Location")
		}()
	}
	wg.Wait()
	for _, location := range locations {
		if location != "/rooms/9" {
			t.Fatalf("locations = %v", locations)
		}
	}
	if f.count(t, `SELECT count(*) FROM users`) != 1 || f.count(t, `SELECT count(*) FROM oauth_links`) != 1 {
		t.Fatal("concurrent first sign-ins created more than one account or link")
	}
}
