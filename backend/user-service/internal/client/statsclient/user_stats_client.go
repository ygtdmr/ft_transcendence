// Package statsclient reads user counters from Stats Service's private listener.
package statsclient

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
	// ErrUnavailable reports that Stats gave no usable answer; the counters are
	// unknown, never zero.
	ErrUnavailable = errors.New("stats service unavailable")
	// ErrMisconfigured reports a rejected service credential or a missing scope.
	ErrMisconfigured = errors.New("stats service rejected this caller")
)

// UserStats are one user's counters. PenaltyPoints is owner-only.
type UserStats struct {
	UserID        int64
	Level         int
	Wins          int
	Losses        int
	MatchesPlayed int
	PenaltyPoints int
}

// Client reads GET /internal/v1/user-stats/{user_id}.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for Stats Service's private listener.
//
// Parameters:
//
//   - baseURL: Stats private listener origin, such as http://stats-service:8090.
//   - credential: This service's credential for Stats (User-to-Stats pair).
//   - timeout: Upper bound for one request; a timeout means the counters are unknown.
//
// Errors:
//
//   - baseURL is not an absolute http(s) origin, credential is empty or timeout is not positive.
func New(baseURL, credential string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("stats base URL must be an http(s) origin without a path")
	}
	if credential == "" || timeout <= 0 {
		return nil, errors.New("stats client needs a credential and a positive timeout")
	}

	return &Client{
		baseURL:    parsed.String(),
		credential: credential,
		http:       &http.Client{Timeout: timeout, CheckRedirect: noRedirects},
	}, nil
}

// noRedirects keeps the credential from following a redirect to another host.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// UserStats reads userID's counters. A user with no recorded match has zero
// counters and level 1, as Stats reports them.
//
// Errors:
//
//   - ErrUnavailable: network failure, timeout, 5xx or an invalid success body.
//   - ErrMisconfigured: 401 or 403; the service credential or scope is wrong.
//   - Other errors: an unexpected response status; treat it as a caller defect.
func (c *Client) UserStats(ctx context.Context, userID int64) (UserStats, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/v1/user-stats/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return UserStats{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		// The error may embed the URL but never the credential header.
		return UserStats{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return UserStats{}, fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		return decodeUserStats(body, userID)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return UserStats{}, ErrMisconfigured
	case response.StatusCode >= 500:
		return UserStats{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return UserStats{}, fmt.Errorf("unexpected stats response status %d", response.StatusCode)
	}
}

func decodeUserStats(body []byte, userID int64) (UserStats, error) {
	var envelope struct {
		Data *struct {
			UserID        int64 `json:"user_id"`
			Level         *int  `json:"level"`
			Wins          *int  `json:"wins"`
			Losses        *int  `json:"losses"`
			MatchesPlayed *int  `json:"matches_played"`
			PenaltyPoints *int  `json:"penalty_points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil {
		return UserStats{}, fmt.Errorf("%w: malformed stats response", ErrUnavailable)
	}
	d := envelope.Data
	if d.UserID != userID || d.Level == nil || d.Wins == nil || d.Losses == nil || d.MatchesPlayed == nil || d.PenaltyPoints == nil ||
		*d.Level < 1 || *d.Wins < 0 || *d.Losses < 0 || *d.MatchesPlayed != *d.Wins+*d.Losses || *d.PenaltyPoints < 0 {
		return UserStats{}, fmt.Errorf("%w: invalid stats response", ErrUnavailable)
	}

	return UserStats{UserID: d.UserID, Level: *d.Level, Wins: *d.Wins, Losses: *d.Losses,
		MatchesPlayed: *d.MatchesPlayed, PenaltyPoints: *d.PenaltyPoints}, nil
}
