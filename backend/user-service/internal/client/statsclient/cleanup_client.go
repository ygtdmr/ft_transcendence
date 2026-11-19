package statsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrRejected reports a permanent answer to a cleanup call: a validation
// error or a conflict. The coordinator stops and does not retry the operation.
var ErrRejected = errors.New("stats service rejected the cleanup request")

// CleanUpAccount sends PUT /internal/v1/account-cleanups/{operation_id}.
//
// Errors:
//
//   - ErrUnavailable: network failure, timeout, 5xx or an unreadable receipt.
//   - ErrMisconfigured: 401 or 403.
//   - ErrRejected: 400, 409 or 422, or a receipt for another operation or user.
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
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	switch status := response.StatusCode; {
	case status == http.StatusOK || status == http.StatusCreated:
		return checkCleanupReceipt(raw, operationID, userID)
	case status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", ErrUnavailable, status)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrMisconfigured
	default:
		return fmt.Errorf("%w: status %d", ErrRejected, status)
	}
}

func checkCleanupReceipt(raw []byte, operationID string, userID int64) error {
	var envelope struct {
		Data *struct {
			OperationID string `json:"operation_id"`
			UserID      int64  `json:"user_id"`
			State       string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return fmt.Errorf("%w: malformed cleanup receipt", ErrUnavailable)
	}
	if d := envelope.Data; !strings.EqualFold(d.OperationID, operationID) || d.UserID != userID || d.State != "COMPLETED" {
		return fmt.Errorf("%w: unexpected cleanup receipt", ErrRejected)
	}

	return nil
}
