// Package config reads the Web BFF's settings from the environment. Every
// problem is collected and reported at once, by variable name only: no value,
// path or secret ever appears in an error.
package config

import (
	"net/url"
	"time"

	sharedconfig "github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
)

// ServiceName is the fixed service attribute of every log record.
const ServiceName = "web-bff"

// Config is the validated Web BFF configuration.
type Config struct {
	sharedconfig.Common

	// PublicOrigin is the single public origin cookie-authenticated writes must
	// come from (PUBLIC_BASE_URL).
	PublicOrigin string
	// Auth is the Auth Service client for session validation (scope
	// session.validate).
	Auth privateclient.Config
	// Upstreams are the public listeners of the services the BFF forwards to.
	Upstreams Upstreams
	// UpstreamTimeout bounds how long the BFF waits for a service's response
	// headers (UPSTREAM_TIMEOUT, default 10s).
	UpstreamTimeout time.Duration
	// Lifecycle bounds shutdown (SHUTDOWN_TIMEOUT, SHUTDOWN_DRAIN_DELAY,
	// SHUTDOWN_CLOSE_TIMEOUT).
	Lifecycle lifecycle.Options
}

// Upstreams holds the public listener origin of each service.
type Upstreams struct {
	Auth  *url.URL
	User  *url.URL
	Stats *url.URL
	Game  *url.URL
}

// Load reads and validates the configuration through lookup (os.LookupEnv in
// production).
func Load(lookup sharedconfig.LookupFunc) (Config, error) {
	r := sharedconfig.NewReader(lookup)
	cfg := Config{Common: r.Common(ServiceName, 8080)}
	cfg.Logging.Environment = string(cfg.Environment)

	cfg.PublicOrigin = r.Required("PUBLIC_BASE_URL")
	if cfg.PublicOrigin != "" {
		if _, err := originguard.New(cfg.PublicOrigin); err != nil {
			r.Problem("PUBLIC_BASE_URL", "must be the public origin, such as https://host[:port], with no path")
		}
	}

	cfg.Auth = privateclient.ConfigFromEnv(r, "AUTH_INTERNAL_URL", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		"AUTH_INTERNAL_TIMEOUT")
	cfg.Upstreams = Upstreams{
		Auth:  upstream(r, "AUTH_PUBLIC_URL"),
		User:  upstream(r, "USER_PUBLIC_URL"),
		Stats: upstream(r, "STATS_PUBLIC_URL"),
		Game:  upstream(r, "GAME_PUBLIC_URL"),
	}
	cfg.UpstreamTimeout = r.Duration("UPSTREAM_TIMEOUT", 10*time.Second, time.Second, time.Minute)
	cfg.Lifecycle = lifecycle.OptionsFromEnv(r)

	if err := r.Err(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// upstream reads a required service origin: http or https, a host, and no
// path, query, fragment or credentials, so the browser path is forwarded as is.
func upstream(r *sharedconfig.Reader, name string) *url.URL {
	raw := r.URL(name, "http", "https")
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		r.Problem(name, "must be an origin such as http://host:port, with no path, query or credentials")
		return nil
	}
	parsed.Path = ""

	return parsed
}
