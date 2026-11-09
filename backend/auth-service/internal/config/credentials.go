package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// minCredentialLength rejects credentials too short to resist guessing.
const minCredentialLength = 32

// callerCredentialFiles lists, per private caller, the variables naming its
// current and previous credential files. Two entries allow a rotation: install
// the new credential, switch the caller, then remove the previous one.
var callerCredentialFiles = map[string][2]string{
	"web-bff":       {"WEB_BFF_CREDENTIAL_FILE", "WEB_BFF_PREVIOUS_CREDENTIAL_FILE"},
	"game-gateway":  {"GAME_GATEWAY_CREDENTIAL_FILE", "GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE"},
	"chat-gateway":  {"CHAT_GATEWAY_CREDENTIAL_FILE", "CHAT_GATEWAY_PREVIOUS_CREDENTIAL_FILE"},
	"user-service":  {"USER_SERVICE_CREDENTIAL_FILE", "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
	"stats-service": {"STATS_SERVICE_CREDENTIAL_FILE", "STATS_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
}

// loadCallerCredentials reads each caller's credential files.
//
// It returns the credentials accepted per caller. A caller whose variables are
// unset is omitted, so no private route accepts it.
//
// Errors:
//
//   - A named file cannot be read or holds a credential shorter than 32 characters.
//   - The same credential is configured for two callers; credentials are per pair.
func loadCallerCredentials() (map[string][]string, error) {
	credentials := map[string][]string{}
	owner := map[string]string{}
	for caller, variables := range callerCredentialFiles {
		for _, variable := range variables {
			path := strings.TrimSpace(os.Getenv(variable))
			if path == "" {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				// The path may be sensitive deployment detail; name only the variable.
				return nil, fmt.Errorf("%s: credential file cannot be read", variable)
			}
			credential := strings.TrimSpace(string(raw))
			if len(credential) < minCredentialLength {
				return nil, fmt.Errorf("%s: credential must be at least %d characters", variable, minCredentialLength)
			}
			if other, taken := owner[credential]; taken && other != caller {
				return nil, errors.New("each private caller needs its own service credential")
			}
			owner[credential] = caller
			credentials[caller] = append(credentials[caller], credential)
		}
	}

	return credentials, nil
}
