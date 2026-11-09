package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// AvatarFileReader returns stored avatar images.
type AvatarFileReader interface {
	Read(key string) ([]byte, error)
}

// AvatarFilesController serves avatar images. They carry no private data and
// are loaded by image tags, so no session is required.
type AvatarFilesController struct {
	files AvatarFileReader
}

// NewAvatarFiles returns the controller for avatar images. files may be nil
// when avatar storage is not configured; default avatars are still served.
func NewAvatarFiles(files AvatarFileReader) *AvatarFilesController {
	return &AvatarFilesController{files: files}
}

// RegisterRoutes adds the avatar image routes.
func (h *AvatarFilesController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/static/avatars/default/:file", router.Wrap(h.defaultAvatar, mapFriendError))
	if h.files != nil {
		routes.GET("/static/avatars/uploaded/:key", router.Wrap(h.uploaded, mapFriendError))
	}
}

func (h *AvatarFilesController) defaultAvatar(c *gin.Context) error {
	raw, found := strings.CutSuffix(c.Param("file"), ".svg")
	userID, err := strconv.ParseInt(raw, 10, 64)
	if !found || err != nil || userID <= 0 {
		return avatarFileNotFound()
	}
	writeAvatarImage(c, "image/svg+xml", "public, max-age=86400", service.DefaultAvatarSVG(userID))

	return nil
}

func (h *AvatarFilesController) uploaded(c *gin.Context) error {
	image, err := h.files.Read(c.Param("key"))
	if errors.Is(err, service.ErrAvatarFileNotFound) {
		return avatarFileNotFound()
	}
	if err != nil {
		return err
	}
	// A key is random and never reused, so its content cannot change.
	writeAvatarImage(c, "image/png", "public, max-age=31536000, immutable", image)

	return nil
}

// writeAvatarImage fixes the type and forbids sniffing and active content, so a
// file can only ever be displayed as an image.
func writeAvatarImage(c *gin.Context, contentType, cacheControl string, image []byte) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	c.Header("Cache-Control", cacheControl)
	c.Data(http.StatusOK, contentType, image)
}

func avatarFileNotFound() *httpresponse.Error {
	return &httpresponse.Error{Status: http.StatusNotFound, Code: "AVATAR_NOT_FOUND",
		Message: "No avatar exists at this address."}
}
