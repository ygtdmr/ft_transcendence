package gameclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.AccountCleaner = (*Client)(nil)

// CleanUpAccount sends PUT /internal/v1/account-cleanups/{operation_id}.
//
// Errors:
//
//   - service.ErrUnavailable: network failure, timeout, 5xx or an unreadable receipt.
//   - service.ErrParticipantRejected: any other status, or a receipt for
//     another operation or user.
func (c *Client) CleanUpAccount(ctx context.Context, operationID string, userID int64) error {
	body, err := json.Marshal(map[string]int64{"user_id": userID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.baseURL+"/internal/v1/account-cleanups/"+url.PathEscape(operationID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: game session: %w", service.ErrUnavailable, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("%w: game session: read response: %w", service.ErrUnavailable, err)
	}

	switch status := response.StatusCode; {
	case status == http.StatusOK || status == http.StatusCreated:
	case status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: game session status %d", service.ErrUnavailable, status)
	default:
		return fmt.Errorf("%w: game session status %d code %q", service.ErrParticipantRejected, status, errorCode(raw))
	}

	var envelope struct {
		Data *struct {
			OperationID string `json:"operation_id"`
			UserID      int64  `json:"user_id"`
			State       string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return fmt.Errorf("%w: game session: malformed cleanup receipt", service.ErrUnavailable)
	}
	if d := envelope.Data; !strings.EqualFold(d.OperationID, operationID) || d.UserID != userID || d.State != "COMPLETED" {
		return fmt.Errorf("%w: game session: unexpected cleanup receipt", service.ErrParticipantRejected)
	}

	return nil
}
