package unit_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

const secretValue = "super-secret-signing-key-0123456789"

// holder embeds Secret in exported and unexported fields to prove neither leaks.
type holder struct {
	Public  config.Secret
	private config.Secret
}

func TestSecretNeverPrintsItsValue(t *testing.T) {
	s := config.NewSecret(secretValue)
	h := holder{Public: s, private: s}

	outputs := map[string]string{
		"%s":         fmt.Sprintf("%s", s),
		"%v":         fmt.Sprintf("%v", s),
		"%+v":        fmt.Sprintf("%+v", s),
		"%#v":        fmt.Sprintf("%#v", s),
		"%q":         fmt.Sprintf("%q", s),
		"struct %v":  fmt.Sprintf("%v", h),
		"struct %+v": fmt.Sprintf("%+v", h),
		"struct %#v": fmt.Sprintf("%#v", h),
	}
	for verb, out := range outputs {
		if strings.Contains(out, secretValue) {
			t.Errorf("%s leaked the secret: %s", verb, out)
		}
	}
}

func TestSecretIsRedactedInJSONAndSlog(t *testing.T) {
	s := config.NewSecret(secretValue)

	encoded, err := json.Marshal(struct{ JWT config.Secret }{s})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(encoded, []byte(secretValue)) || !bytes.Contains(encoded, []byte("[REDACTED]")) {
		t.Errorf("JSON = %s, want redacted", encoded)
	}

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("loaded", "jwt", s)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("loaded", "jwt", s)
	if strings.Contains(buf.String(), secretValue) {
		t.Errorf("slog leaked the secret: %s", buf.String())
	}
}

func TestSecretRevealAndIsSet(t *testing.T) {
	if got := config.NewSecret(secretValue).Reveal(); got != secretValue {
		t.Errorf("Reveal = %q", got)
	}
	var unset config.Secret
	if unset.IsSet() || unset.Reveal() != "" || config.NewSecret("").IsSet() {
		t.Error("zero or empty Secret must be unset")
	}
	if !config.NewSecret("x").IsSet() {
		t.Error("non-empty Secret must be set")
	}
}
