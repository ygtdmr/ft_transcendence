package config

import "github.com/melmut-42/ft_transcendence/backend/shared/logging"

// Environment names the deployment tier a service runs in.
type Environment string

// Supported values of APP_ENV.
const (
	Development Environment = "development"
	Production  Environment = "production"
	Test        Environment = "test"
)

// Common holds the settings every backend service reads identically.
type Common struct {
	Service     string
	Environment Environment
	Port        int
	// Logging is ready to pass to logging.New.
	Logging logging.Config
}

// Common reads APP_ENV, LOG_FORMAT, LOG_LEVEL and PORT for service.
//
// Logging defaults follow the environment: development logs readable text at
// debug, production logs JSON at info, and tests log text at error. Explicit
// LOG_FORMAT or LOG_LEVEL values override those defaults.
func (r *Reader) Common(service string, defaultPort int) Common {
	env := Environment(r.Enum("APP_ENV", string(Development),
		string(Development), string(Production), string(Test)))
	format, level := loggingDefaults(env)
	return Common{
		Service:     service,
		Environment: env,
		Port:        r.Port("PORT", defaultPort),
		Logging: logging.Config{
			Format:  r.Enum("LOG_FORMAT", format, "text", "json"),
			Level:   r.Enum("LOG_LEVEL", level, "debug", "info", "warn", "error"),
			Service: service,
		},
	}
}

func loggingDefaults(env Environment) (format, level string) {
	switch env {
	case Production:
		return "json", "info"
	case Test:
		return "text", "error"
	default:
		return "text", "debug"
	}
}
