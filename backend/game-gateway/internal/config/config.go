// Package config reads the Game Gateway's settings from the environment.
// Every problem is collected and reported at once, by variable name only: no
// value, path or secret ever appears in an error.
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
const ServiceName = "game-gateway"

// Callers of this gateway's private routes, as the authorization matrix
// spells them.
const (
	CallerUserService = "user-service"
	CallerAuthService = "auth-service"
)

// Scopes this receiver grants.
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

var callerFiles = map[string]serviceauth.CallerFiles{
	CallerUserService: {Current: "USER_SERVICE_CREDENTIAL_FILE", Previous: "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
	CallerAuthService: {Current: "AUTH_SERVICE_CREDENTIAL_FILE", Previous: "AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
}

// Config is the validated gateway configuration.
type Config struct {
	sharedconfig.Common

	// InternalAddr is the private listener address, INTERNAL_HOST:INTERNAL_PORT.
	// An empty INTERNAL_HOST (the default) listens on every interface, for
	// local runs; a deployment sets the private-network address.
	InternalAddr string
	// PublicOrigin is the single public origin a handshake must come from
	// (PUBLIC_BASE_URL, the name Auth uses for the same origin).
	PublicOrigin string
	// Auth is the Auth Service client for session validation.
	Auth privateclient.Config
	// Game is the Game Session client for the room.socket calls.
	Game privateclient.Config
	// Callers holds each private caller's accepted credentials.
	Callers map[string][]string
	// Lifecycle bounds shutdown (SHUTDOWN_*).
	Lifecycle lifecycle.Options
	// Socket bounds every room socket.
	Socket SocketConfig
}

// SocketConfig bounds every room socket.
type SocketConfig struct {
	// SendQueue is how many messages may wait for one socket before it is
	// closed as a slow client (SOCKET_SEND_QUEUE, default 64).
	SendQueue int
	// PingInterval is how often the gateway pings (SOCKET_PING_INTERVAL,
	// default 25s).
	PingInterval time.Duration
	// PongWait is how long a silent connection lives (SOCKET_PONG_WAIT,
	// default 60s, more than PingInterval).
	PongWait time.Duration
}

// Load reads and validates the configuration through lookup (os.LookupEnv in
// production).
func Load(lookup sharedconfig.LookupFunc) (Config, error) {
	r := sharedconfig.NewReader(lookup)
	cfg := Config{Common: r.Common(ServiceName, 8080)}

	host := strings.TrimSpace(r.String("INTERNAL_HOST", ""))
	if strings.ContainsAny(host, " /:@") && net.ParseIP(host) == nil {
		r.Problem("INTERNAL_HOST", "must be a host name or IP address without a port")
	}
	cfg.InternalAddr = net.JoinHostPort(host, strconv.Itoa(r.Port("INTERNAL_PORT", 8081)))

	cfg.PublicOrigin = r.Required("PUBLIC_BASE_URL")
	if cfg.PublicOrigin != "" {
		if _, err := originguard.New(cfg.PublicOrigin); err != nil {
			r.Problem("PUBLIC_BASE_URL", "must be the public origin, such as https://host[:port], with no path")
		}
	}

	cfg.Auth = privateclient.ConfigFromEnv(r, "AUTH_INTERNAL_URL", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		"AUTH_INTERNAL_TIMEOUT")
	cfg.Game = privateclient.ConfigFromEnv(r, "GAME_INTERNAL_URL", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		"GAME_INTERNAL_TIMEOUT")
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
// gateway still starts (it refuses them) and logs a warning.
func (c Config) MissingCallers() []string {
	var missing []string
	for _, caller := range []string{CallerUserService, CallerAuthService} {
		if len(c.Callers[caller]) == 0 {
			missing = append(missing, caller)
		}
	}

	return missing
}
