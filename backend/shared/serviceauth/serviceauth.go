// Package serviceauth authenticates private service-to-service callers, as the
// private service REST contract defines it.
//
// Every private request carries exactly one "Authorization: Bearer <service
// credential>". Each credential belongs to one caller/receiver pair and maps to
// exactly one caller; each caller holds a fixed set of operation scopes on this
// receiver (the Matrix). The caller identity comes only from that mapping, never
// from a header, body field or network address.
//
// The receiver fails closed: with no credentials configured it accepts no
// caller. Credentials are compared in constant time, are read from mounted
// secret files, and are never logged, returned or placed in URLs. A receiver
// accepts at most two credentials per caller (current and previous) so a
// credential can be rotated: install the new one here, switch the caller, then
// remove the old one.
//
// This package authenticates the calling service only. It never replaces the end
// user's session or the receiver's own resource authorization; those stay with
// the identity package and the owning service.
package serviceauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// MinCredentialBytes is the shortest accepted service credential (the contract
// asks for 32+ characters, e.g. `openssl rand -hex 32`).
const MinCredentialBytes = 32

// Matrix lists, per caller, the operation scopes it may use on this receiver.
type Matrix map[string][]string

type contextKey struct{}

type credential struct {
	digest [sha256.Size]byte
	caller string
}

// Authenticator checks service credentials and operation scopes. The zero value
// accepts no caller.
type Authenticator struct {
	credentials []credential
	matrix      Matrix
}

// New builds an Authenticator from each caller's accepted credentials (at most
// current and previous). Callers that are not in matrix are ignored, so a stray
// credential cannot create an identity the receiver has no rules for. Empty
// credentials are skipped. No credentials means no caller is accepted.
func New(credentials map[string][]string, matrix Matrix) *Authenticator {
	a := &Authenticator{matrix: matrix}
	for caller, values := range credentials {
		if _, known := matrix[caller]; !known {
			continue
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			a.credentials = append(a.credentials, credential{digest: sha256.Sum256([]byte(value)), caller: caller})
		}
	}
	return a
}

// Identify maps an Authorization header value to its caller. It accepts only the
// "Bearer <token>" form. The comparison runs against every configured credential
// with no early exit, so timing does not reveal which one matched.
func (a *Authenticator) Identify(authorization string) (caller string, ok bool) {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	presented := sha256.Sum256([]byte(token))
	for _, candidate := range a.credentials {
		if subtle.ConstantTimeCompare(candidate.digest[:], presented[:]) == 1 {
			caller = candidate.caller
		}
	}
	return caller, caller != ""
}

// Allowed reports whether caller holds scope on this receiver.
func (a *Authenticator) Allowed(caller, scope string) bool {
	for _, granted := range a.matrix[caller] {
		if granted == scope {
			return true
		}
	}
	return false
}

// Require returns middleware admitting only callers that hold scope.
//
//   - 401 SERVICE_UNAUTHENTICATED with "WWW-Authenticate: Bearer": the
//     Authorization header is missing, repeated, malformed or unknown;
//   - 403 SERVICE_FORBIDDEN: a known caller without the scope.
//
// Both have empty details, never echo the presented credential, and are distinct
// from user-level UNAUTHORIZED and domain 403 codes. On success the caller name
// is available through Caller.
func (a *Authenticator) Require(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers := r.Header.Values("Authorization")
			if len(headers) != 1 {
				unauthenticated(w)
				return
			}
			caller, ok := a.Identify(headers[0])
			if !ok {
				unauthenticated(w)
				return
			}
			if !a.Allowed(caller, scope) {
				httpresponse.WriteError(w, &httpresponse.Error{
					Status:  http.StatusForbidden,
					Code:    "SERVICE_FORBIDDEN",
					Message: "This service is not allowed to perform this operation.",
				})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, caller)))
		})
	}
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpresponse.WriteError(w, &httpresponse.Error{
		Status:  http.StatusUnauthorized,
		Code:    "SERVICE_UNAUTHENTICATED",
		Message: "Service credentials are missing or invalid.",
	})
}

// Caller returns the authenticated caller stored by Require, or "".
func Caller(ctx context.Context) string {
	caller, _ := ctx.Value(contextKey{}).(string)
	return caller
}

// CallerFiles names the environment variables holding one caller's credential
// files: the current one and, during a rotation, the previous one.
type CallerFiles struct {
	Current  string // e.g. "AUTH_SERVICE_CREDENTIAL_FILE"
	Previous string // e.g. "AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE"
}

// LoadCredentials reads each caller's credential files through r and returns the
// credentials to hand to New. A caller whose variables are unset is omitted, so
// the receiver refuses it (fail closed). Problems are recorded on r, naming
// variables only, never paths or values:
//
//   - a file that is unreadable or shorter than MinCredentialBytes;
//   - a previous credential without a current one, or equal to it;
//   - one credential configured for two callers (credentials are per pair).
func LoadCredentials(r *config.Reader, callers map[string]CallerFiles) map[string][]string {
	out := map[string][]string{}
	owner := map[string]string{}
	for caller, files := range callers {
		current := r.OptionalSecretFile(files.Current, MinCredentialBytes)
		previous := r.OptionalSecretFile(files.Previous, MinCredentialBytes)
		if !current.IsSet() && previous.IsSet() {
			r.Problem(files.Previous, "requires "+files.Current+" to be set")
			continue
		}
		if current.IsSet() && previous.IsSet() && current.Reveal() == previous.Reveal() {
			r.Problem(files.Previous, "must differ from "+files.Current)
			continue
		}
		for _, secret := range []config.Secret{current, previous} {
			if !secret.IsSet() {
				continue
			}
			if other, taken := owner[secret.Reveal()]; taken && other != caller {
				r.Problem(files.Current, "each private caller needs its own service credential")
				continue
			}
			owner[secret.Reveal()] = caller
			out[caller] = append(out[caller], secret.Reveal())
		}
	}
	return out
}
