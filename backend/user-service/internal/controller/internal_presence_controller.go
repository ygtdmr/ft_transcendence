package controller

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// InternalPresenceController serves the private presence read.
type InternalPresenceController struct {
	presence service.PresenceReader
	auth     *serviceauth.Authenticator
}

// NewInternalPresence returns the controller for the private presence read.
func NewInternalPresence(presence service.PresenceReader, auth *serviceauth.Authenticator) *InternalPresenceController {
	return &InternalPresenceController{presence: presence, auth: auth}
}

// RegisterRoutes adds GET /internal/v1/presence. Register it only on the
// private listener.
func (h *InternalPresenceController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/internal/v1/presence",
		h.auth.Require(serviceauth.ScopePresenceRead), router.Wrap(h.read, mapPresenceError))
}

func (h *InternalPresenceController) read(c *gin.Context) error {
	ids, err := presenceUserIDs(c.Request.URL.Query())
	if err != nil {
		return err
	}
	online, err := h.presence.Online(c.Request.Context(), ids)
	if err != nil {
		return err
	}
	entries := make([]gin.H, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, gin.H{"user_id": id, "is_online": online[id]})
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"presence": entries}})

	return nil
}

// presenceUserIDs reports an unknown parameter as malformed and returns the
// distinct user IDs in ascending order.
func presenceUserIDs(query url.Values) ([]int64, error) {
	for name := range query {
		if name != "user_id" {
			return nil, malformed(name, "Only user_id is allowed.")
		}
	}
	invalid := &service.ValidationError{Field: "user_id",
		Reason: "must be 1 to 100 distinct positive integers"}
	values := query["user_id"]
	if len(values) == 0 {
		return nil, invalid
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, invalid
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > service.MaxPresenceUsers {
		return nil, invalid
	}

	return ids, nil
}

// mapPresenceError reuses the private contract's common error responses.
func mapPresenceError(err error) *httpresponse.Error {
	return mapProvisioningError(err)
}
