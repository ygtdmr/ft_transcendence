package unit_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/googleclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const testGoogleClientID = "client-123.apps.googleusercontent.com"

type googleStub struct {
	key        *rsa.PrivateKey
	idToken    string
	tokenCode  int
	keysCode   int
	lastForm   map[string]string
	keyFetches int
}

func newGoogleStub(t *testing.T) (*googleStub, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	stub := &googleStub{key: key, tokenCode: http.StatusOK, keysCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.lastForm = map[string]string{}
		for name := range r.PostForm {
			stub.lastForm[name] = r.PostForm.Get(name)
		}
		w.WriteHeader(stub.tokenCode)
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": stub.idToken, "access_token": "ya29.secret"})
	})
	mux.HandleFunc("/certs", func(w http.ResponseWriter, r *http.Request) {
		stub.keyFetches++
		w.WriteHeader(stub.keysCode)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "key-1", "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return stub, server
}

func (s *googleStub) sign(t *testing.T, claims jwt.MapClaims, kid string, method jwt.SigningMethod, key any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func validGoogleClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": testGoogleClientID, "sub": "1081234567890",
		"email": "ada@example.com", "email_verified": true, "name": "Ada Lovelace",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// TestGoogleClientIdentify checks the code exchange and ID token verification.
//
// Expected output:
//
//   - The token request carries the code, PKCE verifier, client credentials and
//     redirect URI; a valid RS256 ID token yields the verified identity.
//   - Wrong audience, issuer, expiry, algorithm, signing key or missing claims
//     are ErrGoogleRejected; a refused code is ErrGoogleRejected.
//   - Token endpoint 5xx and an unavailable key set are ErrGoogleUnavailable.
func TestGoogleClientIdentify(t *testing.T) {
	stub, server := newGoogleStub(t)
	client, err := googleclient.New(googleclient.Config{
		ClientID: testGoogleClientID, ClientSecret: "client-secret", RedirectURI: "https://example.com/cb",
		TokenEndpoint: server.URL + "/token", KeysEndpoint: server.URL + "/certs",
	})
	if err != nil {
		t.Fatal(err)
	}

	stub.idToken = stub.sign(t, validGoogleClaims(), "key-1", jwt.SigningMethodRS256, stub.key)
	identity, err := client.Identify(context.Background(), "auth-code", "verifier-123")
	if err != nil || identity.Subject != "1081234567890" || identity.Email != "ada@example.com" || !identity.EmailVerified || identity.Name != "Ada Lovelace" {
		t.Fatalf("Identify() = %+v, %v", identity, err)
	}
	want := map[string]string{"grant_type": "authorization_code", "code": "auth-code", "code_verifier": "verifier-123",
		"client_id": testGoogleClientID, "client_secret": "client-secret", "redirect_uri": "https://example.com/cb"}
	for name, value := range want {
		if stub.lastForm[name] != value {
			t.Fatalf("token form %s = %q, want %q", name, stub.lastForm[name], value)
		}
	}

	stringVerified := validGoogleClaims()
	stringVerified["email_verified"] = "true"
	stub.idToken = stub.sign(t, stringVerified, "key-1", jwt.SigningMethodRS256, stub.key)
	if identity, err := client.Identify(context.Background(), "c", "v"); err != nil || !identity.EmailVerified {
		t.Fatalf("string email_verified = %+v, %v", identity, err)
	}
	unverified := validGoogleClaims()
	unverified["email_verified"] = false
	stub.idToken = stub.sign(t, unverified, "key-1", jwt.SigningMethodRS256, stub.key)
	if identity, err := client.Identify(context.Background(), "c", "v"); err != nil || identity.EmailVerified {
		t.Fatalf("unverified email = %+v, %v", identity, err)
	}

	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	mutate := func(change func(jwt.MapClaims)) jwt.MapClaims {
		claims := validGoogleClaims()
		change(claims)
		return claims
	}
	for name, raw := range map[string]string{
		"wrong audience": stub.sign(t, mutate(func(c jwt.MapClaims) { c["aud"] = "someone-else" }), "key-1", jwt.SigningMethodRS256, stub.key),
		"wrong issuer":   stub.sign(t, mutate(func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }), "key-1", jwt.SigningMethodRS256, stub.key),
		"expired":        stub.sign(t, mutate(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), "key-1", jwt.SigningMethodRS256, stub.key),
		"no subject":     stub.sign(t, mutate(func(c jwt.MapClaims) { delete(c, "sub") }), "key-1", jwt.SigningMethodRS256, stub.key),
		"no email":       stub.sign(t, mutate(func(c jwt.MapClaims) { delete(c, "email") }), "key-1", jwt.SigningMethodRS256, stub.key),
		"other key":      stub.sign(t, validGoogleClaims(), "key-1", jwt.SigningMethodRS256, otherKey),
		"unknown kid":    stub.sign(t, validGoogleClaims(), "key-9", jwt.SigningMethodRS256, stub.key),
		"hs256":          stub.sign(t, validGoogleClaims(), "key-1", jwt.SigningMethodHS256, []byte("client-secret")),
	} {
		stub.idToken = raw
		if _, err := client.Identify(context.Background(), "c", "v"); !errors.Is(err, service.ErrGoogleRejected) {
			t.Errorf("%s: error = %v, want ErrGoogleRejected", name, err)
		}
	}

	stub.tokenCode = http.StatusBadRequest
	if _, err := client.Identify(context.Background(), "c", "v"); !errors.Is(err, service.ErrGoogleRejected) {
		t.Fatalf("refused code error = %v", err)
	}
	stub.tokenCode = http.StatusServiceUnavailable
	_, err = client.Identify(context.Background(), "c", "v")
	if !errors.Is(err, service.ErrGoogleUnavailable) || strings.Contains(err.Error(), "client-secret") {
		t.Fatalf("token outage error = %v", err)
	}
}

// TestGoogleClientKeyOutage checks that an unreachable key set is an outage.
func TestGoogleClientKeyOutage(t *testing.T) {
	stub, server := newGoogleStub(t)
	stub.keysCode = http.StatusInternalServerError
	stub.idToken = stub.sign(t, validGoogleClaims(), "key-1", jwt.SigningMethodRS256, stub.key)
	client, _ := googleclient.New(googleclient.Config{
		ClientID: testGoogleClientID, ClientSecret: "s", RedirectURI: "https://example.com/cb",
		TokenEndpoint: server.URL + "/token", KeysEndpoint: server.URL + "/certs",
	})
	if _, err := client.Identify(context.Background(), "c", "v"); !errors.Is(err, service.ErrGoogleUnavailable) {
		t.Fatalf("key outage error = %v", err)
	}
	if _, err := googleclient.New(googleclient.Config{ClientID: "x"}); err == nil {
		t.Fatal("incomplete config accepted")
	}
}

// TestGoogleUsername checks generated usernames for new Google accounts.
func TestGoogleUsername(t *testing.T) {
	for _, tt := range []struct {
		name, email string
		attempt     int
		want        string
	}{
		{"Ada Lovelace", "ada@example.com", 1, "Ada_Lovelace"},
		{"Ada Lovelace", "ada@example.com", 2, "Ada_Lovelace_2"},
		{"Zoë O'Brien-Smith", "z@example.com", 1, "Zo_OBrien_Smith"},
		{"李", "first.last@example.com", 1, "first_last"},
		{"", "a@example.com", 1, "player"},
		{"Alexandria Ocasio Cortez", "x@example.com", 1, "Alexandria_Ocasio_Co"},
		{"Alexandria Ocasio Cortez", "x@example.com", 10, "Alexandria_Ocasio_10"},
	} {
		if got := service.GoogleUsername(tt.name, tt.email, tt.attempt); got != tt.want || len(got) > 20 {
			t.Errorf("GoogleUsername(%q, %q, %d) = %q, want %q", tt.name, tt.email, tt.attempt, got, tt.want)
		}
	}
}
