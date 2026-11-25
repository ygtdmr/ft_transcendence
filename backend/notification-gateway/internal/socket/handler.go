// Package socket serves the notification socket, GET /ws/v2/notifications.
//
// Admission runs in the contract's order, and nothing is upgraded before it
// succeeds:
//
//  1. Origin: exactly the deployment's public origin, else
//     403 ORIGIN_NOT_ALLOWED. No cookie is examined for a foreign page.
//  2. The request must be a WebSocket upgrade.
//  3. Session: only the ft_session cookie, validated by Auth Service
//     (session.validate). An invalid session is 401 UNAUTHORIZED; an Auth
//     outage is 503 SERVICE_UNAVAILABLE, never a logout; a rejected service
//     credential is a configuration fault (500). A token in a query string or
//     header is never read.
//  4. Upgrade, register the socket, send notifications.ready.
//
// The socket accepts no client command: every client frame is answered with
// INVALID_EVENT and the socket stays open, until a client sends more than
// abuseLimit frames in abuseWindow, which closes it with 1008.
package socket

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/socketregistry"
)

// Path is the public notification socket route.
const Path = "/ws/v2/notifications"

const (
	// maxClientMessage bounds one client frame. The socket accepts no
	// commands, so anything larger is abuse; gorilla closes it with 1009.
	maxClientMessage = 4 << 10
	// writeWait bounds one write to the client.
	writeWait = 10 * time.Second
	// abuseLimit client frames are answered within abuseWindow; one more
	// closes the socket with 1008.
	abuseLimit  = 10
	abuseWindow = time.Minute
)

// Options configures the handler.
type Options struct {
	Registry *socketregistry.Registry
	Origin   *originguard.Guard
	Sessions identity.SessionValidator
	Logger   *slog.Logger
	// PingInterval and PongWait keep idle connections alive and detect dead
	// ones.
	PingInterval time.Duration
	PongWait     time.Duration
	// Now stamps notifications.ready and counts client frames; tests replace
	// it. Connection deadlines always use the real clock. Default time.Now.
	Now func() time.Time
}

// Handler admits and serves notification sockets.
type Handler struct {
	opts     Options
	upgrader websocket.Upgrader
	wg       sync.WaitGroup
}

// New returns the socket handler.
func New(opts Options) *Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	h := &Handler{opts: opts}
	h.upgrader = websocket.Upgrader{
		HandshakeTimeout: writeWait,
		ReadBufferSize:   1024,
		WriteBufferSize:  4096,
		// Checked already; repeated here so the library can never accept a
		// foreign origin on its own.
		CheckOrigin: opts.Origin.SameOrigin,
		Error:       upgradeError,
	}

	return h
}

// RegisterRoutes adds the socket route.
func (h *Handler) RegisterRoutes(routes gin.IRoutes) {
	routes.GET(Path, h.checkOrigin, requireUpgrade,
		ginhttp.Wrap(identity.Authenticate(h.opts.Sessions, identity.Options{Logger: h.opts.Logger})),
		h.accept)
}

// Wait blocks until every socket goroutine has finished, or until done is
// closed. It reports whether all of them finished.
func (h *Handler) Wait(done <-chan struct{}) bool {
	finished := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return true
	case <-done:
		return false
	}
}

func (h *Handler) checkOrigin(c *gin.Context) {
	if !h.opts.Origin.SameOrigin(c.Request) {
		c.Header("Cache-Control", "no-store")
		httpresponse.WriteError(c.Writer, &httpresponse.Error{
			Status:  http.StatusForbidden,
			Code:    "ORIGIN_NOT_ALLOWED",
			Message: "The request origin is not allowed.",
		})
		c.Abort()

		return
	}
	c.Next()
}

func requireUpgrade(c *gin.Context) {
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.Header("Cache-Control", "no-store")
		httpresponse.WriteError(c.Writer, &httpresponse.Error{
			Status:  http.StatusBadRequest,
			Code:    "VALIDATION_ERROR",
			Message: "This route only accepts a WebSocket upgrade.",
		})
		c.Abort()

		return
	}
	c.Next()
}

// upgradeError answers a handshake the library refuses (for example a missing
// Sec-WebSocket-Key) with the contract's error envelope instead of plain text.
func upgradeError(w http.ResponseWriter, _ *http.Request, status int, _ error) {
	w.Header().Set("Cache-Control", "no-store")
	httpresponse.WriteError(w, &httpresponse.Error{
		Status:  status,
		Code:    "VALIDATION_ERROR",
		Message: "The WebSocket handshake is invalid.",
	})
}

func (h *Handler) accept(c *gin.Context) {
	id, ok := identity.FromContext(c.Request.Context())
	if !ok {
		// Authenticate always sets the identity; reaching here without one is
		// a wiring fault, so fail closed.
		httpresponse.WriteError(c.Writer, httpresponse.InternalError())

		return
	}
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// The upgrader has already answered the handshake.
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.serve(conn, id)
	}()
}

// serve registers the socket and runs it until it closes.
func (h *Handler) serve(conn *websocket.Conn, id identity.Identity) {
	sessionID := strings.ToLower(id.SessionID)
	sock, err := h.opts.Registry.Add(id.UserID, sessionID)
	switch {
	case errors.Is(err, socketregistry.ErrRevoked):
		// Auth validated the session, then reported it revoked before the
		// socket was registered.
		closeNow(conn, socketregistry.Closure{Code: CloseSessionRevoked, Reason: "SESSION_REVOKED",
			Final: UnauthorizedFrame})

		return
	case err != nil:
		closeNow(conn, socketregistry.Closure{Code: CloseGoingAway, Reason: "GOING_AWAY"})

		return
	}
	defer h.opts.Registry.Remove(sock)

	sock.Offer(readyMessage(id.UserID, h.opts.Now()))

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.write(conn, sock)
	}()
	h.read(conn, sock)
	<-writerDone
}

// read consumes client frames until the connection ends or the socket closes.
func (h *Handler) read(conn *websocket.Conn, sock *socketregistry.Socket) {
	conn.SetReadLimit(maxClientMessage)
	extend := func() { _ = conn.SetReadDeadline(time.Now().Add(h.opts.PongWait)) }
	extend()
	conn.SetPongHandler(func(string) error {
		extend()

		return nil
	})

	var recent []time.Time
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			// The peer left, the connection broke, a frame was too large, or
			// the writer closed the connection. Make sure the writer stops.
			sock.Close(socketregistry.Closure{Code: CloseNormal})

			return
		}
		extend()
		now := h.opts.Now()
		recent = withinWindow(recent, now)
		if len(recent) >= abuseLimit {
			sock.Close(socketregistry.Closure{Code: ClosePolicyViolation, Reason: "POLICY_VIOLATION",
				Final: invalidEventFrame})

			return
		}
		recent = append(recent, now)
		sock.Offer(invalidEventFrame)
	}
}

// withinWindow keeps the timestamps of the last abuseWindow.
func withinWindow(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-abuseWindow)
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	return kept
}

// write is the only goroutine that writes data frames to conn.
func (h *Handler) write(conn *websocket.Conn, sock *socketregistry.Socket) {
	ticker := time.NewTicker(h.opts.PingInterval)
	defer ticker.Stop()
	for {
		// A pending close wins over queued events: nothing is delivered to a
		// revoked session after its revocation.
		select {
		case <-sock.Closing():
			closeNow(conn, sock.Closure())

			return
		default:
		}
		select {
		case <-sock.Closing():
			closeNow(conn, sock.Closure())

			return
		case msg := <-sock.Send():
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				sock.Close(socketregistry.Closure{Code: CloseNormal})
				_ = conn.Close()

				return
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				sock.Close(socketregistry.Closure{Code: CloseNormal})
				_ = conn.Close()

				return
			}
		}
	}
}

// closeNow writes c.Final (if any) and the close frame, then closes conn.
// Write errors are ignored: the connection is going away either way.
func closeNow(conn *websocket.Conn, c socketregistry.Closure) {
	deadline := time.Now().Add(writeWait)
	if c.Final != nil {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.WriteMessage(websocket.TextMessage, c.Final)
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(c.Code, c.Reason), deadline)
	_ = conn.Close()
}
