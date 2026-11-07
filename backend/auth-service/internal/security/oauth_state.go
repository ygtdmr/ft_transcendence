package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidOAuthState covers a tampered, malformed or expired ft_oauth cookie.
var ErrInvalidOAuthState = errors.New("invalid oauth state cookie")

const oauthCookieKeyInfo = "ft_transcendence auth ft_oauth cookie v1"

// OAuthState is what the authorize step hands to the callback through ft_oauth.
type OAuthState struct {
	State        string
	CodeVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

type sealedOAuthState struct {
	State        string `json:"s"`
	CodeVerifier string `json:"v"`
	ReturnTo     string `json:"r"`
	ExpiresAt    int64  `json:"e"`
}

// OAuthStateSealer encrypts and authenticates ft_oauth cookie values.
//
// The cookie holds the PKCE code verifier, so it is encrypted, not only
// signed. AES-256-GCM keeps it confidential and detects any change.
type OAuthStateSealer struct {
	aead cipher.AEAD
}

// NewOAuthStateSealer derives a dedicated cookie key from the signing secret.
//
// HKDF with a fixed label separates this key from the JWT signing key, so no
// extra secret is needed and neither key reveals the other.
//
// Errors:
//
//   - The secret is shorter than 32 bytes or key derivation fails.
func NewOAuthStateSealer(secret []byte) (*OAuthStateSealer, error) {
	if len(secret) < 32 {
		return nil, errors.New("oauth state key secret must contain at least 32 bytes")
	}
	key, err := hkdf.Key(sha256.New, secret, nil, oauthCookieKeyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("derive oauth state key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &OAuthStateSealer{aead: aead}, nil
}

// Seal returns the base64url cookie value for state.
func (s *OAuthStateSealer) Seal(state OAuthState) (string, error) {
	plain, err := json.Marshal(sealedOAuthState{
		State: state.State, CodeVerifier: state.CodeVerifier, ReturnTo: state.ReturnTo, ExpiresAt: state.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, nil)), nil
}

// Open decrypts a cookie value and rejects it after its expiry.
//
// Errors:
//
//   - ErrInvalidOAuthState: the value is malformed, was changed, or expired at now.
func (s *OAuthStateSealer) Open(value string, now time.Time) (OAuthState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return OAuthState{}, ErrInvalidOAuthState
	}
	nonce, sealed := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return OAuthState{}, ErrInvalidOAuthState
	}
	var decoded sealedOAuthState
	if err := json.Unmarshal(plain, &decoded); err != nil || decoded.State == "" || decoded.CodeVerifier == "" {
		return OAuthState{}, ErrInvalidOAuthState
	}
	expiresAt := time.Unix(decoded.ExpiresAt, 0).UTC()
	if !now.Before(expiresAt) {
		return OAuthState{}, ErrInvalidOAuthState
	}

	return OAuthState{State: decoded.State, CodeVerifier: decoded.CodeVerifier, ReturnTo: decoded.ReturnTo, ExpiresAt: expiresAt}, nil
}

// NewOAuthSecret returns 32 random bytes as base64url text, used for the
// state and the PKCE code verifier (43 characters, within RFC 7636 limits).
func NewOAuthSecret() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// PKCEChallenge returns the S256 code challenge for verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// OAuthStateHash returns the hex SHA-256 used to store a state server-side.
func OAuthStateHash(state string) string {
	sum := sha256.Sum256([]byte(state))

	return hex.EncodeToString(sum[:])
}
