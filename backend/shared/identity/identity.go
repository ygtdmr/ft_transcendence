// Package identity authenticates the end user of a request and carries who they
// are to downstream layers.
//
// The browser's only session credential is the ft_session cookie. Authenticate
// reads it from that cookie and nowhere else (never a query string, JSON body or
// custom header), asks a SessionValidator, which in production is the Auth
// Service's private session-validation call, and puts the verdict in the request
// context. Client-supplied identity headers (X-User-ID and the like) are never
// consulted.
//
// Authorization is deliberately thin. Whether this user may touch this room,
// channel or profile is the owning service's rule; Authorize only runs that rule
// at the entry layer and maps its refusal to a response.
package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// SessionCookie is the only carrier of the browser access credential.
const SessionCookie = "ft_session"

// maxCredentialBytes bounds the credential handed to the validator.
const maxCredentialBytes = 4096

// Identity is the authenticated user of a request.
type Identity struct {
	UserID    int64
	SessionID string
	ExpiresAt time.Time
}

// Errors a SessionValidator returns. Anything else is treated as an unexpected
// failure.
var (
	// ErrInvalidSession: the credential is missing, malformed, expired or revoked,
	// or the account is not active. The only error that means "not logged in".
	ErrInvalidSession = errors.New("identity: session is not valid")
	// ErrUnavailable: the verdict could not be obtained (Auth Service or its
	// database down, timeout, unreadable answer). It is an availability failure,
	// never a logout: the browser must keep its cookie.
	ErrUnavailable = errors.New("identity: session verdict unavailable")
	// ErrServiceRejected: Auth refused this service's own credential. A
	// deployment configuration fault; fail closed, never shown as a logout.
	ErrServiceRejected = errors.New("identity: service credential rejected by the session verifier")
)

// SessionValidator turns the browser's ft_session credential into an Identity.
type SessionValidator interface {
	Validate(ctx context.Context, sessionCredential string) (Identity, error)
}

type contextKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the authenticated identity, or false if the request was
// not authenticated.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// Options configures Authenticate and Authorize.
type Options struct {
	// Logger receives security-relevant faults (never credentials). May be nil.
	Logger *slog.Logger
}

// Authenticate rejects requests without a valid session and otherwise makes the
// Identity available through FromContext.
//
//   - 401 UNAUTHORIZED: no ft_session cookie, more than one, an implausible
//     value, or the validator answers ErrInvalidSession;
//   - 503 SERVICE_UNAVAILABLE: the verdict was unavailable (ErrUnavailable);
//   - 500 INTERNAL_ERROR: a configuration fault (ErrServiceRejected) or any
//     unexpected validator failure, logged by kind only.
//
// The credential is never logged or echoed.
func Authenticate(v SessionValidator, opts Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			credential, ok := sessionCredential(r)
			if !ok {
				writeUnauthorized(w)
				return
			}
			id, err := v.Validate(r.Context(), credential)
			switch {
			case err == nil:
				if id.UserID <= 0 || id.SessionID == "" {
					log(opts, r, "session_validator_returned_invalid_identity")
					httpresponse.WriteError(w, httpresponse.InternalError())
					return
				}
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
			case errors.Is(err, ErrInvalidSession):
				writeUnauthorized(w)
			case errors.Is(err, ErrUnavailable):
				httpresponse.WriteError(w, &httpresponse.Error{
					Status:  http.StatusServiceUnavailable,
					Code:    "SERVICE_UNAVAILABLE",
					Message: "The session could not be verified right now.",
				})
			case errors.Is(err, ErrServiceRejected):
				log(opts, r, "session_verifier_rejected_service_credential")
				httpresponse.WriteError(w, httpresponse.InternalError())
			default:
				log(opts, r, "session_validation_failed_unexpectedly")
				httpresponse.WriteError(w, httpresponse.InternalError())
			}
		})
	}
}

// sessionCredential extracts the single ft_session cookie. Zero cookies or more
// than one is ambiguous and refused: which one a parser picks must not matter.
func sessionCredential(r *http.Request) (string, bool) {
	var value string
	count := 0
	for _, c := range r.Cookies() {
		if c.Name == SessionCookie {
			value = c.Value
			count++
		}
	}
	if count != 1 || value == "" || len(value) > maxCredentialBytes {
		return "", false
	}
	return value, true
}

func writeUnauthorized(w http.ResponseWriter) {
	httpresponse.WriteError(w, &httpresponse.Error{
		Status:  http.StatusUnauthorized,
		Code:    "UNAUTHORIZED",
		Message: "The session is missing, expired or revoked.",
	})
}

func log(opts Options, r *http.Request, msg string) {
	if opts.Logger != nil {
		opts.Logger.ErrorContext(r.Context(), msg)
	}
}

// Authorize runs check for the authenticated user after Authenticate. If check
// returns an error it is mapped with m (see httpresponse.Mapper): typically a
// 403 with the owning service's domain code. A request that reaches Authorize
// without an identity is a wiring mistake and fails closed with 401.
//
// check holds the service's resource rule ("is this user a member of this
// room"); it does not belong in shared code.
func Authorize(check func(r *http.Request, id Identity) error, m *httpresponse.Mapper, opts Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := FromContext(r.Context())
			if !ok {
				writeUnauthorized(w)
				return
			}
			if err := check(r, id); err != nil {
				httpresponse.WriteError(w, httpresponse.ResolveAndLog(r.Context(), err, m, opts.Logger))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
