// Package gameclient calls Game Session's private listener.
package gameclient

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

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// maxResponseBody bounds one membership answer; a valid one is far smaller.
const maxResponseBody = 64 << 10

var _ service.MembershipReader = (*Client)(nil)

// Client reads room membership from Game Session.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for Game Session's private listener.
//
// Parameters:
//
//   - baseURL: Game Session private listener origin, such as http://game-session:8093.
//   - credential: User's credential for Game Session (User-to-Game pair).
//   - timeout: Upper bound for one request; a timeout means the outcome is unknown.
//
// Errors:
//
//   - baseURL is not an absolute http(s) origin, credential is empty or timeout is not positive.
func New(baseURL, credential string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("game session base URL must be an http(s) origin without a path")
	}
	if credential == "" || timeout <= 0 {
		return nil, errors.New("game session client needs a credential and a positive timeout")
	}

	return &Client{
		baseURL:    parsed.String(),
		credential: credential,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

// CurrentMembership sends GET /internal/v1/memberships/{user_id}.
//
// Network failures, timeouts, 5xx and unreadable success bodies are
// service.ErrMembershipUnavailable. Any other answer, such as 401, 403 or 422,
// means broken configuration and is a plain error.
func (c *Client) CurrentMembership(ctx context.Context, userID int64) (service.Membership, error) {
	status, raw, err := c.get(ctx, "/internal/v1/memberships/"+strconv.FormatInt(userID, 10))
	if err != nil {
		return service.Membership{}, fmt.Errorf("%w: %w", service.ErrMembershipUnavailable, err)
	}
	switch {
	case status == http.StatusOK:
		return decodeMembership(raw, userID)
	case status >= http.StatusInternalServerError:
		return service.Membership{}, fmt.Errorf("%w: status %d", service.ErrMembershipUnavailable, status)
	}

	return service.Membership{}, fmt.Errorf("game session membership read: status %d code %q", status, errorCode(raw))
}

// get performs one request. An error means no complete answer was received.
func (c *Client) get(ctx context.Context, path string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}

	return response.StatusCode, raw, nil
}

// decodeMembership requires both fields to be present, possibly null, for the
// requested user. Pairing and value rules are checked by the service.
func decodeMembership(raw []byte, userID int64) (service.Membership, error) {
	malformed := fmt.Errorf("%w: malformed membership", service.ErrMembershipUnavailable)
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return service.Membership{}, malformed
	}
	rawUser, hasUser := envelope.Data["user_id"]
	rawRoom, hasRoom := envelope.Data["active_room_id"]
	rawVersion, hasVersion := envelope.Data["active_room_api_version"]
	var answeredUser int64
	if !hasUser || !hasRoom || !hasVersion || json.Unmarshal(rawUser, &answeredUser) != nil || answeredUser != userID {
		return service.Membership{}, malformed
	}
	var membership service.Membership
	if json.Unmarshal(rawRoom, &membership.RoomID) != nil || json.Unmarshal(rawVersion, &membership.APIVersion) != nil {
		return service.Membership{}, malformed
	}

	return membership, nil
}

func errorCode(raw []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}

	return envelope.Error.Code
}
