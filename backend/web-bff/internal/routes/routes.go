// Package routes is the Web BFF's explicit public route table. Every browser
// REST route the BFF serves is listed here with its owning service and its
// access rule; anything else under /api is answered 404 NOT_FOUND by the BFF
// itself and never reaches a service.
package routes

import "net/http"

// Upstream names the service a route is forwarded to.
type Upstream string

// The services behind the BFF.
const (
	Auth  Upstream = "auth-service"
	User  Upstream = "user-service"
	Stats Upstream = "stats-service"
	Game  Upstream = "game-session"
)

// Access is the rule the BFF applies before forwarding.
type Access int

const (
	// Public routes need no session: registration, login and Google sign-in.
	Public Access = iota
	// RefreshCookie routes authenticate with the ft_refresh cookie, which only
	// Auth can verify. The BFF applies the same-origin check and forwards.
	RefreshCookie
	// Session routes require a valid ft_session cookie, verified through Auth
	// before the request is forwarded. Writes are also checked for origin.
	Session
)

// Route is one public REST route.
type Route struct {
	Method   string
	Path     string
	Upstream Upstream
	Access   Access
}

// Table returns every forwarded route.
//
// Room routes exist only in REST v2. /api/v1/rooms is not routed while the
// support of the legacy room API is undecided.
func Table() []Route {
	return []Route{
		// Auth Service: /api/v1/auth/*.
		{http.MethodPost, "/api/v1/auth/register", Auth, Public},
		{http.MethodPost, "/api/v1/auth/login", Auth, Public},
		{http.MethodPost, "/api/v1/auth/refresh", Auth, RefreshCookie},
		{http.MethodGet, "/api/v1/auth/oauth/google/authorize", Auth, Public},
		{http.MethodGet, "/api/v1/auth/oauth/google/callback", Auth, Public},
		{http.MethodGet, "/api/v1/auth/session", Auth, Session},
		{http.MethodDelete, "/api/v1/auth/session", Auth, Session},

		// Stats: the owner's match history.
		{http.MethodGet, "/api/v1/users/me/matches", Stats, Session},

		// User Service: profiles, avatars, search, reports, blocks, friends.
		{http.MethodGet, "/api/v1/users/me", User, Session},
		{http.MethodPatch, "/api/v1/users/me", User, Session},
		{http.MethodDelete, "/api/v1/users/me", User, Session},
		{http.MethodPost, "/api/v1/users/me/avatar", User, Session},
		{http.MethodPut, "/api/v1/users/me/avatar", User, Session},
		{http.MethodGet, "/api/v1/avatars/presets", User, Session},
		{http.MethodGet, "/api/v1/users/search", User, Session},
		{http.MethodGet, "/api/v1/users/:user_id", User, Session},
		{http.MethodPost, "/api/v1/users/:user_id/reports", User, Session},
		{http.MethodPut, "/api/v1/users/:user_id/block", User, Session},
		{http.MethodDelete, "/api/v1/users/:user_id/block", User, Session},
		{http.MethodGet, "/api/v1/friends", User, Session},
		{http.MethodDelete, "/api/v1/friends/:user_id", User, Session},
		{http.MethodGet, "/api/v1/friends/requests", User, Session},
		{http.MethodPost, "/api/v1/friends/requests", User, Session},
		{http.MethodPost, "/api/v1/friends/requests/:request_id/accept", User, Session},
		{http.MethodPost, "/api/v1/friends/requests/:request_id/decline", User, Session},
		{http.MethodDelete, "/api/v1/friends/requests/:request_id", User, Session},

		// Game Session: REST v2 rooms.
		{http.MethodPost, "/api/v2/rooms", Game, Session},
		{http.MethodGet, "/api/v2/rooms/lookup/:room_code", Game, Session},
		{http.MethodGet, "/api/v2/rooms/:room_id", Game, Session},
		{http.MethodPost, "/api/v2/rooms/:room_id/members", Game, Session},
		{http.MethodDelete, "/api/v2/rooms/:room_id/members/me", Game, Session},
	}
}
