// Package gatewayclient reads one Gateway instance's active socket registry.
package gatewayclient

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

// MaxUsersPerRequest is the most user IDs one connections read may carry.
const MaxUsersPerRequest = 100

const maxResponseBody = 16 << 10

var (
	// ErrUnavailable reports that the instance gave no usable answer; nothing is
	// known about the requested users.
	ErrUnavailable = errors.New("gateway registry unavailable")
	// ErrMisconfigured reports a rejected service credential or a missing scope.
	ErrMisconfigured = errors.New("gateway rejected this caller")
)

// Client reads GET /internal/v1/connections from one Gateway instance.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for one Gateway instance's private listener.
//
// Parameters:
//
//   - baseURL: Instance origin, such as http://game-gateway-1:8090.
//   - credential: This service's credential for that Gateway (User-to-Gateway pair).
//   - timeout: Upper bound for one request; a timeout means nothing is known.
//
// Errors:
//
//   - baseURL is not an absolute http(s) origin, credential is empty or timeout is not positive.
func New(baseURL, credential string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("gateway base URL must be an http(s) origin without a path")
	}
	if credential == "" || timeout <= 0 {
		return nil, errors.New("gateway client needs a credential and a positive timeout")
	}

	return &Client{
		baseURL:    parsed.String(),
		credential: credential,
		http:       &http.Client{Timeout: timeout, CheckRedirect: noRedirects},
	}, nil
}

// noRedirects keeps the credential from following a redirect to another host.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Connected returns which of userIDs hold an open authenticated socket on this
// instance. An empty result is a complete answer that none of them do.
//
// Parameters:
//
//   - userIDs: 1..MaxUsersPerRequest distinct positive IDs.
//
// Errors:
//
//   - ErrUnavailable: network failure, timeout, 5xx, or a body that is not a
//     subset of the requested IDs.
//   - ErrMisconfigured: 401 or 403; the service credential or scope is wrong.
//   - Other errors: invalid input or an unexpected response status.
func (c *Client) Connected(ctx context.Context, userIDs []int64) ([]int64, error) {
	if len(userIDs) == 0 || len(userIDs) > MaxUsersPerRequest {
		return nil, fmt.Errorf("connections read needs 1..%d user IDs", MaxUsersPerRequest)
	}
	query := url.Values{}
	requested := make(map[int64]bool, len(userIDs))
	for _, id := range userIDs {
		query.Add("user_id", strconv.FormatInt(id, 10))
		requested[id] = true
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/v1/connections?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		// The error may embed the URL but never the credential header.
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		return decodeConnected(body, requested)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, ErrMisconfigured
	case response.StatusCode >= 500:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return nil, fmt.Errorf("unexpected gateway response status %d", response.StatusCode)
	}
}

func decodeConnected(body []byte, requested map[int64]bool) ([]int64, error) {
	var envelope struct {
		Data *struct {
			ConnectedUserIDs *[]int64 `json:"connected_user_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil || envelope.Data.ConnectedUserIDs == nil {
		return nil, fmt.Errorf("%w: malformed connections response", ErrUnavailable)
	}
	connected := *envelope.Data.ConnectedUserIDs
	for _, id := range connected {
		if !requested[id] {
			return nil, fmt.Errorf("%w: connections response names an unrequested user", ErrUnavailable)
		}
	}

	return connected, nil
}
