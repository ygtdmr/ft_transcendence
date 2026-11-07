package unit_test

import (
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// TestAvatarURLs checks the avatar URL built for each stored avatar kind.
//
// Expected output:
//
//   - Default avatars derive from user_id; presets and uploads use their escaped
//     reference; an empty base gives root-relative URLs.
func TestAvatarURLs(t *testing.T) {
	for _, tt := range []struct {
		base, kind, ref, want string
	}{
		{"https://example.com", service.AvatarDefault, "", "https://example.com/static/avatars/default/42.svg"},
		{"", service.AvatarDefault, "", "/static/avatars/default/42.svg"},
		{"https://example.com", service.AvatarPreset, "cyborg-cat", "https://example.com/avatars/presets/cyborg-cat.png"},
		{"https://example.com", service.AvatarUpload, "42.webp", "https://example.com/static/avatars/uploaded/42.webp"},
		{"", service.AvatarUpload, "../x y", "/static/avatars/uploaded/..%2Fx%20y"},
	} {
		if got := (service.AvatarURLs{Base: tt.base}).URL(tt.kind, tt.ref, 42); got != tt.want {
			t.Errorf("URL(%q, %q, %q) = %q, want %q", tt.base, tt.kind, tt.ref, got, tt.want)
		}
	}
}
