package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateRoomBodySize(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		name     string
		body     string
		accepted bool
	}{
		{"empty", "", true},
		{"null", "null", true},
		{"at limit", "{}" + strings.Repeat(" ", limit-2), true},
		// The first 64 KiB alone is valid JSON; trailing bytes must not be ignored.
		{"over limit", "{}" + strings.Repeat(" ", limit-2) + "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v2/rooms", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			_, ok := readCreateRoomRequest(w, r)
			if ok != tc.accepted {
				t.Fatalf("accepted = %v, want %v", ok, tc.accepted)
			}
			if !ok && w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", w.Code)
			}
		})
	}
}

func TestCreateRoomJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`not json`, http.StatusBadRequest},
		{`{"max_players":`, http.StatusBadRequest},
		{`{} {}`, http.StatusBadRequest},
		{`{"max_players":"four"}`, http.StatusUnprocessableEntity},
		{`[]`, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.body, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v2/rooms", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			_, ok := readCreateRoomRequest(w, r)
			if ok || w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"VALIDATION_ERROR"`) {
				t.Fatalf("accepted=%v status=%d body=%s", ok, w.Code, w.Body.String())
			}
		})
	}
}
