// Package authclient validates browser sessions with Auth Service.
package authclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBody = 16 << 10

var (
	// ErrSessionInvalid reports that Auth does not accept the browser session.
	ErrSessionInvalid = errors.New("auth session invalid")
	// ErrUnavailable reports that Auth could not answer.
	ErrUnavailable = errors.New("auth service unavailable")
	// ErrMisconfigured reports a rejected service credential or a missing scope.
	ErrMisconfigured = errors.New("auth service rejected this caller")
)

// Client calls Auth's private listener.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for Auth's private listener.
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

	return &Client{baseURL: parsed.String(), credential: credential, http: &http.Client{Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// ValidateSession asks Auth who holds a browser ft_session credential and
// returns the signed-in user's id.
//
// Errors:
//
//   - ErrSessionInvalid: Auth answered 401 UNAUTHORIZED.
//   - ErrUnavailable: network failure, timeout, 5xx or an unreadable success body.
//   - ErrMisconfigured: Auth refused this service's credential or scope.
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
	case response.StatusCode >= http.StatusInternalServerError:
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
