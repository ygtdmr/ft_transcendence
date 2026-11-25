// Package config reads the Notification Gateway's settings from the
// environment. Every problem is collected and reported at once, by variable
// name only: no value, path or secret ever appears in an error.
package config

import (
	"net"
	"strconv"
	"strings"
	"time"

	sharedconfig "github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

// ServiceName is the fixed service attribute of every log record.
const ServiceName = "notification-gateway"

// Caller names, as the private authorization matrix spells them.
const (
	CallerUserService = "user-service"
	CallerAuthService = "auth-service"
)

// Scopes this receiver grants (authorization matrix rows connections.read and
// session.revocation.notify).
const (
	ScopeConnectionsRead         = "connections.read"
	ScopeSessionRevocationNotify = "session.revocation.notify"
)

// Matrix lists the operations each caller may use on this gateway.
func Matrix() serviceauth.Matrix {
	return serviceauth.Matrix{
		CallerUserService: {ScopeConnectionsRead},
		CallerAuthService: {ScopeSessionRevocationNotify},
	}
}

// callerFiles names the credential files of each caller this gateway accepts.
var callerFiles = map[string]serviceauth.CallerFiles{
	CallerUserService: {Current: "USER_SERVICE_CREDENTIAL_FILE", Previous: "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
	CallerAuthService: {Current: "AUTH_SERVICE_CREDENTIAL_FILE", Previous: "AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
}

// Config is the validated gateway configuration.
type Config struct {
	sharedconfig.Common

	// InternalAddr is the private listener address, INTERNAL_HOST:INTERNAL_PORT.
	// INTERNAL_HOST empty (the default) listens on every interface, for local
	// runs; a deployment sets it to the private-network address so the private
	// listener is not reachable from the reverse proxy's network.
	InternalAddr string
	// PublicOrigin is the single public origin a handshake must come from.
	PublicOrigin string
	// Auth is the Auth Service client for session validation.
	Auth privateclient.Config
	// Callers holds each private caller's accepted credentials.
	Callers map[string][]string
	// Lifecycle bounds shutdown (SHUTDOWN_TIMEOUT, SHUTDOWN_DRAIN_DELAY,
	// SHUTDOWN_CLOSE_TIMEOUT).
	Lifecycle lifecycle.Options
	// Socket bounds every notification socket.
	Socket SocketConfig
}

// SocketConfig bounds every notification socket.
type SocketConfig struct {
	// SendQueue is how many events may wait for one socket before it is closed
	// as a slow client (SOCKET_SEND_QUEUE, default 64).
	SendQueue int
	// PingInterval is how often the gateway pings (SOCKET_PING_INTERVAL,
	// default 25s).
	PingInterval time.Duration
	// PongWait is how long a silent connection lives without a pong
	// (SOCKET_PONG_WAIT, default 60s, more than PingInterval).
	PongWait time.Duration
}

// Load reads and validates the configuration through lookup (os.LookupEnv in
// production).
func Load(lookup sharedconfig.LookupFunc) (Config, error) {
	r := sharedconfig.NewReader(lookup)
	cfg := Config{Common: r.Common(ServiceName, 8080)}
	cfg.Logging.Environment = string(cfg.Environment)

	host := strings.TrimSpace(r.String("INTERNAL_HOST", ""))
	if strings.ContainsAny(host, " /:@") && net.ParseIP(host) == nil {
		r.Problem("INTERNAL_HOST", "must be a host name or IP address without a port")
	}
	cfg.InternalAddr = net.JoinHostPort(host, strconv.Itoa(r.Port("INTERNAL_PORT", 8081)))

	cfg.PublicOrigin = r.Required("PUBLIC_ORIGIN")
	if cfg.PublicOrigin != "" {
		if _, err := originguard.New(cfg.PublicOrigin); err != nil {
			r.Problem("PUBLIC_ORIGIN", "must be the public origin, such as https://host[:port], with no path")
		}
	}

	cfg.Auth = privateclient.ConfigFromEnv(r, "AUTH_INTERNAL_URL", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		"AUTH_INTERNAL_TIMEOUT")
	cfg.Callers = serviceauth.LoadCredentials(r, callerFiles)
	cfg.Lifecycle = lifecycle.OptionsFromEnv(r)

	cfg.Socket = SocketConfig{
		SendQueue:    r.Int("SOCKET_SEND_QUEUE", 64, 1, 4096),
		PingInterval: r.Duration("SOCKET_PING_INTERVAL", 25*time.Second, time.Second, 5*time.Minute),
		PongWait:     r.Duration("SOCKET_PONG_WAIT", 60*time.Second, 2*time.Second, 10*time.Minute),
	}
	if cfg.Socket.PongWait <= cfg.Socket.PingInterval {
		r.Problem("SOCKET_PONG_WAIT", "must be longer than SOCKET_PING_INTERVAL")
	}

	if err := r.Err(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// MissingCallers returns the callers without a configured credential. The
// gateway still starts (it fails closed for them) and logs a warning.
func (c Config) MissingCallers() []string {
	var missing []string
	for _, caller := range []string{CallerUserService, CallerAuthService} {
		if len(c.Callers[caller]) == 0 {
			missing = append(missing, caller)
		}
	}

	return missing
}
