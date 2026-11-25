// Package socket serves the room socket, GET /ws/v2/rooms/{room_id}.
//
// Admission runs in the contract's order and nothing is upgraded before it
// succeeds:
//
//  1. Origin: exactly the deployment's public origin, else a bare 403 (the
//     contract: "handshake rejected, no code"). No cookie is examined.
//  2. The request must be a WebSocket upgrade (400 otherwise).
//  3. Session: only the ft_session cookie, validated by Auth Service. Invalid
//     is 401 UNAUTHORIZED; an Auth outage is 503, never a logout.
//  4. Room: a positive integer, else 404 ROOM_NOT_FOUND.
//  5. Report socket opened to Game Session. Its verdict refuses the handshake
//     with the same status and code (403 NOT_ROOM_MEMBER, 404 ROOM_NOT_FOUND,
//     409 INVALID_ROOM_STATE); any other failure is 503 SERVICE_UNAVAILABLE.
//  6. Upgrade. If the upgrade fails, the counted socket is released and, when
//     it was the member's only one, Game Session hears "last socket closed".
//
// Every text message is forwarded to Game Session unchanged, one at a time per
// socket, and Game Session's reply is written back to that socket only. A
// binary message closes the socket with 1003, an empty one is ignored, and one
// over 64 KiB closes it with 1009.
package socket

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/connectivity"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/gamesession"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/socketregistry"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

// RoutePath is the public room socket route.
const RoutePath = "/ws/v2/rooms/:room_id"

const (
	// maxClientMessage is the largest client message forwarded; Game Session
	// refuses larger bodies, so the gateway closes the socket with 1009 first.
	maxClientMessage = 64 << 10
	// writeWait bounds one write to the client.
	writeWait = 10 * time.Second
	// reportTimeout bounds one "last socket closed" report, retries included.
	reportTimeout = 10 * time.Second
)

// Rooms is the part of Game Session the socket needs.
type Rooms interface {
	Connected(ctx context.Context, roomID, userID int64) error
	Disconnected(ctx context.Context, roomID, userID int64) error
	Command(ctx context.Context, roomID, userID int64, sessionID string, raw []byte) (gamesession.CommandResult, error)
}

// Options configures the handler.
type Options struct {
	Registry *socketregistry.Registry
	Tracker  *connectivity.Tracker
	Origin   *originguard.Guard
	Sessions identity.SessionValidator
	Rooms    Rooms
	Logger   *slog.Logger
	// PingInterval and PongWait keep idle connections alive and detect dead
	// ones.
	PingInterval time.Duration
	PongWait     time.Duration
}

// Handler admits and serves room sockets.
type Handler struct {
	opts     Options
	upgrader websocket.Upgrader
	wg       sync.WaitGroup
	open     atomic.Bool
}

// New returns the socket handler. It refuses every handshake with 503 until
// Open is called, which the gateway does once Game Session acknowledged that
// this instance started without sockets.
func New(opts Options) *Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	h := &Handler{opts: opts}
	h.upgrader = websocket.Upgrader{
		HandshakeTimeout: writeWait,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		CheckOrigin:      opts.Origin.SameOrigin,
		Error:            upgradeError,
	}

	return h
}

// Open starts accepting handshakes.
func (h *Handler) Open() { h.open.Store(true) }

// RegisterRoutes adds the room socket route.
func (h *Handler) RegisterRoutes(routes gin.IRoutes) {
	routes.GET(RoutePath, ginhttp.Wrap(h.opts.Origin.WebSocket()), requireUpgrade,
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

func requireUpgrade(c *gin.Context) {
	if !websocket.IsWebSocketUpgrade(c.Request) {
		writeHandshakeError(c.Writer, http.StatusBadRequest, "VALIDATION_ERROR", "This route only accepts a WebSocket upgrade.")
		c.Abort()

		return
	}
	c.Next()
}

func upgradeError(w http.ResponseWriter, _ *http.Request, status int, _ error) {
	writeHandshakeError(w, status, "VALIDATION_ERROR", "The WebSocket handshake is invalid.")
}

func writeHandshakeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	httpresponse.WriteError(w, &httpresponse.Error{Status: status, Code: code, Message: message})
}

func (h *Handler) accept(c *gin.Context) {
	id, ok := identity.FromContext(c.Request.Context())
	if !ok {
		httpresponse.WriteError(c.Writer, httpresponse.InternalError())

		return
	}
	roomID, err := validate.ParsePositiveInt("room_id", c.Param("room_id"))
	if err != nil {
		writeHandshakeError(c.Writer, http.StatusNotFound, "ROOM_NOT_FOUND", "The room does not exist.")

		return
	}
	if !h.open.Load() {
		writeHandshakeError(c.Writer, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE",
			"The service is temporarily unavailable.")

		return
	}

	err = h.opts.Tracker.Open(roomID, id.UserID, func() error {
		return h.opts.Rooms.Connected(c.Request.Context(), roomID, id.UserID)
	})
	switch {
	case errors.Is(err, gamesession.ErrNotMember):
		writeHandshakeError(c.Writer, http.StatusForbidden, "NOT_ROOM_MEMBER", "You are not a member of this room.")
		return
	case errors.Is(err, gamesession.ErrRoomNotFound):
		writeHandshakeError(c.Writer, http.StatusNotFound, "ROOM_NOT_FOUND", "The room does not exist.")
		return
	case errors.Is(err, gamesession.ErrRoomClosed):
		writeHandshakeError(c.Writer, http.StatusConflict, "INVALID_ROOM_STATE", "The room is closed.")
		return
	case err != nil:
		h.opts.Logger.WarnContext(c.Request.Context(), "room_admission_unavailable", "room_id", roomID)
		writeHandshakeError(c.Writer, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE",
			"The service is temporarily unavailable.")
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Game Session counts the member connected; nothing will be.
		h.release(roomID, id.UserID)

		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer h.release(roomID, id.UserID)
		h.serve(conn, id, roomID)
	}()
}

// release uncounts one socket and reports "last socket closed" when it was the
// member's last one for the room.
func (h *Handler) release(roomID, userID int64) {
	h.opts.Tracker.Close(roomID, userID, func() {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		if err := h.opts.Rooms.Disconnected(ctx, roomID, userID); err != nil {
			h.opts.Logger.Warn("room_disconnect_report_failed", "room_id", roomID)
		}
	})
}

// serve registers the socket and runs it until it closes.
func (h *Handler) serve(conn *websocket.Conn, id identity.Identity, roomID int64) {
	scope := strconv.FormatInt(roomID, 10)
	sessionID := strings.ToLower(id.SessionID)
	sock, err := h.opts.Registry.AddIn(scope, id.UserID, sessionID)
	switch {
	case errors.Is(err, socketregistry.ErrRevoked):
		closeNow(conn, socketregistry.Closure{Code: CloseSessionRevoked, Reason: "SESSION_REVOKED", Final: UnauthorizedFrame})
		return
	case err != nil:
		closeNow(conn, socketregistry.Closure{Code: CloseGoingAway, Reason: "GOING_AWAY"})
		return
	}
	defer h.opts.Registry.Remove(sock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// A closing socket cancels its in-flight command; the command ledger
		// makes a repeat by the client safe.
		select {
		case <-sock.Closing():
			cancel()
		case <-ctx.Done():
		}
	}()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.write(conn, sock)
	}()
	h.read(ctx, conn, sock, roomID, sessionID)
	<-writerDone
}

// read forwards client messages one at a time until the connection ends.
func (h *Handler) read(ctx context.Context, conn *websocket.Conn, sock *socketregistry.Socket, roomID int64, sessionID string) {
	conn.SetReadLimit(maxClientMessage)
	extend := func() { _ = conn.SetReadDeadline(time.Now().Add(h.opts.PongWait)) }
	extend()
	conn.SetPongHandler(func(string) error {
		extend()

		return nil
	})

	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			// The peer left, the connection broke, a message was too large
			// (the library closes with 1009), or the writer closed it.
			sock.Close(socketregistry.Closure{Code: CloseNormal})

			return
		}
		extend()
		if kind == websocket.BinaryMessage {
			sock.Close(socketregistry.Closure{Code: CloseUnsupportedData, Reason: "UNSUPPORTED_DATA"})

			return
		}
		if len(data) == 0 {
			continue
		}
		h.forward(ctx, sock, roomID, sessionID, data)
		if ctx.Err() != nil {
			return
		}
	}
}

// forward sends one client message to Game Session and applies its answer.
func (h *Handler) forward(ctx context.Context, sock *socketregistry.Socket, roomID int64, sessionID string, data []byte) {
	result, err := h.opts.Rooms.Command(ctx, roomID, sock.UserID(), sessionID, data)
	if err != nil {
		if ctx.Err() == nil {
			// Nothing reaches the client; it may repeat the same request_id,
			// which Game Session's ledger answers once.
			h.opts.Logger.Warn("room_command_unavailable", "room_id", roomID)
		}

		return
	}
	var own *gamesession.Closure
	for i := range result.Closures {
		cl := result.Closures[i]
		if cl.Code < 4000 || cl.Code > 4999 {
			continue
		}
		if cl.UserID == sock.UserID() {
			own = &cl
			continue
		}
		h.opts.Registry.CloseUserIn(sock.Scope(), cl.UserID, socketregistry.Closure{Code: cl.Code, Reason: cl.Reason})
	}
	if own == nil {
		sock.Offer(result.Reply)

		return
	}
	// The reply is written before this socket's close frame; the member's
	// other sockets for the room close too.
	sock.Close(socketregistry.Closure{Code: own.Code, Reason: own.Reason, Final: result.Reply})
	h.opts.Registry.CloseUserIn(sock.Scope(), own.UserID, socketregistry.Closure{Code: own.Code, Reason: own.Reason})
}

// write is the only goroutine that writes data frames to conn.
func (h *Handler) write(conn *websocket.Conn, sock *socketregistry.Socket) {
	ticker := time.NewTicker(h.opts.PingInterval)
	defer ticker.Stop()
	for {
		// A pending close wins over queued messages.
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
func closeNow(conn *websocket.Conn, c socketregistry.Closure) {
	deadline := time.Now().Add(writeWait)
	if c.Final != nil {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.WriteMessage(websocket.TextMessage, c.Final)
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(c.Code, c.Reason), deadline)
	_ = conn.Close()
}
