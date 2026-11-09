package unit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

type avatarStoreStub struct {
	previous service.StoredProfile
	err      error
	calls    int
}

func (s *avatarStoreStub) SetAvatar(context.Context, int64, string, string, service.AvatarEventBuilder) (service.StoredProfile, error) {
	s.calls++
	return s.previous, s.err
}

type avatarFilesStub struct {
	stored    []string
	removed   []string
	storeErr  error
	removeErr error
	content   map[string][]byte
}

func (f *avatarFilesStub) Store(key string, _ []byte) error {
	if f.storeErr != nil {
		return f.storeErr
	}
	f.stored = append(f.stored, key)
	return nil
}

func (f *avatarFilesStub) Remove(key string) error {
	f.removed = append(f.removed, key)
	return f.removeErr
}

func (f *avatarFilesStub) Read(key string) ([]byte, error) {
	if !service.ValidAvatarKey(key) {
		return nil, service.ErrAvatarFileNotFound
	}
	data, found := f.content[key]
	if !found {
		return nil, service.ErrAvatarFileNotFound
	}
	return data, nil
}

func avatarEngine(store *avatarStoreStub, files *avatarFilesStub, cleanup func(string, error)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	avatars := service.NewAvatars(store, files, service.AvatarURLs{}, cleanup)
	controller.NewAvatars(avatars, blockSessions{}).RegisterRoutes(engine)
	controller.NewAvatarFiles(files).RegisterRoutes(engine)
	return engine
}

func avatarCall(engine *gin.Engine, method, target, contentType string, body []byte, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func multipartAvatar(t *testing.T, field string, content []byte) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, "../../etc/passwd.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

// TestAvatarPresetRoutes checks the catalog and preset selection responses.
//
// Expected output:
//
//   - The catalog has 11 entries in the contract's order.
//   - A valid preset answers avatar_url; a replaced upload is removed.
//   - A missing, null, non-string or unknown preset is 422; bad JSON is 400.
//   - A user without a usable profile is 401, an outage 503.
func TestAvatarPresetRoutes(t *testing.T) {
	const own = "/api/v1/users/me/avatar"
	store, files := &avatarStoreStub{}, &avatarFilesStub{}
	engine := avatarEngine(store, files, nil)

	listed := avatarCall(engine, http.MethodGet, "/api/v1/avatars/presets", "", nil, "valid")
	if listed.Code != http.StatusOK || strings.Count(listed.Body.String(), "preset_id") != 11 ||
		!strings.Contains(listed.Body.String(), `{"avatar_url":"/avatars/presets/alien.png","preset_id":"alien"}`) {
		t.Fatalf("catalog = %d %s", listed.Code, listed.Body.String())
	}
	if unauthenticated := avatarCall(engine, http.MethodGet, "/api/v1/avatars/presets", "", nil, ""); unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("catalog without session = %d", unauthenticated.Code)
	}

	store.previous = service.StoredProfile{AvatarKind: service.AvatarUpload, AvatarRef: "old-key"}
	selected := avatarCall(engine, http.MethodPut, own, "application/json", []byte(`{"preset_id":"scholar-owl"}`), "valid")
	if selected.Code != http.StatusOK || selected.Body.String() != `{"data":{"avatar_url":"/avatars/presets/scholar-owl.png"}}` {
		t.Fatalf("select = %d %s", selected.Code, selected.Body.String())
	}
	if len(files.removed) != 1 || files.removed[0] != "old-key" {
		t.Fatalf("removed = %v", files.removed)
	}

	for _, tt := range []struct {
		name, body, code string
		status           int
	}{
		{"missing", `{}`, "INVALID_AVATAR_PRESET", http.StatusUnprocessableEntity},
		{"null", `{"preset_id":null}`, "INVALID_AVATAR_PRESET", http.StatusUnprocessableEntity},
		{"number", `{"preset_id":7}`, "INVALID_AVATAR_PRESET", http.StatusUnprocessableEntity},
		{"unknown", `{"preset_id":"dragon"}`, "INVALID_AVATAR_PRESET", http.StatusUnprocessableEntity},
		{"traversal", `{"preset_id":"../../etc/passwd"}`, "INVALID_AVATAR_PRESET", http.StatusUnprocessableEntity},
		{"malformed", `{`, "VALIDATION_ERROR", http.StatusBadRequest},
		{"two objects", `{}{}`, "VALIDATION_ERROR", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := store.calls
			recorder := avatarCall(engine, http.MethodPut, own, "application/json", []byte(tt.body), "valid")
			if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), tt.code) || store.calls != before {
				t.Fatalf("response = %d %s, store calls %d", recorder.Code, recorder.Body.String(), store.calls-before)
			}
		})
	}

	for _, tt := range []struct {
		err    error
		status int
	}{
		{service.ErrUserNotFound, http.StatusUnauthorized},
		{&service.UserDeletedError{UserID: 42}, http.StatusUnauthorized},
		{fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable},
	} {
		failing := avatarEngine(&avatarStoreStub{err: tt.err}, &avatarFilesStub{}, nil)
		if recorder := avatarCall(failing, http.MethodPut, own, "application/json", []byte(`{"preset_id":"orc"}`), "valid"); recorder.Code != tt.status {
			t.Fatalf("store error %v = %d %s", tt.err, recorder.Code, recorder.Body.String())
		}
	}
}

// TestAvatarUploadRoute checks upload validation and file handling.
//
// Expected output:
//
//   - A valid upload answers a URL under a generated key, never the client's name.
//   - Every bad request is 422 INVALID_IMAGE and stores nothing.
//   - A store refusal removes the new file; an unknown commit keeps it.
//   - A failed file write leaves the profile alone.
//   - A failed removal of the replaced file does not fail the request.
func TestAvatarUploadRoute(t *testing.T) {
	const own = "/api/v1/users/me/avatar"
	image := pngBytes(t, solidImage(64, 64, color.NRGBA{R: 200, A: 255}))
	contentType, body := multipartAvatar(t, "avatar", image)

	store, files := &avatarStoreStub{}, &avatarFilesStub{}
	engine := avatarEngine(store, files, nil)
	uploaded := avatarCall(engine, http.MethodPost, own, contentType, body, "valid")
	if uploaded.Code != http.StatusOK || len(files.stored) != 1 || !service.ValidAvatarKey(files.stored[0]) ||
		uploaded.Body.String() != `{"data":{"avatar_url":"/static/avatars/uploaded/`+files.stored[0]+`"}}` {
		t.Fatalf("upload = %d %s, stored %v", uploaded.Code, uploaded.Body.String(), files.stored)
	}

	if recorder := avatarCall(engine, http.MethodPost, own, contentType, body, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d", recorder.Code)
	}

	wrongField, wrongBody := multipartAvatar(t, "picture", image)
	textType, textBody := multipartAvatar(t, "avatar", []byte("not an image"))
	bigType, bigBody := multipartAvatar(t, "avatar", make([]byte, service.MaxAvatarBytes+1))
	hugeType, hugeBody := multipartAvatar(t, "avatar", make([]byte, 4*service.MaxAvatarBytes))
	for _, tt := range []struct {
		name, contentType string
		body              []byte
	}{
		{"not multipart", "application/json", []byte(`{}`)},
		{"no content type", "", nil},
		{"wrong field", wrongField, wrongBody},
		{"not an image", textType, textBody},
		{"one byte over", bigType, bigBody},
		{"far over", hugeType, hugeBody},
		{"cut off", contentType, body[:len(body)/2]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testStore, testFiles := &avatarStoreStub{}, &avatarFilesStub{}
			recorder := avatarCall(avatarEngine(testStore, testFiles, nil), http.MethodPost, own, tt.contentType, tt.body, "valid")
			if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"INVALID_IMAGE"`) ||
				testStore.calls != 0 || len(testFiles.stored) != 0 {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}

	refusedFiles := &avatarFilesStub{}
	if recorder := avatarCall(avatarEngine(&avatarStoreStub{err: service.ErrUserNotFound}, refusedFiles, nil), http.MethodPost, own, contentType, body, "valid"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("refused = %d", recorder.Code)
	}
	if len(refusedFiles.removed) != 1 || refusedFiles.removed[0] != refusedFiles.stored[0] {
		t.Fatalf("refused upload: stored %v, removed %v", refusedFiles.stored, refusedFiles.removed)
	}

	unknownFiles := &avatarFilesStub{}
	unknown := &avatarStoreStub{err: fmt.Errorf("%w: %w", service.ErrOutcomeUnknown, service.ErrUnavailable)}
	if recorder := avatarCall(avatarEngine(unknown, unknownFiles, nil), http.MethodPost, own, contentType, body, "valid"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown commit = %d", recorder.Code)
	}
	if len(unknownFiles.removed) != 0 {
		t.Fatalf("unknown commit removed %v", unknownFiles.removed)
	}

	writeStore := &avatarStoreStub{}
	if recorder := avatarCall(avatarEngine(writeStore, &avatarFilesStub{storeErr: errors.New("disk full")}, nil), http.MethodPost, own, contentType, body, "valid"); recorder.Code != http.StatusInternalServerError || writeStore.calls != 0 {
		t.Fatalf("failed write = %d, store calls %d", recorder.Code, writeStore.calls)
	}

	var cleanupKey string
	replacedStore := &avatarStoreStub{previous: service.StoredProfile{AvatarKind: service.AvatarUpload, AvatarRef: "old-key"}}
	replacedFiles := &avatarFilesStub{removeErr: errors.New("busy")}
	replaced := avatarCall(avatarEngine(replacedStore, replacedFiles, func(key string, _ error) { cleanupKey = key }), http.MethodPost, own, contentType, body, "valid")
	if replaced.Code != http.StatusOK || cleanupKey != "old-key" {
		t.Fatalf("failed cleanup = %d, reported %q", replaced.Code, cleanupKey)
	}
}

// TestAvatarFileRoutes checks serving images.
//
// Expected output:
//
//   - A stored file is image/png with nosniff, a restrictive policy and no session.
//   - Keys of any other form are 404, including traversal.
//   - A default avatar is a stable SVG; a bad name is 404.
func TestAvatarFileRoutes(t *testing.T) {
	key := "42-" + strings.Repeat("a", 32) + ".png"
	files := &avatarFilesStub{content: map[string][]byte{key: []byte("png-bytes")}}
	engine := avatarEngine(&avatarStoreStub{}, files, nil)

	served := avatarCall(engine, http.MethodGet, "/static/avatars/uploaded/"+key, "", nil, "")
	if served.Code != http.StatusOK || served.Body.String() != "png-bytes" || served.Header().Get("Content-Type") != "image/png" ||
		served.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(served.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("served = %d %v", served.Code, served.Header())
	}

	for _, bad := range []string{"missing-key.png", "..%2f..%2fetc%2fpasswd", "42-" + strings.Repeat("b", 32) + ".png", key + ".html", "%2e%2e"} {
		if recorder := avatarCall(engine, http.MethodGet, "/static/avatars/uploaded/"+bad, "", nil, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("key %q = %d", bad, recorder.Code)
		}
	}

	first := avatarCall(engine, http.MethodGet, "/static/avatars/default/42.svg", "", nil, "")
	second := avatarCall(engine, http.MethodGet, "/static/avatars/default/42.svg", "", nil, "")
	other := avatarCall(engine, http.MethodGet, "/static/avatars/default/43.svg", "", nil, "")
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "image/svg+xml" ||
		first.Header().Get("X-Content-Type-Options") != "nosniff" || first.Body.String() != second.Body.String() ||
		first.Body.String() == other.Body.String() || strings.Contains(first.Body.String(), "script") {
		t.Fatalf("default = %d %s", first.Code, first.Body.String())
	}
	for _, bad := range []string{"42", "0.svg", "-1.svg", "abc.svg", "42.svg.html"} {
		if recorder := avatarCall(engine, http.MethodGet, "/static/avatars/default/"+bad, "", nil, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("default %q = %d", bad, recorder.Code)
		}
	}
}
