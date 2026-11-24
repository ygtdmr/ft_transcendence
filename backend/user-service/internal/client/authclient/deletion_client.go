package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrRejected reports a permanent answer to a deletion call: a validation
// error, a conflict or a missing record. The coordinator stops and does not
// retry the operation.
var ErrRejected = errors.New("auth service rejected the deletion request")

// BeginDeletion sends PUT /internal/v1/account-deletions/{operation_id}.
//
// Errors:
//
//   - ErrUnavailable: network failure, timeout, 5xx or an unreadable receipt.
//   - ErrMisconfigured: 401 or 403.
//   - ErrRejected: 400, 404, 409 or 422, or a receipt for another operation.
func (c *Client) BeginDeletion(ctx context.Context, operationID string, userID int64) error {
	body, err := json.Marshal(map[string]int64{"user_id": userID})
	if err != nil {
		return err
	}
	status, raw, err := c.deletionCall(ctx, "/internal/v1/account-deletions/"+url.PathEscape(operationID), body)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return deletionError(status, raw)
	}

	return checkDeletionReceipt(raw, operationID, userID, "")
}

// CompleteDeletion sends PUT /internal/v1/account-deletions/{operation_id}/completion.
//
// Errors are those of BeginDeletion; a 200 receipt must be COMPLETED.
func (c *Client) CompleteDeletion(ctx context.Context, operationID string) error {
	status, raw, err := c.deletionCall(ctx, "/internal/v1/account-deletions/"+url.PathEscape(operationID)+"/completion", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return deletionError(status, raw)
	}

	return checkDeletionReceipt(raw, operationID, 0, "COMPLETED")
}

// deletionCall sends one PUT. An error means no complete answer was received.
func (c *Client) deletionCall(ctx context.Context, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	return response.StatusCode, raw, nil
}

func deletionError(status int, raw []byte) error {
	switch {
	case status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", ErrUnavailable, status)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrMisconfigured
	}

	return fmt.Errorf("%w: status %d code %q", ErrRejected, status, errorCode(raw))
}

// checkDeletionReceipt requires the receipt to name the requested operation
// and, when given, the user and the state.
func checkDeletionReceipt(raw []byte, operationID string, userID int64, state string) error {
	var envelope struct {
		Data *struct {
			OperationID string `json:"operation_id"`
			UserID      int64  `json:"user_id"`
			State       string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return fmt.Errorf("%w: malformed deletion receipt", ErrUnavailable)
	}
	d := envelope.Data
	if !strings.EqualFold(d.OperationID, operationID) || (userID != 0 && d.UserID != userID) ||
		(d.State != "BEGUN" && d.State != "COMPLETED") || (state != "" && d.State != state) {
		return fmt.Errorf("%w: receipt for operation %q user %s state %q", ErrRejected, d.OperationID,
			strconv.FormatInt(d.UserID, 10), d.State)
	}

	return nil
}
