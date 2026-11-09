package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const (
	maxPresetBody = 1 << 10
	// maxAvatarRequest leaves room for multipart boundaries and part headers.
	maxAvatarRequest = service.MaxAvatarBytes + 64<<10
)

// AvatarService answers the avatar routes.
type AvatarService interface {
	Presets() []service.AvatarPresetEntry
	SelectPreset(ctx context.Context, userID int64, presetID string) (string, error)
	Upload(ctx context.Context, userID int64, raw []byte) (string, error)
}

// AvatarsController serves the public avatar routes.
type AvatarsController struct {
	avatars  AvatarService
	sessions sessionauth.Validator
}

// NewAvatars returns the controller for the avatar catalog and the caller's avatar.
func NewAvatars(avatars AvatarService, sessions sessionauth.Validator) *AvatarsController {
	return &AvatarsController{avatars: avatars, sessions: sessions}
}

// RegisterRoutes adds the avatar routes behind session validation.
func (h *AvatarsController) RegisterRoutes(routes gin.IRoutes) {
	const own = "/api/v1/users/me/avatar"
	auth := sessionauth.Require(h.sessions)
	routes.GET("/api/v1/avatars/presets", auth, router.Wrap(h.presets, mapFriendError))
	routes.PUT(own, auth, router.Wrap(h.selectPreset, mapFriendError))
	routes.POST(own, auth, router.Wrap(h.upload, mapFriendError))
}

func (h *AvatarsController) presets(c *gin.Context) error {
	presets := h.avatars.Presets()
	entries := make([]gin.H, 0, len(presets))
	for _, preset := range presets {
		entries = append(entries, gin.H{"preset_id": preset.PresetID, "avatar_url": preset.AvatarURL})
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"presets": entries}})

	return nil
}

func (h *AvatarsController) selectPreset(c *gin.Context) error {
	presetID, err := decodePresetID(c.Request.Body)
	if err != nil {
		return err
	}
	avatarURL, err := h.avatars.SelectPreset(c.Request.Context(), sessionauth.UserID(c), presetID)
	if err != nil {
		return avatarError(err, presetID)
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"avatar_url": avatarURL}})

	return nil
}

func (h *AvatarsController) upload(c *gin.Context) error {
	raw, err := readAvatarPart(c)
	if err != nil {
		return avatarError(err, "")
	}
	avatarURL, err := h.avatars.Upload(c.Request.Context(), sessionauth.UserID(c), raw)
	if err != nil {
		return avatarError(err, "")
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"avatar_url": avatarURL}})

	return nil
}

// readAvatarPart streams the request and buffers only the avatar field, so a
// large request cannot fill memory or disk. Every malformed upload is
// service.ErrInvalidImage, the contract's one error for a bad file.
func readAvatarPart(c *gin.Context) ([]byte, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarRequest)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, service.ErrInvalidImage
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, service.ErrInvalidImage
		}
		if part.FormName() != "avatar" {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(part, service.MaxAvatarBytes+1))
		if err != nil {
			return nil, service.ErrInvalidImage
		}

		return raw, nil
	}
}

// decodePresetID reads one JSON object. Malformed JSON is 400; a missing or
// non-string preset_id is an empty id, which is not in the catalog.
func decodePresetID(body io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxPresetBody+1))
	if err != nil || len(raw) > maxPresetBody {
		return "", malformed("body", "Body must be one JSON object.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return "", malformed("body", "Body must be one JSON object.")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", malformed("body", "Body must be one JSON object.")
	}
	var presetID string
	if value, present := fields["preset_id"]; present && json.Unmarshal(value, &presetID) != nil {
		return "", nil
	}

	return presetID, nil
}

func avatarError(err error, presetID string) error {
	var deleted *service.UserDeletedError
	switch {
	case errors.Is(err, service.ErrInvalidImage):
		return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_IMAGE",
			Message: "Avatar must be image/jpeg, image/png, or image/webp under 2 MiB.", Details: map[string]any{}}
	case errors.Is(err, service.ErrInvalidAvatarPreset):
		message := fmt.Sprintf("Avatar preset '%s' does not exist.", presetID)
		if presetID == "" {
			message = "preset_id is required."
		}
		return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_AVATAR_PRESET",
			Message: message, Details: map[string]any{"field": "preset_id"}}
	case errors.Is(err, service.ErrUserNotFound), errors.As(err, &deleted):
		return sessionauth.Unauthorized()
	default:
		return err
	}
}
