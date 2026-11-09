package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

const (
	defaultMatchLimit = 20
	maxMatchLimit     = 50
)

// StatsReader answers counter and match history reads.
type StatsReader interface {
	UserStats(ctx context.Context, userID int64) (service.UserStats, error)
	MatchHistory(ctx context.Context, userID int64, limit int, before *time.Time) ([]service.MatchSummary, error)
}

// MatchesController serves the signed-in user's match history.
type MatchesController struct {
	reads    StatsReader
	sessions sessionauth.Validator
}

// NewMatches returns the public match history controller.
func NewMatches(reads StatsReader, sessions sessionauth.Validator) *MatchesController {
	return &MatchesController{reads: reads, sessions: sessions}
}

// RegisterRoutes adds GET /api/v1/users/me/matches behind session validation.
func (h *MatchesController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/api/v1/users/me/matches", sessionauth.Require(h.sessions), router.Wrap(h.list, mapStatsError))
}

func (h *MatchesController) list(c *gin.Context) error {
	query := c.Request.URL.Query()
	limit, err := matchLimit(query)
	if err != nil {
		return err
	}
	before, err := matchCursor(query)
	if err != nil {
		return err
	}
	matches, err := h.reads.MatchHistory(c.Request.Context(), sessionauth.UserID(c), limit, before)
	if err != nil {
		return err
	}
	entries := make([]gin.H, 0, len(matches))
	for _, match := range matches {
		entries = append(entries, matchEntry(match))
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"matches": entries}})

	return nil
}

func matchEntry(match service.MatchSummary) gin.H {
	opponents := make([]gin.H, 0, len(match.Opponents))
	for _, opponent := range match.Opponents {
		opponents = append(opponents, gin.H{"user_id": opponent.UserID, "username": opponent.Username})
	}

	return gin.H{
		"game_id": match.GameID, "room_id": match.RoomID, "team": match.Team, "role": match.Role,
		"opponents": opponents, "result": match.Result, "end_reason": match.EndReason,
		"score":       gin.H{"red": match.ScoreRed, "blue": match.ScoreBlue},
		"finished_at": match.FinishedAt.UTC().Format(time.RFC3339Nano),
	}
}

func matchLimit(query url.Values) (int, error) {
	values, present := query["limit"]
	if !present {
		return defaultMatchLimit, nil
	}
	if len(values) != 1 {
		return 0, invalidField("limit", "limit must be one integer from 1 to 50.")
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit < 1 || limit > maxMatchLimit {
		return 0, invalidField("limit", "limit must be one integer from 1 to 50.")
	}

	return limit, nil
}

func matchCursor(query url.Values) (*time.Time, error) {
	values, present := query["before"]
	if !present {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, invalidField("before", "before must be one ISO 8601 UTC time.")
	}
	before, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil {
		return nil, invalidField("before", "before must be one ISO 8601 UTC time.")
	}

	return &before, nil
}

// InternalStatsController serves private counter reads.
type InternalStatsController struct {
	reads StatsReader
	auth  *serviceauth.Authenticator
}

// NewInternalStats returns the private counters controller.
func NewInternalStats(reads StatsReader, auth *serviceauth.Authenticator) *InternalStatsController {
	return &InternalStatsController{reads: reads, auth: auth}
}

// RegisterRoutes adds GET /internal/v1/user-stats/{user_id}. Register it only
// on the private listener: the response holds owner-only penalty points.
func (h *InternalStatsController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/internal/v1/user-stats/:user_id", h.auth.Require(serviceauth.ScopeStatsRead), router.Wrap(h.read, mapStatsError))
}

func (h *InternalStatsController) read(c *gin.Context) error {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		return invalidField("user_id", "user_id must be a positive integer.")
	}
	stats, err := h.reads.UserStats(c.Request.Context(), userID)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id": stats.UserID, "level": stats.Level, "wins": stats.Wins, "losses": stats.Losses,
		"matches_played": stats.MatchesPlayed, "penalty_points": stats.PenaltyPoints,
	}})

	return nil
}

func invalidField(field, message string) *httpresponse.Error {
	return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR",
		Message: message, Details: map[string]any{"field": field}}
}

func mapStatsError(err error) *httpresponse.Error {
	if errors.Is(err, service.ErrUnavailable) {
		return &httpresponse.Error{Status: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE",
			Message: "The service is temporarily unavailable."}
	}

	return httpresponse.InternalError()
}
