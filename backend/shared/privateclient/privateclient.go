// Package privateclient is the caller side of the private service contract: one
// way to call another service's /internal/v1 routes, with the contract's
// failure semantics built in so every caller classifies outcomes the same way.
//
// Every call sends the caller's credential for that one receiver as
// "Authorization: Bearer <credential>", and only ever the end user's ft_session
// cookie when an operation needs it. The credential is held as a config.Secret,
// never appears in an error, and never leaves for any other host. Calls carry the
// request ID and a child trace span (see requestcontext.Transport) and stop with
// the caller's context.
//
// Outcomes, as the contract defines them:
//
//   - ErrUnavailable: network failure, timeout, cancellation, a 5xx, or an
//     unreadable answer. The outcome is UNKNOWN, not failed: for a write the
//     receiver may have applied it. Retry the identical request later (Retryable).
//   - ErrServiceRejected: 401 SERVICE_UNAUTHENTICATED or 403 SERVICE_FORBIDDEN.
//     A deployment configuration fault: fail closed, do not retry in a loop, and
//     never present it to a browser as a logout.
//   - *RemoteError: any other 4xx. The receiver understood the call and refused
//     it with a code (UNAUTHORIZED, USER_NOT_FOUND, VALIDATION_ERROR ...) that
//     the caller maps to its own answer.
//
// A dependency failure is never turned into a not-found, invalid or offline
// verdict here: only an explicit 4xx code is a verdict.
package privateclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestcontext"
)

// DefaultMaxResponseBytes caps how much of an answer is read.
const DefaultMaxResponseBytes = 64 << 10

const sessionCookieName = "ft_session"

var (
	// ErrInvalidConfig reports a bad Client configuration.
	ErrInvalidConfig = errors.New("privateclient: invalid configuration")
	// ErrInvalidRequest reports a request the client refuses to send (a path
	// outside /internal/, a malformed cookie value, an unencodable body).
	ErrInvalidRequest = errors.New("privateclient: invalid request")
	// ErrUnavailable: see the package comment.
	ErrUnavailable = errors.New("privateclient: receiver unavailable; the outcome is unknown")
	// ErrServiceRejected: see the package comment.
	ErrServiceRejected = errors.New("privateclient: receiver rejected this service's credential or scope")
)

// RemoteError is a receiver's explicit refusal: its HTTP status and error code.
// Messages and details are deliberately not kept; the code is the contract.
type RemoteError struct {
	Status int
	Code   string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("privateclient: receiver answered status %d code %q", e.Status, e.Code)
}

// Code returns the receiver's error code carried by err, or "".
func Code(err error) string {
	var remote *RemoteError
	if errors.As(err, &remote) {
		return remote.Code
	}
	return ""
}

// Retryable reports whether sending the identical request again later is
// appropriate: only an unavailable receiver. A rejected credential or an explicit
// refusal will not change by retrying.
func Retryable(err error) bool { return errors.Is(err, ErrUnavailable) }

// Config describes a client for one receiver.
type Config struct {
	// BaseURL is the receiver's private listener origin, e.g. http://user-service:8091.
	BaseURL string
	// Credential is this caller's credential for this receiver.
	Credential config.Secret
	// Timeout bounds one call. It is required because the contract specifies no
	// value; the caller chooses and documents it.
	Timeout time.Duration
	// MaxResponseBytes caps the answer; zero means DefaultMaxResponseBytes.
	MaxResponseBytes int64
	// Transport is the underlying transport; nil means http.DefaultTransport.
	Transport http.RoundTripper
}

// ConfigFromEnv reads a receiver's settings through r from the named variables:
// the receiver's private URL, the file holding this caller's credential for it
// (32+ bytes), and the per-call timeout (required, 1s-1m).
func ConfigFromEnv(r *config.Reader, urlVar, credentialFileVar, timeoutVar string) Config {
	cfg := Config{
		BaseURL:    r.URL(urlVar, "http", "https"),
		Credential: r.SecretFile(credentialFileVar, 32),
		Timeout:    r.Duration(timeoutVar, 0, time.Second, time.Minute),
	}
	if cfg.Timeout == 0 {
		r.Problem(timeoutVar, "is required")
	}
	return cfg
}

// Client calls one receiver. It is safe for concurrent use.
type Client struct {
	baseURL    string
	credential config.Secret
	http       *http.Client
	maxBody    int64
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("%w: BaseURL must be an http(s) origin without path, query or credentials", ErrInvalidConfig)
	}
	if !cfg.Credential.IsSet() {
		return nil, fmt.Errorf("%w: a credential is required", ErrInvalidConfig)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: a positive Timeout is required", ErrInvalidConfig)
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxResponseBytes
	}
	return &Client{
		baseURL:    parsed.String(),
		credential: cfg.Credential,
		maxBody:    maxBody,
		http: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: requestcontext.Transport(cfg.Transport),
			// A private listener never redirects; following one could send the
			// credential to a different host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Request describes one private call.
type Request struct {
	Method string
	// Path must start with /internal/ and be clean: no dot segments, no empty
	// segments, no query or fragment. Escape dynamic segments with url.PathEscape.
	Path  string
	Query url.Values
	// Body, if non-nil, is sent as JSON.
	Body any
	// SessionCookie, if set, is forwarded as the ft_session cookie and nothing
	// else. Never pass ft_refresh or any other browser cookie.
	SessionCookie string
}

// Do sends req and decodes the {"data": ...} of a successful answer into out
// (nil to discard it; a 204 has no body). See the package comment for how
// failures are classified. Do never retries; the caller decides, using Retryable.
func (c *Client) Do(ctx context.Context, req Request, out any) error {
	httpReq, err := c.build(ctx, req)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		// The transport error is dropped: it embeds the URL and can embed more.
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil || int64(len(raw)) > c.maxBody {
		return fmt.Errorf("%w: unreadable or oversized answer", ErrUnavailable)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return decodeSuccess(resp.StatusCode, raw, out)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}

	code := errorCode(raw)
	if (resp.StatusCode == http.StatusUnauthorized && code == "SERVICE_UNAUTHENTICATED") ||
		(resp.StatusCode == http.StatusForbidden && code == "SERVICE_FORBIDDEN") {
		return fmt.Errorf("%w: status %d", ErrServiceRejected, resp.StatusCode)
	}
	return &RemoteError{Status: resp.StatusCode, Code: code}
}

func (c *Client) build(ctx context.Context, req Request) (*http.Request, error) {
	if !strings.HasPrefix(req.Path, "/internal/") || pathpkg.Clean(req.Path) != req.Path ||
		strings.ContainsAny(req.Path, "?#\\") || strings.Contains(req.Path, "//") {
		return nil, fmt.Errorf("%w: path must be a clean /internal/ path", ErrInvalidRequest)
	}
	target := c.baseURL + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}

	var body io.Reader
	if req.Body != nil {
		encoded, err := json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: body cannot be encoded", ErrInvalidRequest)
		}
		body = bytes.NewReader(encoded)
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed request", ErrInvalidRequest)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.credential.Reveal())
	httpReq.Header.Set("Accept", "application/json")
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if req.SessionCookie != "" {
		if !validCookieValue(req.SessionCookie) {
			return nil, fmt.Errorf("%w: session cookie value is not valid", ErrInvalidRequest)
		}
		httpReq.Header.Set("Cookie", sessionCookieName+"="+req.SessionCookie)
	}
	return httpReq, nil
}

func decodeSuccess(status int, raw []byte, out any) error {
	if status == http.StatusNoContent || out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("%w: malformed success answer", ErrUnavailable)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("%w: malformed success answer", ErrUnavailable)
	}
	return nil
}

func errorCode(raw []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Error.Code
}

// validCookieValue accepts RFC 6265 cookie-octets only (printable ASCII except
// space, double quote, comma, semicolon and backslash), so a value cannot add
// attributes or a second cookie to the header.
func validCookieValue(v string) bool {
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b < 0x21 || b > 0x7e || b == '"' || b == ',' || b == ';' || b == '\\' {
			return false
		}
	}
	return true
}
