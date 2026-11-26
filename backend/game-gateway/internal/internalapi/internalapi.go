// Package internalapi serves the Game Gateway's private routes, on the
// private listener only:
//
//   - GET /internal/v1/connections (scope connections.read, caller
//     user-service): which requested users hold an open room socket on this
//     instance, from memory only.
//   - PUT /internal/v1/session-revocations/{session_id} (scope
//     session.revocation.notify, caller auth-service): close every local room
//     socket of a revoked session with 4401, after an UNAUTHORIZED error.
package internalapi

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/socket"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/shared/socketregistry"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

// MaxUsersPerRead is the most distinct user ids one connections read accepts.
const MaxUsersPerRead = 100

// Revocation causes Auth Service sends.
const (
	CauseSessionRevoked = "SESSION_REVOKED"
	CauseAccountDeleted = "ACCOUNT_DELETED"
)

const maxRevocationBody = 1 << 10

var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Controller serves the private routes.
type Controller struct {
	auth     *serviceauth.Authenticator
	registry *socketregistry.Registry
	logger   *slog.Logger
}

// New returns the private routes controller.
func New(auth *serviceauth.Authenticator, registry *socketregistry.Registry, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}

	return &Controller{auth: auth, registry: registry, logger: logger}
}

// RegisterRoutes adds the private routes behind service authentication.
func (h *Controller) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/internal/v1/connections",
		ginhttp.Wrap(h.auth.Require(config.ScopeConnectionsRead)),
		ginhttp.Handle(h.connections, nil, h.logger))
	routes.PUT("/internal/v1/session-revocations/:session_id",
		ginhttp.Wrap(h.auth.Require(config.ScopeSessionRevocationNotify)),
		ginhttp.Handle(h.revoke, nil, h.logger))
}

func (h *Controller) connections(c *gin.Context) error {
	if err := validate.NewQuery(c.Request).Only("user_id").Err(); err != nil {
		return err
	}
	raw := c.Request.URL.Query()["user_id"]
	if len(raw) == 0 {
		return &validate.FieldError{Field: "user_id", Message: "At least one user_id is required."}
	}
	distinct := make(map[int64]struct{}, len(raw))
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		id, err := validate.ParsePositiveInt("user_id", value)
		if err != nil {
			return err
		}
		if _, seen := distinct[id]; !seen {
			distinct[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) > MaxUsersPerRead {
		return &validate.FieldError{Field: "user_id", Message: "At most 100 distinct user_id values are allowed."}
	}
	httpresponse.WriteData(c.Writer, http.StatusOK, gin.H{"connected_user_ids": h.registry.ConnectedUsers(ids)})

	return nil
}

type revocationRequest struct {
	UserID int64  `json:"user_id"`
	Cause  string `json:"cause"`
}

// Validate implements the validate.DecodeJSON constraint.
func (r *revocationRequest) Validate() error {
	return validate.First(
		validate.Positive("user_id", r.UserID),
		validate.OneOf("cause", r.Cause, CauseSessionRevoked, CauseAccountDeleted),
	)
}

func (h *Controller) revoke(c *gin.Context) error {
	sessionID := c.Param("session_id")
	if !sessionIDPattern.MatchString(sessionID) {
		return &validate.FieldError{Field: "session_id", Message: "session_id must be a UUID."}
	}
	req, err := validate.DecodeJSON[revocationRequest](c.Writer, c.Request, validate.Options{MaxBytes: maxRevocationBody})
	if err != nil {
		return err
	}
	closed := h.registry.CloseSession(strings.ToLower(sessionID), socketregistry.Closure{
		Code:   socket.CloseSessionRevoked,
		Reason: req.Cause,
		Final:  socket.UnauthorizedFrame,
	})
	h.logger.InfoContext(c.Request.Context(), "session_sockets_closed",
		"user_id", req.UserID, "cause", req.Cause, "sockets", closed)
	httpresponse.WriteNoContent(c.Writer)

	return nil
}
