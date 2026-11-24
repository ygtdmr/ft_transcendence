package config

import (
	"errors"
	"time"
)

const defaultGameInternalTimeout = 5 * time.Second

// loadGameService reads the Game Session membership settings.
//
// GAME_INTERNAL_URL and GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in
// production and must be set together elsewhere. Without them, development and
// test sessions report no active room.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an http(s) origin, the timeout is invalid, or the
//     credential file is unreadable, too short, reused for an inbound caller
//     or equal to the User Service credential.
func (cfg *Config) loadGameService() error {
	settings, err := cfg.loadOutbound(outboundService{
		urlVar: "GAME_INTERNAL_URL", fileVar: "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		timeoutVar: "GAME_INTERNAL_TIMEOUT", fallbackTimeout: defaultGameInternalTimeout, label: "Game Session",
	})
	if err != nil {
		return err
	}
	if settings.credential != "" && settings.credential == cfg.UserServiceCredential {
		return errors.New("the outbound Game Session credential must differ from the User Service credential")
	}
	cfg.GameInternalURL, cfg.GameServiceCredential, cfg.GameInternalTimeout = settings.url, settings.credential, settings.timeout

	return nil
}
