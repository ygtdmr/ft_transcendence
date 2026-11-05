package httpresponse

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Cookie describes a session-style cookie as the API contract defines them:
// HttpOnly, Secure and SameSite=Lax, with an explicit Path (ft_session uses "/",
// ft_refresh uses "/api/v1/auth/refresh"). Those attributes are not options; the
// only deviation allowed is Insecure, for local development over plain HTTP.
type Cookie struct {
	Name string
	// Path scopes the cookie and is required, so a cookie is never accidentally
	// sent to every route.
	Path string
	// MaxAge is the lifetime for SetCookie; zero makes a browser-session cookie.
	MaxAge time.Duration
	// Insecure drops the Secure attribute. Never set it in a deployed
	// environment; browsers only send Secure cookies over HTTPS.
	Insecure bool
}

// SetCookie writes a Set-Cookie header for c with the given value. Call it
// before WriteData or WriteNoContent, because headers cannot change after the
// status line is written. It returns an error, and writes nothing, if the name,
// path or value is not a valid cookie component (for example a value containing
// a semicolon, space or line break), instead of letting net/http silently drop
// the header.
func SetCookie(w http.ResponseWriter, c Cookie, value string) error {
	if value == "" {
		return errors.New("httpresponse: cookie value must not be empty; use ClearCookie to delete")
	}
	if !validCookieValue(value) {
		return errors.New("httpresponse: cookie value contains a character that is not allowed in a cookie")
	}
	cookie, err := build(c, value)
	if err != nil {
		return err
	}
	if c.MaxAge > 0 {
		seconds := int(c.MaxAge / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		cookie.MaxAge = seconds
	}
	http.SetCookie(w, cookie)
	return nil
}

// ClearCookie tells the browser to delete the cookie (empty value, Max-Age=0).
// The Path must match the one used to set it.
func ClearCookie(w http.ResponseWriter, c Cookie) error {
	cookie, err := build(c, "")
	if err != nil {
		return err
	}
	cookie.MaxAge = -1 // serialised as Max-Age=0
	http.SetCookie(w, cookie)
	return nil
}

func build(c Cookie, value string) (*http.Cookie, error) {
	if c.Name == "" || !strings.HasPrefix(c.Path, "/") {
		return nil, errors.New("httpresponse: cookie needs a name and a path starting with /")
	}
	cookie := &http.Cookie{
		Name:     c.Name,
		Value:    value,
		Path:     c.Path,
		HttpOnly: true,
		Secure:   !c.Insecure,
		SameSite: http.SameSiteLaxMode,
	}
	if err := cookie.Valid(); err != nil {
		return nil, fmt.Errorf("httpresponse: invalid cookie: %w", err)
	}
	return cookie, nil
}

// validCookieValue accepts only RFC 6265 cookie-octets: printable ASCII except
// space, double quote, comma, semicolon and backslash. net/http is more lenient
// (it quotes a value containing a space), but a session token never needs that,
// and a stricter rule rules out ambiguity between parsers.
func validCookieValue(v string) bool {
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b < 0x21 || b > 0x7e || b == '"' || b == ',' || b == ';' || b == '\\' {
			return false
		}
	}
	return true
}
