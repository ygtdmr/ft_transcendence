package gameclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

// departureAttempts bounds how often one logout repeats the identical
// departure request before it reports Game Session as unavailable.
const departureAttempts = 3

// departureBackoff is the pause before the second and third attempt.
var departureBackoff = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}

// EndSession sends PUT /internal/v1/session-departures/{session_id} with
// {"user_id"}, so Game Session removes the user from their room with reason
// SESSION_ENDED before Auth revokes the session.
//
// Game Session keys the receipt by session id, so repeating the identical
// request is safe: 201 applies the departure and 200 returns the stored
// receipt. Network errors, timeouts, 500 and 503 are retried up to
// departureAttempts times.
//
// Errors:
//
//   - service.ErrServiceUnavailable: Game Session gave no usable answer after
//     every attempt, or answered with a receipt for another session or user.
//   - A plain error for any other answer (400, 401, 403, 409, 422): the
//     configuration or the data needs investigation and the logout stops.
func (c *Client) EndSession(ctx context.Context, userID int64, sessionID string) error {
	sessionID = strings.ToLower(sessionID)
	body, err := json.Marshal(map[string]int64{"user_id": userID})
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < departureAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: game session departure: %w", service.ErrServiceUnavailable, ctx.Err())
			case <-time.After(departureBackoff[attempt-1]):
			}
		}
		retry, err := c.recordDeparture(ctx, userID, sessionID, body)
		if err == nil || !retry {
			return err
		}
		last = err
	}

	return fmt.Errorf("%w: game session departure: %w", service.ErrServiceUnavailable, last)
}

// recordDeparture sends one departure request. retry reports whether the
// identical request may be sent again.
func (c *Client) recordDeparture(ctx context.Context, userID int64, sessionID string, body []byte) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.baseURL+"/internal/v1/session-departures/"+sessionID, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return true, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return true, fmt.Errorf("read response: %w", err)
	}

	switch status := response.StatusCode; {
	case status == http.StatusCreated || status == http.StatusOK:
		if err := checkDepartureReceipt(raw, userID, sessionID); err != nil {
			return false, fmt.Errorf("%w: %w", service.ErrServiceUnavailable, err)
		}
		return false, nil
	case status == http.StatusInternalServerError || status == http.StatusServiceUnavailable:
		return true, fmt.Errorf("status %d", status)
	default:
		return false, fmt.Errorf("game session departure: status %d code %q", status, errorCode(raw))
	}
}

// checkDepartureReceipt requires a COMPLETED receipt for this session and user.
func checkDepartureReceipt(raw []byte, userID int64, sessionID string) error {
	var envelope struct {
		Data *struct {
			SessionID string `json:"session_id"`
			UserID    int64  `json:"user_id"`
			State     string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return fmt.Errorf("malformed departure receipt")
	}
	receipt := envelope.Data
	if strings.ToLower(receipt.SessionID) != sessionID || receipt.UserID != userID || receipt.State != "COMPLETED" {
		return fmt.Errorf("departure receipt does not match the request")
	}

	return nil
}
