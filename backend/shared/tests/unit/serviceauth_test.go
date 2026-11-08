package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

const (
	credGateway    = "gateway-credential-0123456789abcdef-0123456789"
	credGatewayOld = "gateway-OLD-credential-0123456789abcdef-012"
	credUser       = "user-service-credential-0123456789abcdef-0123"
)

var testMatrix = serviceauth.Matrix{
	"game-gateway": {"session.validate"},
	"user-service": {"account.metadata.read", "account.deletion.begin"},
}

func newAuth() *serviceauth.Authenticator {
	return serviceauth.New(map[string][]string{
		"game-gateway": {credGateway},
		"user-service": {credUser},
		"stranger":     {"stranger-credential-0123456789abcdef-0123"}, // not in the matrix
	}, testMatrix)
}

func call(t *testing.T, a *serviceauth.Authenticator, scope string, headers ...string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var caller string
	h := a.Require(scope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller = serviceauth.Caller(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/x", nil)
	for _, v := range headers {
		req.Header.Add("Authorization", v)
	}
	h.ServeHTTP(rec, req)
	return rec, caller
}

func TestIdentifyAcceptsOnlyBearerWithAKnownCredential(t *testing.T) {
	a := newAuth()
	for header, want := range map[string]string{
		"Bearer " + credGateway: "game-gateway",
		"bearer " + credUser:    "user-service",
		"BEARER " + credGateway: "game-gateway",
	} {
		if caller, ok := a.Identify(header); !ok || caller != want {
			t.Errorf("Identify(%q) = %q, %v", header, caller, ok)
		}
	}
	for _, header := range []string{
		"", "Bearer", "Bearer ", credGateway, "Basic " + credGateway, "Token " + credGateway,
		"Bearer " + credGateway + " extra", "Bearer  " + credGateway, "Bearer\t" + credGateway,
		"Bearer not-a-known-credential", "Bearer " + strings.ToUpper(credGateway),
		"Bearer stranger-credential-0123456789abcdef-0123", // known string, but its caller has no rules
	} {
		if caller, ok := a.Identify(header); ok {
			t.Errorf("Identify(%q) accepted as %q", header, caller)
		}
	}
}

func TestRequireRejectsWith401AndNeverEchoesTheCredential(t *testing.T) {
	a := newAuth()
	const presented = "presented-secret-credential-9f31-aaaaaaaaaaaaaa"
	for name, headers := range map[string][]string{
		"missing":   nil,
		"unknown":   {"Bearer " + presented},
		"malformed": {"Basic " + presented},
		"repeated":  {"Bearer " + credGateway, "Bearer " + credGateway},
	} {
		rec, caller := call(t, a, "session.validate", headers...)
		if rec.Code != 401 || caller != "" || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %d caller=%q www=%q", name, rec.Code, caller, rec.Header().Get("WWW-Authenticate"))
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"code":"SERVICE_UNAUTHENTICATED"`) || !strings.Contains(body, `"details":{}`) || strings.Contains(body, presented) {
			t.Errorf("%s: body = %s", name, body)
		}
		if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
			t.Errorf("%s: %v", name, v)
		}
	}
}

func TestRequireEnforcesTheScopeMatrixWith403(t *testing.T) {
	a := newAuth()
	rec, caller := call(t, a, "session.validate", "Bearer "+credGateway)
	if rec.Code != 204 || caller != "game-gateway" {
		t.Fatalf("allowed call: %d caller=%q", rec.Code, caller)
	}
	// Known caller, scope it does not hold (the gateway may not read account metadata).
	rec, caller = call(t, a, "account.metadata.read", "Bearer "+credGateway)
	if rec.Code != 403 || caller != "" || !strings.Contains(rec.Body.String(), `"code":"SERVICE_FORBIDDEN"`) || !strings.Contains(rec.Body.String(), `"details":{}`) {
		t.Errorf("forbidden call: %d %s caller=%q", rec.Code, rec.Body.String(), caller)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("403 is not an authentication challenge")
	}
	// A scope nobody holds is refused to everyone.
	if rec, _ := call(t, a, "no.such.scope", "Bearer "+credUser); rec.Code != 403 {
		t.Errorf("unknown scope: %d", rec.Code)
	}
	// A caller outside the matrix has no identity even with a configured credential.
	if rec, _ := call(t, a, "session.validate", "Bearer stranger-credential-0123456789abcdef-0123"); rec.Code != 401 {
		t.Errorf("caller outside the matrix: %d, want 401", rec.Code)
	}
}

func TestReceiverFailsClosedWithoutCredentials(t *testing.T) {
	for name, a := range map[string]*serviceauth.Authenticator{
		"nil map":      serviceauth.New(nil, testMatrix),
		"empty map":    serviceauth.New(map[string][]string{}, testMatrix),
		"empty values": serviceauth.New(map[string][]string{"game-gateway": {"", ""}}, testMatrix),
		"zero value":   {},
	} {
		if rec, _ := call(t, a, "session.validate", "Bearer "+credGateway); rec.Code != 401 {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
		if rec, _ := call(t, a, "session.validate", "Bearer "); rec.Code != 401 {
			t.Errorf("%s (empty token): %d, want 401", name, rec.Code)
		}
	}
}

func TestRotationAcceptsCurrentAndPreviousUntilThePreviousIsRemoved(t *testing.T) {
	during := serviceauth.New(map[string][]string{"game-gateway": {credGateway, credGatewayOld}}, testMatrix)
	for _, cred := range []string{credGateway, credGatewayOld} {
		if rec, _ := call(t, during, "session.validate", "Bearer "+cred); rec.Code != 204 {
			t.Errorf("during rotation %q: %d", cred[:10], rec.Code)
		}
	}
	after := serviceauth.New(map[string][]string{"game-gateway": {credGateway}}, testMatrix)
	if rec, _ := call(t, after, "session.validate", "Bearer "+credGatewayOld); rec.Code != 401 {
		t.Errorf("the removed credential must stop working, got %d", rec.Code)
	}
}

func TestCallerIsEmptyOutsideARequestThatPassedRequire(t *testing.T) {
	if serviceauth.Caller(context.Background()) != "" {
		t.Error("no caller without Require")
	}
}

// --- credential loading ---------------------------------------------------

func writeSecret(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(values map[string]string) *config.Reader {
	return config.NewReader(func(name string) (string, bool) { v, ok := values[name]; return v, ok })
}

var loadSpec = map[string]serviceauth.CallerFiles{
	"game-gateway": {Current: "GAME_GATEWAY_CREDENTIAL_FILE", Previous: "GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE"},
	"user-service": {Current: "USER_SERVICE_CREDENTIAL_FILE", Previous: "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
}

func TestLoadCredentialsReadsFilesAndTrimsTheTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	r := envOf(map[string]string{
		"GAME_GATEWAY_CREDENTIAL_FILE":          writeSecret(t, dir, "gw", credGateway+"\n"),
		"GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE": writeSecret(t, dir, "gw-old", credGatewayOld+"\n"),
		// user-service unset: omitted, so refused
	})
	got := serviceauth.LoadCredentials(r, loadSpec)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got["game-gateway"]) != 2 || got["game-gateway"][0] != credGateway || got["game-gateway"][1] != credGatewayOld {
		t.Errorf("gateway credentials = %v", got["game-gateway"])
	}
	if _, present := got["user-service"]; present {
		t.Error("an unconfigured caller must be omitted so the receiver refuses it")
	}
	a := serviceauth.New(got, testMatrix)
	if rec, _ := call(t, a, "account.metadata.read", "Bearer "+credUser); rec.Code != 401 {
		t.Errorf("the unconfigured caller was accepted: %d", rec.Code)
	}
}

func TestLoadCredentialsReportsProblemsByVariableNeverByPathOrValue(t *testing.T) {
	dir := t.TempDir()
	short := writeSecret(t, dir, "short", "too-short")
	tooBig := writeSecret(t, dir, "big", strings.Repeat("x", 5000))
	missing := filepath.Join(dir, "does-not-exist-9f31")
	shared := writeSecret(t, dir, "shared", credGateway)
	sharedCopy := writeSecret(t, dir, "shared-copy", credGateway)
	same := writeSecret(t, dir, "same", credUser)
	sameCopy := writeSecret(t, dir, "same-copy", credUser)

	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"unreadable":                  {map[string]string{"GAME_GATEWAY_CREDENTIAL_FILE": missing}, "GAME_GATEWAY_CREDENTIAL_FILE must name a readable file"},
		"too short":                   {map[string]string{"GAME_GATEWAY_CREDENTIAL_FILE": short}, "GAME_GATEWAY_CREDENTIAL_FILE file must contain at least 32 bytes"},
		"oversized":                   {map[string]string{"GAME_GATEWAY_CREDENTIAL_FILE": tooBig}, "GAME_GATEWAY_CREDENTIAL_FILE must name a readable file"},
		"previous without current":    {map[string]string{"GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE": shared}, "GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE requires GAME_GATEWAY_CREDENTIAL_FILE"},
		"previous equals current":     {map[string]string{"USER_SERVICE_CREDENTIAL_FILE": same, "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE": sameCopy}, "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE must differ"},
		"one credential, two callers": {map[string]string{"GAME_GATEWAY_CREDENTIAL_FILE": shared, "USER_SERVICE_CREDENTIAL_FILE": sharedCopy}, "each private caller needs its own service credential"},
	}
	for name, tc := range cases {
		r := envOf(tc.env)
		serviceauth.LoadCredentials(r, loadSpec)
		err := r.Err()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want substring %q", name, err, tc.want)
			continue
		}
		for _, leak := range []string{dir, "does-not-exist", credGateway, credUser, "too-short"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: the error leaked %q: %v", name, leak, err)
			}
		}
	}
}

func TestSecretFileReaders(t *testing.T) {
	dir := t.TempDir()
	good := writeSecret(t, dir, "good", "  "+credGateway+" \n\n")
	r := envOf(map[string]string{"A": good})
	if s := r.SecretFile("A", 32); !s.IsSet() || s.Reveal() != credGateway || r.Err() != nil {
		t.Errorf("SecretFile: %q %v", s.Reveal(), r.Err())
	}
	r = envOf(nil)
	if s := r.OptionalSecretFile("A", 32); s.IsSet() || r.Err() != nil {
		t.Error("an unset optional secret file is not a problem")
	}
	r = envOf(nil)
	r.SecretFile("A", 32)
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "A is required") {
		t.Errorf("a required secret file that is unset must be a problem: %v", r.Err())
	}
	r = envOf(map[string]string{"A": writeSecret(t, dir, "short", "abc")})
	if s := r.OptionalSecretFile("A", 32); s.IsSet() || r.Err() == nil {
		t.Error("an optional but half-configured secret must still be a problem")
	}
}

var _ = httpresponse.InternalError
