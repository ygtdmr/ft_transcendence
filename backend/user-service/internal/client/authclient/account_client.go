// Package authclient calls Auth Service's private account reads.
package authclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBody = 16 << 10

var (
	// ErrAccountNotFound reports that Auth holds no account for the user.
	ErrAccountNotFound = errors.New("auth account not found")
	// ErrSessionInvalid reports that Auth does not accept the browser session.
	ErrSessionInvalid = errors.New("auth session invalid")
	// ErrUnavailable reports that Auth could not answer; the outcome is unknown
	// and the caller may retry later.
	ErrUnavailable = errors.New("auth service unavailable")
	// ErrMisconfigured reports a rejected service credential or a missing
	// scope; callers fail closed and do not retry.
	ErrMisconfigured = errors.New("auth service rejected this caller")
)

// Account statuses returned by Auth.
const (
	StatusActive       = "ACTIVE"
	StatusProvisioning = "PROVISIONING"
	StatusDeleting     = "DELETING"
)

// Account is Auth-owned account metadata. Email is owner-only: compose it into
// the owner's private profile and never store it or return it elsewhere.
type Account struct {
	UserID    int64
	Email     string
	Status    string
	CreatedAt time.Time
}

// Client reads account metadata from Auth's private listener.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for Auth's private listener.
//
// Parameters:
//
//   - baseURL: Auth private listener origin, such as http://auth-service:8090.
//   - credential: This service's credential for Auth (User-to-Auth pair).
//   - timeout: Upper bound for one request; a timeout means the outcome is unknown.
//
// Errors:
//
//   - baseURL is not an absolute http(s) origin, credential is empty or timeout is not positive.
func New(baseURL, credential string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("auth base URL must be an http(s) origin without a path")
	}
	if credential == "" || timeout <= 0 {
		return nil, errors.New("auth client needs a credential and a positive timeout")
	}

	return &Client{
		baseURL:    parsed.String(),
		credential: credential,
		http:       &http.Client{Timeout: timeout, CheckRedirect: noRedirects},
	}, nil
}

// noRedirects keeps the credential from following a redirect to another host.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// ValidateSession asks Auth who holds a browser ft_session credential, through
// GET /internal/v1/sessions/current. The credential travels only as a Cookie
// header next to this service's own credential.
//
// It returns the signed-in user's id.
//
// Errors:
//
//   - ErrSessionInvalid: Auth answered 401 UNAUTHORIZED.
//   - ErrUnavailable: network failure, timeout, 5xx or an unreadable success body.
//   - ErrMisconfigured: Auth refused this service's credential or scope.
//   - Other errors: an unexpected response; treat it as a caller defect.
func (c *Client) ValidateSession(ctx context.Context, session string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/v1/sessions/current", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")
	request.AddCookie(&http.Cookie{Name: "ft_session", Value: session})

	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return 0, fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		return decodeSession(body)
	case response.StatusCode == http.StatusUnauthorized && errorCode(body) == "UNAUTHORIZED":
		return 0, ErrSessionInvalid
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return 0, ErrMisconfigured
	case response.StatusCode >= 500:
		return 0, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return 0, fmt.Errorf("unexpected auth response status %d", response.StatusCode)
	}
}

func decodeSession(body []byte) (int64, error) {
	var envelope struct {
		Data *struct {
			UserID int64 `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil || envelope.Data.UserID <= 0 {
		return 0, fmt.Errorf("%w: malformed session response", ErrUnavailable)
	}

	return envelope.Data.UserID, nil
}

func errorCode(body []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)

	return envelope.Error.Code
}

// AccountMetadata reads GET /internal/v1/accounts/{user_id}.
//
// Errors:
//
//   - ErrAccountNotFound: 404 USER_NOT_FOUND.
//   - ErrUnavailable: network failure, timeout, 5xx or an unreadable success body.
//   - ErrMisconfigured: 401 or 403; the service credential or scope is wrong.
//   - Other errors: an unexpected response; treat it as a caller defect.
func (c *Client) AccountMetadata(ctx context.Context, userID int64) (Account, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/v1/accounts/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return Account{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		// The error may embed the URL but never the credential header.
		return Account{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return Account{}, fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		return decodeAccount(body, userID)
	case response.StatusCode == http.StatusNotFound:
		return Account{}, ErrAccountNotFound
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return Account{}, ErrMisconfigured
	case response.StatusCode >= 500:
		return Account{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return Account{}, fmt.Errorf("unexpected auth response status %d", response.StatusCode)
	}
}

func decodeAccount(body []byte, userID int64) (Account, error) {
	var envelope struct {
		Data *struct {
			UserID    int64  `json:"user_id"`
			Email     string `json:"email"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil {
		return Account{}, fmt.Errorf("%w: malformed account response", ErrUnavailable)
	}
	data := envelope.Data
	createdAt, err := time.Parse(time.RFC3339Nano, data.CreatedAt)
	validStatus := data.Status == StatusActive || data.Status == StatusProvisioning || data.Status == StatusDeleting
	if err != nil || data.UserID != userID || data.Email == "" || !validStatus {
		return Account{}, fmt.Errorf("%w: invalid account response", ErrUnavailable)
	}

	return Account{UserID: data.UserID, Email: data.Email, Status: data.Status, CreatedAt: createdAt.UTC()}, nil
}
