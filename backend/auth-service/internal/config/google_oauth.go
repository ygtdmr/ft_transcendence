package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// loadGoogleOAuth reads the backend-only Google sign-in settings.
//
// GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET and PUBLIC_BASE_URL are set together
// or not at all. Without them, Google sign-in redirects to
// /?oauth_error=OAUTH_PROVIDER_ERROR.
//
// Errors:
//
//   - Only some of the three settings are present.
//   - PUBLIC_BASE_URL is not an https:// origin without a path.
func (cfg *Config) loadGoogleOAuth() error {
	cfg.GoogleClientID = strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
	cfg.GoogleClientSecret = strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET"))
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/")

	set := 0
	for _, value := range []string{cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.PublicBaseURL} {
		if value != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil
	case 3:
	default:
		return errors.New("GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET and PUBLIC_BASE_URL must be set together")
	}

	base, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("PUBLIC_BASE_URL must be an https:// origin without a path")
	}

	return nil
}

// GoogleRedirectURI returns the callback URL registered with Google, or "".
func (cfg Config) GoogleRedirectURI() string {
	if cfg.PublicBaseURL == "" {
		return ""
	}

	return cfg.PublicBaseURL + "/api/v1/auth/oauth/google/callback"
}
