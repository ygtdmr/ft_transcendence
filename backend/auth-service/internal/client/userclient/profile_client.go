package userclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var _ service.ProfileReader = (*Client)(nil)

// CanonicalUsername sends GET /internal/v1/profiles/{user_id}.
//
// Network failures, timeouts, 5xx and unreadable success bodies are
// service.ErrProfileUnavailable. 409 USER_DELETED is service.ErrProfileDeleted.
// Any other answer means broken configuration or data and is a plain error.
func (c *Client) CanonicalUsername(ctx context.Context, userID int64) (string, error) {
	status, raw, err := c.send(ctx, http.MethodGet, "/internal/v1/profiles/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", service.ErrProfileUnavailable, err)
	}
	switch {
	case status == http.StatusOK:
		return decodeUsername(raw, userID)
	case status >= http.StatusInternalServerError:
		return "", fmt.Errorf("%w: status %d", service.ErrProfileUnavailable, status)
	case status == http.StatusConflict && errorCode(raw) == "USER_DELETED":
		return "", service.ErrProfileDeleted
	}

	return "", fmt.Errorf("user service profile read: status %d code %q", status, errorCode(raw))
}

func decodeUsername(raw []byte, userID int64) (string, error) {
	var envelope struct {
		Data *struct {
			UserID   int64  `json:"user_id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil ||
		envelope.Data.UserID != userID || envelope.Data.Username == "" {
		return "", fmt.Errorf("%w: malformed profile", service.ErrProfileUnavailable)
	}

	return envelope.Data.Username, nil
}
