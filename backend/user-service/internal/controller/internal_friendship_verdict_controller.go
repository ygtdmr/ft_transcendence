package controller

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// FriendshipVerdictService answers the private active-friend verdict.
type FriendshipVerdictService interface {
	Verdict(ctx context.Context, actorID, targetID int64) (bool, error)
}

// InternalFriendshipVerdictController serves the private active-friend verdict.
type InternalFriendshipVerdictController struct {
	verdicts FriendshipVerdictService
	auth     *serviceauth.Authenticator
}

// NewInternalFriendshipVerdict returns the controller for the private verdict read.
func NewInternalFriendshipVerdict(verdicts FriendshipVerdictService, auth *serviceauth.Authenticator) *InternalFriendshipVerdictController {
	return &InternalFriendshipVerdictController{verdicts: verdicts, auth: auth}
}

// RegisterRoutes adds GET /internal/v1/friendship-verdicts. Register it only on
// the private listener.
func (h *InternalFriendshipVerdictController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/internal/v1/friendship-verdicts",
		h.auth.Require(serviceauth.ScopeFriendVerdict), router.Wrap(h.read, mapFriendshipVerdictError))
}

func (h *InternalFriendshipVerdictController) read(c *gin.Context) error {
	actorID, targetID, err := verdictParameters(c.Request.URL.Query())
	if err != nil {
		return err
	}
	active, err := h.verdicts.Verdict(c.Request.Context(), actorID, targetID)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"active_friendship": active}})

	return nil
}

// verdictParameters reports a missing, unknown or repeated parameter as
// malformed, then a value that is not an integer as a field error.
func verdictParameters(query url.Values) (actorID, targetID int64, err error) {
	for name := range query {
		if name != "actor_user_id" && name != "target_user_id" {
			return 0, 0, malformed(name, "Only actor_user_id and target_user_id are allowed.")
		}
	}
	for _, name := range []string{"actor_user_id", "target_user_id"} {
		if values := query[name]; len(values) != 1 || values[0] == "" {
			return 0, 0, malformed(name, name+" is required exactly once.")
		}
	}
	if actorID, err = parseVerdictID(query, "actor_user_id"); err != nil {
		return 0, 0, err
	}
	if targetID, err = parseVerdictID(query, "target_user_id"); err != nil {
		return 0, 0, err
	}

	return actorID, targetID, nil
}

func parseVerdictID(query url.Values, name string) (int64, error) {
	id, err := strconv.ParseInt(query.Get(name), 10, 64)
	if err != nil {
		return 0, &service.ValidationError{Field: name, Reason: "must be a positive integer"}
	}

	return id, nil
}

// mapFriendshipVerdictError reuses the private contract's common error responses.
func mapFriendshipVerdictError(err error) *httpresponse.Error {
	return mapProvisioningError(err)
}
