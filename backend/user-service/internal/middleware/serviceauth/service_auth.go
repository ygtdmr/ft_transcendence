// Package serviceauth authenticates private service-to-service callers.
//
// Every private request carries "Authorization: Bearer <service credential>".
// Each credential belongs to exactly one caller, and each caller holds a fixed
// set of operation scopes on this receiver. The caller identity comes only
// from that mapping, never from a header, body field or network address.
package serviceauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
)

// Operation scopes served by this receiver.
const (
	ScopeProfileProvision = "profile.provision"
	ScopeProvisioningRead = "provisioning.read"
	ScopeProfileRead      = "profile.read"
)

// Matrix is the authorization matrix: the scopes each caller may use on this receiver.
type Matrix map[string][]string

// DefaultMatrix returns the receiver's authorization matrix from the private contract.
func DefaultMatrix() Matrix {
	return Matrix{
		"auth-service": {ScopeProfileProvision, ScopeProvisioningRead, ScopeProfileRead},
	}
}

const callerKey = "serviceauth.caller"

type credential struct {
	digest [sha256.Size]byte
	caller string
}

// Authenticator checks service credentials and operation scopes.
type Authenticator struct {
	credentials []credential
	matrix      Matrix
}

// New builds an Authenticator from each caller's accepted credentials.
//
// Parameters:
//
//   - credentials: Caller name to its accepted credentials (at most current and
//     previous). Callers missing from matrix are ignored.
//   - matrix: Scopes each caller may use; normally DefaultMatrix().
//
// An empty credentials map yields an Authenticator that accepts no caller.
func New(credentials map[string][]string, matrix Matrix) *Authenticator {
	auth := &Authenticator{matrix: matrix}
	for caller, values := range credentials {
		if _, known := matrix[caller]; !known {
			continue
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			auth.credentials = append(auth.credentials, credential{digest: sha256.Sum256([]byte(value)), caller: caller})
		}
	}

	return auth
}

// Require returns middleware admitting only callers holding scope.
//
// It answers 401 SERVICE_UNAUTHENTICATED with "WWW-Authenticate: Bearer" for a
// missing, malformed or unknown credential, and 403 SERVICE_FORBIDDEN for a
// known caller without the scope. Comparisons run in constant time against
// every configured credential, and the presented credential is never logged
// or echoed.
func (a *Authenticator) Require(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		caller, ok := a.identify(c.GetHeader("Authorization"))
		if !ok {
			c.Header("WWW-Authenticate", "Bearer")
			router.WriteError(c, &httpresponse.Error{
				Status:  http.StatusUnauthorized,
				Code:    "SERVICE_UNAUTHENTICATED",
				Message: "Service credentials are missing or invalid.",
			})
			return
		}
		if !a.allowed(caller, scope) {
			router.WriteError(c, &httpresponse.Error{
				Status:  http.StatusForbidden,
				Code:    "SERVICE_FORBIDDEN",
				Message: "This service is not allowed to perform this operation.",
			})
			return
		}
		c.Set(callerKey, caller)
		c.Next()
	}
}

// Caller returns the authenticated caller stored by Require, or "".
func Caller(c *gin.Context) string {
	return c.GetString(callerKey)
}

// identify maps an Authorization header value to its caller.
func (a *Authenticator) identify(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	presented := sha256.Sum256([]byte(token))
	caller := ""
	for _, candidate := range a.credentials {
		// No early exit: the time taken does not depend on which credential matched.
		if subtle.ConstantTimeCompare(candidate.digest[:], presented[:]) == 1 {
			caller = candidate.caller
		}
	}

	return caller, caller != ""
}

func (a *Authenticator) allowed(caller, scope string) bool {
	for _, granted := range a.matrix[caller] {
		if granted == scope {
			return true
		}
	}

	return false
}
