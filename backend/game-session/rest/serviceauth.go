package rest

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type ServiceAccessCheck struct {
	serviceTokens map[string]string
	serviceScopes map[string][]string
}

func NewServiceAccessCheck(serviceTokens map[string]string, serviceScopes map[string][]string) *ServiceAccessCheck {
	return &ServiceAccessCheck{
		serviceTokens: serviceTokens,
		serviceScopes: serviceScopes,
	}
}

func (check *ServiceAccessCheck) RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestingService, found := check.findServiceByToken(c.GetHeader("Authorization"))
		if !found {
			writeError(c, http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED", "Service credentials are missing or invalid.", nil)
			c.Abort()
			return
		}
		if !check.hasScope(requestingService, scope) {
			writeError(c, http.StatusForbidden, "SERVICE_FORBIDDEN", "This service is not allowed to perform this operation.", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (check *ServiceAccessCheck) findServiceByToken(authHeader string) (string, bool) {
	scheme, token, found := strings.Cut(authHeader, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	for requestingService, serviceToken := range check.serviceTokens {
		if token == serviceToken {
			return requestingService, true
		}
	}
	return "", false
}

func (check *ServiceAccessCheck) hasScope(requestingService, scope string) bool {
	for _, allowedScope := range check.serviceScopes[requestingService] {
		if allowedScope == scope {
			return true
		}
	}
	return false
}
