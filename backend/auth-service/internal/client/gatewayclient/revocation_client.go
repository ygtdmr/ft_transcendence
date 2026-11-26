// Package gatewayclient tells Game Gateway and Notification Gateway instances
// that a session was revoked.
package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxResponseBody bounds one answer; a valid one has no body.
const maxResponseBody = 16 << 10

// Instance is one running gateway and Auth's credential for it.
type Instance struct {
	URL        string
	Credential string
}

// Client sends session revocation notifications to every configured instance.
type Client struct {
	instances []Instance
	http      *http.Client
}

// New returns a client for the given gateway instances.
//
// Parameters:
//
//   - instances: private origins of every running Game Gateway and
//     Notification Gateway, each with Auth's credential for its kind.
//   - timeout: Upper bound for one request; a timeout means the outcome is unknown.
//
// Errors:
//
//   - No instance is given, an origin is not an absolute http(s) origin
//     without a path or user information, a credential is empty, or timeout is not positive.
func New(instances []Instance, timeout time.Duration) (*Client, error) {
	if len(instances) == 0 || timeout <= 0 {
		return nil, errors.New("gateway client needs at least one instance and a positive timeout")
	}
	normalized := make([]Instance, 0, len(instances))
	for _, instance := range instances {
		parsed, err := url.Parse(strings.TrimRight(instance.URL, "/"))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.User != nil {
			return nil, errors.New("gateway URLs must be http(s) origins without a path")
		}
		if instance.Credential == "" {
			return nil, errors.New("gateway client needs a credential for every instance")
		}
		normalized = append(normalized, Instance{URL: parsed.String(), Credential: instance.Credential})
	}

	return &Client{
		instances: normalized,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

// SessionRevoked sends PUT /internal/v1/session-revocations/{session_id} with
// {"user_id", "cause"} to every instance at once. Each gateway closes its own
// sockets of that session with 4401 and answers 204, also for a repeat or a
// session it does not know, so the whole notification can be sent again.
//
// Errors:
//
//   - One error per instance that did not answer 204, joined. The message
//     names the instance and status, never the credential.
func (c *Client) SessionRevoked(ctx context.Context, userID int64, sessionID, cause string) error {
	body, err := json.Marshal(struct {
		UserID int64  `json:"user_id"`
		Cause  string `json:"cause"`
	}{userID, cause})
	if err != nil {
		return err
	}
	path := "/internal/v1/session-revocations/" + url.PathEscape(strings.ToLower(sessionID))

	failures := make([]error, len(c.instances))
	var wg sync.WaitGroup
	for i, instance := range c.instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.notify(ctx, instance, path, body); err != nil {
				failures[i] = fmt.Errorf("gateway %s: %w", instance.URL, err)
			}
		}()
	}
	wg.Wait()

	return errors.Join(failures...)
}

// notify sends one notification to one instance.
func (c *Client) notify(ctx context.Context, instance Instance, path string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, instance.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+instance.Credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("session revocation: status %d", response.StatusCode)
	}

	return nil
}
