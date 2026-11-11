package rest_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"game-session/rest"
)

func TestServiceAccessCheck(t *testing.T) {
	access := rest.NewServiceAccessCheck(
		map[string]string{"auth-service": "auth-secret"},
		map[string][]string{"auth-service": {"membership.read"}},
	)
	router := gin.New()
	router.GET("/ping", access.RequireScope("membership.read"), func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	// True password -> allowed
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Authorization", "Bearer auth-secret")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "pong" {
		t.Fatalf("right token: got %d %q", w.Code, w.Body.String())
	}

	// None password -> forbidden
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", w.Code)
	}
}
