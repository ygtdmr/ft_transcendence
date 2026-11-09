package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/avatarfiles"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const avatarPath = "/api/v1/users/me/avatar"

func avatarHandler(t *testing.T, pool *pgxpool.Pool, dir string) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	files, err := avatarfiles.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	urls := service.AvatarURLs{}
	avatars := service.NewAvatars(userpostgres.NewAvatars(pool), files, urls, nil)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger,
		controller.NewAvatars(avatars, fakeSessions{}),
		controller.NewAvatarFiles(files),
		controller.NewFriendRequests(service.NewFriendRequests(userpostgres.NewFriendRequests(pool), urls, service.NoPresence{}), fakeSessions{}),
		controller.NewBlocks(service.NewBlocks(userpostgres.NewBlocks(pool), urls), fakeSessions{}),
		controller.NewFriends(service.NewFriends(userpostgres.NewFriends(pool), urls, service.NoPresence{}), fakeSessions{}),
	).Handler
}

func avatarImage(t *testing.T, shade uint8) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			canvas.Set(x, y, color.NRGBA{R: shade, G: 80, B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func uploadAvatar(t *testing.T, handler http.Handler, user int64, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("avatar", "photo.png")
	if err != nil {
		t.Error(err)
	}
	_, _ = part.Write(content)
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, avatarPath, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: "ft_session", Value: session(user)})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func mustUpload(t *testing.T, handler http.Handler, user int64, shade uint8) string {
	t.Helper()
	recorder := uploadAvatar(t, handler, user, avatarImage(t, shade))
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data struct {
			AvatarURL string `json:"avatar_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data.AvatarURL
}

func selectPreset(handler http.Handler, user int64, preset string) friendResponse {
	return friendCall(handler, http.MethodPut, avatarPath, fmt.Sprintf(`{"preset_id":%q}`, preset), session(user))
}

func storedAvatarFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func storedAvatar(t *testing.T, pool *pgxpool.Pool, user int64) (string, string) {
	t.Helper()
	var kind, ref string
	if err := pool.QueryRow(context.Background(), `SELECT avatar_kind, COALESCE(avatar_ref, '') FROM profiles WHERE user_id = $1`, user).Scan(&kind, &ref); err != nil {
		t.Fatal(err)
	}
	return kind, ref
}

func profileEvents(t *testing.T, pool *pgxpool.Pool) []map[string]any {
	t.Helper()
	return outboxPayloads(t, pool, service.EventUserProfileUpdated)
}

// TestAvatarLifecyclePostgres follows one user through every avatar change.
//
// Expected output:
//
//   - An upload stores one PNG, updates the profile, is served by its URL and
//     records one event with the socket fields.
//   - A later upload replaces the file; a preset removes the last file.
//   - Choosing the current preset changes nothing and records nothing.
func TestAvatarLifecyclePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1)
	dir := t.TempDir()
	handler := avatarHandler(t, pool, dir)

	first := mustUpload(t, handler, 1, 10)
	firstKey := filepath.Base(first)
	if files := storedAvatarFiles(t, dir); len(files) != 1 || files[0] != firstKey {
		t.Fatalf("files = %v, want %s", files, firstKey)
	}
	if kind, ref := storedAvatar(t, pool, 1); kind != service.AvatarUpload || ref != firstKey {
		t.Fatalf("profile = %s %s", kind, ref)
	}
	events := profileEvents(t, pool)
	if len(events) != 1 || events[0]["user_id"] != float64(1) || events[0]["username"] != "player_1" || events[0]["avatar_url"] != first ||
		payloadKeys(events[0]) != "avatar_url,recipient_user_ids,user_id,username" {
		t.Fatalf("events = %v", events)
	}
	served := httptest.NewRecorder()
	handler.ServeHTTP(served, httptest.NewRequest(http.MethodGet, first, nil))
	if decoded, err := png.Decode(bytes.NewReader(served.Body.Bytes())); served.Code != http.StatusOK || err != nil || decoded.Bounds().Dx() != 256 {
		t.Fatalf("served = %d, decode %v", served.Code, err)
	}

	second := mustUpload(t, handler, 1, 20)
	secondKey := filepath.Base(second)
	if files := storedAvatarFiles(t, dir); len(files) != 1 || files[0] != secondKey || secondKey == firstKey {
		t.Fatalf("after replace = %v", files)
	}

	if response := selectPreset(handler, 1, "orc"); response.status != http.StatusOK {
		t.Fatalf("preset = %d %s", response.status, response.raw)
	}
	if files := storedAvatarFiles(t, dir); len(files) != 0 {
		t.Fatalf("after preset = %v", files)
	}
	if kind, ref := storedAvatar(t, pool, 1); kind != service.AvatarPreset || ref != "orc" {
		t.Fatalf("profile = %s %s", kind, ref)
	}
	before := len(profileEvents(t, pool))
	if response := selectPreset(handler, 1, "orc"); response.status != http.StatusOK {
		t.Fatalf("same preset = %d", response.status)
	}
	if got := len(profileEvents(t, pool)); got != before || before != 3 {
		t.Fatalf("events = %d, before %d, want 3", got, before)
	}
}

// TestAvatarVisibleAndRecipientsPostgres checks who learns about a change.
//
// Expected output:
//
//   - Recipients are the user, active friends and pending request peers, sorted.
//   - A blocked friend and a stranger are not recipients.
//   - The friend list shows the new avatar.
func TestAvatarVisibleAndRecipientsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6)
	handler := avatarHandler(t, pool, t.TempDir())
	befriend(t, handler, 1, 2)
	mustSend(t, handler, 3, 1)
	mustSend(t, handler, 1, 4)
	befriend(t, handler, 1, 5)
	mustBlock(t, handler, 5, 1)

	url := mustUpload(t, handler, 1, 30)
	events := profileEvents(t, pool)
	recipients, _ := json.Marshal(events[0]["recipient_user_ids"])
	if string(recipients) != "[1,2,3,4]" {
		t.Fatalf("recipients = %s", recipients)
	}
	if list := listFriends(t, handler, 2, ""); len(list.Friends) != 1 || list.Friends[0].AvatarURL != url {
		t.Fatalf("friend list = %+v", list)
	}
}

// TestAvatarRefusalLeavesNothingPostgres checks a refused change.
//
// Expected output:
//
//   - A user being deleted or without a profile is 401, with no file and no event.
//   - A failed event rolls the profile back.
func TestAvatarRefusalLeavesNothingPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	dir := t.TempDir()
	handler := avatarHandler(t, pool, dir)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 1)`)

	for _, user := range []int64{1, 404} {
		if recorder := uploadAvatar(t, handler, user, avatarImage(t, 40)); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("upload by %d = %d %s", user, recorder.Code, recorder.Body)
		}
		if response := selectPreset(handler, user, "orc"); response.status != http.StatusUnauthorized {
			t.Fatalf("preset by %d = %d", user, response.status)
		}
	}
	if files := storedAvatarFiles(t, dir); len(files) != 0 {
		t.Fatalf("files = %v", files)
	}
	if got := len(profileEvents(t, pool)); got != 0 {
		t.Fatalf("events = %d", got)
	}

	bad := func(service.StoredProfile, []int64) service.OutboxEvent {
		return service.OutboxEvent{AggregateType: "profile", AggregateID: "2", EventType: "not.in.catalog", Payload: map[string]any{}}
	}
	if _, err := userpostgres.NewAvatars(pool).SetAvatar(context.Background(), 2, service.AvatarPreset, "orc", bad); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("SetAvatar = %v", err)
	}
	if kind, ref := storedAvatar(t, pool, 2); kind != service.AvatarDefault || ref != "" {
		t.Fatalf("after rollback = %s %q", kind, ref)
	}
}

// TestAvatarConcurrentChangesPostgres checks racing changes for one user.
//
// Expected output: every request succeeds, one event per change is recorded,
// and only the profile's final file remains, or none when a preset won.
func TestAvatarConcurrentChangesPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1)
	dir := t.TempDir()
	handler := avatarHandler(t, pool, dir)

	for round := range 5 {
		before := len(profileEvents(t, pool))
		var changes []func() int
		for n := range 8 {
			changes = append(changes, func() int { return uploadAvatar(t, handler, 1, avatarImage(t, uint8(round*10+n))).Code })
		}
		changes = append(changes, func() int { return selectPreset(handler, 1, "punk").status })
		for _, status := range runTogether(changes...) {
			if status != http.StatusOK {
				t.Fatalf("round %d status = %d", round, status)
			}
		}
		if got := len(profileEvents(t, pool)); got < before+8 || got > before+9 {
			t.Fatalf("round %d events = %d, before %d", round, got, before)
		}
		kind, ref := storedAvatar(t, pool, 1)
		files := storedAvatarFiles(t, dir)
		switch {
		case kind == service.AvatarUpload && (len(files) != 1 || files[0] != ref):
			t.Fatalf("round %d: profile %s, files %v", round, ref, files)
		case kind == service.AvatarPreset && len(files) != 0:
			t.Fatalf("round %d: preset won, files %v", round, files)
		}
		for _, name := range files {
			if strings.HasPrefix(name, ".upload-") {
				t.Fatalf("temporary file left: %s", name)
			}
		}
	}
}

// TestAvatarFilesConfinedToDirectory checks the store with hostile keys.
//
// Expected output: no key can write, read or remove outside the directory.
func TestAvatarFilesConfinedToDirectory(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := avatarfiles.Open(filepath.Join(parent, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()

	for _, key := range []string{"../outside.png", "/etc/passwd", "1-.png", ""} {
		if err := files.Store(key, []byte("x")); err == nil {
			t.Fatalf("Store(%q) succeeded", key)
		}
		if err := files.Remove(key); err == nil {
			t.Fatalf("Remove(%q) succeeded", key)
		}
		if _, err := files.Read(key); !errors.Is(err, service.ErrAvatarFileNotFound) {
			t.Fatalf("Read(%q) = %v", key, err)
		}
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "secret" {
		t.Fatalf("outside file = %q, %v", content, err)
	}
}
