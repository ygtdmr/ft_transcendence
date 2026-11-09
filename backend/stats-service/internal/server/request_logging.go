package server

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/requestid"
)

// requestLogger records completed requests without query parameters or bodies.
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logging.LogAccess(c.Request.Context(), logger, logging.AccessLog{
			Time:      time.Now(),
			RequestID: requestid.FromContext(c.Request.Context()),
			Status:    c.Writer.Status(),
			Latency:   time.Since(start),
			ClientIP:  c.ClientIP(),
			Method:    c.Request.Method,
			Path:      c.Request.URL.Path,
		})
	}
}
