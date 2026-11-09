// Package userclient calls User Service's private provisioning operations.
package userclient

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

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const (
	maxResponseBody = 16 << 10
	timeLayout      = "2006-01-02T15:04:05.000000Z07:00"
)

var _ service.ProfileProvisioner = (*Client)(nil)

// Client sends provisioning requests to User Service's private listener.
type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

// New returns a client for User Service's private listener.
//
// Parameters:
//
//   - baseURL: User private listener origin, such as http://user-service:8090.
//   - credential: Auth's credential for User Service (Auth-to-User pair).
//   - timeout: Upper bound for one request; a timeout means the outcome is unknown.
//
// Errors:
//
//   - baseURL is not an absolute http(s) origin, credential is empty or timeout is not positive.
func New(baseURL, credential string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("user service base URL must be an http(s) origin without a path")
	}
	if credential == "" || timeout <= 0 {
		return nil, errors.New("user service client needs a credential and a positive timeout")
	}

	return &Client{
		baseURL:    parsed.String(),
		credential: credential,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

// Provision sends PUT /internal/v1/provisioning-operations/{operation_id}.
//
// Errors map User Service answers to the service.ErrProvision* values:
// network failures, timeouts, 5xx and unreadable success bodies are
// ErrProvisionUnavailable; 401/403/400/422 VALIDATION_ERROR and
// OPERATION_CONFLICT are ErrProvisionRejected.
func (c *Client) Provision(ctx context.Context, request service.ProvisionRequest) (service.ProvisionReceipt, error) {
	body, err := json.Marshal(map[string]any{
		"user_id":    request.UserID,
		"username":   request.Username,
		"created_at": request.CreatedAt.UTC().Format(timeLayout),
	})
	if err != nil {
		return service.ProvisionReceipt{}, err
	}

	return c.do(ctx, http.MethodPut, "/internal/v1/provisioning-operations/"+url.PathEscape(request.OperationID), body)
}

// ReceiptByUser sends GET /internal/v1/provisioning-operations?user_id=.
func (c *Client) ReceiptByUser(ctx context.Context, userID int64) (service.ProvisionReceipt, error) {
	return c.do(ctx, http.MethodGet, "/internal/v1/provisioning-operations?user_id="+strconv.FormatInt(userID, 10), nil)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (service.ProvisionReceipt, error) {
	status, raw, err := c.send(ctx, method, path, body)
	if err != nil {
		return service.ProvisionReceipt{}, fmt.Errorf("%w: %w", service.ErrProvisionUnavailable, err)
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return decodeReceipt(raw)
	}
	if status >= http.StatusInternalServerError {
		return service.ProvisionReceipt{}, fmt.Errorf("%w: status %d", service.ErrProvisionUnavailable, status)
	}

	return service.ProvisionReceipt{}, mapError(status, errorCode(raw))
}

// send performs one request. An error means no complete answer was received.
func (c *Client) send(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
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
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}

	return response.StatusCode, raw, nil
}

func mapError(status int, code string) error {
	switch code {
	case "USERNAME_TAKEN":
		return service.ErrProvisionUsernameTaken
	case "INVALID_USERNAME":
		return service.ErrProvisionUsernameInvalid
	case "USER_ALREADY_PROVISIONED":
		return service.ErrProvisionAlreadyProvisioned
	case "USER_DELETED":
		return service.ErrProvisionUserDeleted
	case "PROVISIONING_NOT_FOUND":
		return service.ErrProvisionNotFound
	}

	return fmt.Errorf("%w: status %d code %q", service.ErrProvisionRejected, status, code)
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

func decodeReceipt(raw []byte) (service.ProvisionReceipt, error) {
	var envelope struct {
		Data *struct {
			OperationID string `json:"operation_id"`
			UserID      int64  `json:"user_id"`
			Outcome     string `json:"outcome"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil ||
		envelope.Data.Outcome != "PROVISIONED" || envelope.Data.OperationID == "" || envelope.Data.UserID <= 0 {
		return service.ProvisionReceipt{}, fmt.Errorf("%w: malformed receipt", service.ErrProvisionUnavailable)
	}

	return service.ProvisionReceipt{OperationID: strings.ToLower(envelope.Data.OperationID), UserID: envelope.Data.UserID}, nil
}
