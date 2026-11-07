package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
)

const (
	Issuer   = "ft_transcendence/auth-service"
	Audience = "ft_transcendence"
)

// Claims binds an access token to a user (sub) and revocable session (sid).
type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Credentials contains persistence records and cookie values, never an API body.
type Credentials struct {
	Session         model.Session      `json:"-"`
	Refresh         model.RefreshToken `json:"-"`
	AccessToken     string             `json:"-"`
	RefreshToken    string             `json:"-"`
	AccessExpiresAt time.Time          `json:"-"`
}

type TokenIssuer struct {
	key        []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokenIssuer creates an HS256 credential issuer.
//
// Parameters:
//
//   - key: Random signing material containing at least 32 bytes; copied into the issuer.
//   - accessTTL: Access token lifetime, at least one second.
//   - refreshTTL: Refresh token lifetime, longer than accessTTL.
//
// It returns an issuer with a private copy of key, or nil on failure.
//
// Errors:
//
//   - Invalid key: The signing key contains fewer than 32 bytes.
//   - Invalid lifetimes: The durations do not satisfy 1s <= accessTTL < refreshTTL.
func NewTokenIssuer(key []byte, accessTTL, refreshTTL time.Duration) (*TokenIssuer, error) {
	if len(key) < 32 {
		return nil, errors.New("JWT_SECRET must contain at least 32 bytes")
	}

	if accessTTL < time.Second || refreshTTL <= accessTTL {
		return nil, errors.New("token TTLs must satisfy 1s <= access TTL < refresh TTL")
	}

	return &TokenIssuer{key: append([]byte(nil), key...), accessTTL: accessTTL, refreshTTL: refreshTTL}, nil
}

// Issue generates access and refresh credentials for a persisted user.
//
// Parameters:
//
//   - userID: Positive ID of the persisted user who owns the credentials.
//
// It returns cookie values and session/refresh records for the caller to persist;
// only the refresh token hash appears in its persistence record.
// On failure, it returns a zero Credentials value.
//
// Errors:
//
//   - Invalid user ID: userID is not positive.
//   - Random generation errors: Session, refresh ID or refresh token generation failed.
//   - Signing errors: The access JWT could not be signed.
func (i *TokenIssuer) Issue(userID int64) (Credentials, error) {
	if userID <= 0 {
		return Credentials{}, errors.New("credentials require a positive user ID")
	}

	sessionID, err := newUUID()
	if err != nil {
		return Credentials{}, err
	}
	return i.Rotate(userID, sessionID)
}

// Rotate creates new credentials tied to an existing, caller-validated session.
// The caller must persist the refresh record and revoke its predecessor atomically.
func (i *TokenIssuer) Rotate(userID int64, sessionID string) (Credentials, error) {
	if userID <= 0 || !validSessionID(sessionID) {
		return Credentials{}, errors.New("credentials require a positive user ID and valid session ID")
	}
	credentials, err := newRefreshCredentials(userID, sessionID)
	if err != nil {
		return Credentials{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(i.accessTTL).Truncate(time.Second)
	access, err := i.signAccessToken(userID, credentials.Session.ID, now, expiresAt)
	if err != nil {
		return Credentials{}, err
	}
	credentials.Session.CreatedAt = now
	credentials.Refresh.CreatedAt = now
	credentials.Refresh.ExpiresAt = now.Add(i.refreshTTL).Truncate(time.Second)
	credentials.AccessToken, credentials.AccessExpiresAt = access, expiresAt
	return credentials, nil
}

// signAccessToken binds the user and session to the access token lifetime.
func (i *TokenIssuer) signAccessToken(userID int64, sessionID string, now, expiresAt time.Time) (string, error) {
	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: Issuer, Audience: jwt.ClaimStrings{Audience}, Subject: strconv.FormatInt(userID, 10),
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.key)
}

// newRefreshCredentials creates a refresh identifier and hashed secret for the session.
func newRefreshCredentials(userID int64, sessionID string) (Credentials, error) {
	refreshID, err := newUUID()
	if err != nil {
		return Credentials{}, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Credentials{}, err
	}
	rawRefresh := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(rawRefresh))
	return Credentials{
		Session:      model.Session{ID: sessionID, UserID: userID},
		Refresh:      model.RefreshToken{ID: refreshID, SessionID: sessionID, TokenHash: hex.EncodeToString(hash[:])},
		RefreshToken: rawRefresh,
	}, nil
}

func newUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}

	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

// NewUUID returns a random version 4 UUID in lowercase text form.
func NewUUID() (string, error) {
	return newUUID()
}
