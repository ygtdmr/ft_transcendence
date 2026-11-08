package rest

import (
	"net/http"
	"time"

	"game-session/room"
	"game-session/service"

	"github.com/gin-gonic/gin"
)

type Pinger interface {
	Ping() error
}

type Handler struct {
	svc *service.RoomService
	db  Pinger
	now func() time.Time
}

func NewHandler(svc *service.RoomService, db Pinger) *Handler {
	return &Handler{
		svc: svc,
		db:  db,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (h *Handler) Routes() http.Handler {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", h.health)
	router.GET("/readyz", h.ready)
	router.POST("/api/v2/rooms", h.createRoom)
	router.GET("/api/v2/rooms/lookup/:room_code", h.lookupRoom)
	router.GET("/api/v2/rooms/:room_id", h.roomSnapshot)
	router.POST("/api/v2/rooms/:room_id/members", h.joinRoom)
	router.DELETE("/api/v2/rooms/:room_id/members/me", h.leaveRoom)
	return router
}

func (h *Handler) health(c *gin.Context) {
	writeData(c, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ready(c *gin.Context) {
	if err := h.db.Ping(); err != nil {
		writeError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Database unavailable.", nil)
		return
	}
	writeData(c, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) createRoom(c *gin.Context) {
	userID, ok := userIDParam(c)
	if !ok {
		return
	}
	req, ok := readCreateRoomRequest(c)
	if !ok {
		return
	}

	snapshot, err := h.svc.Create(userID, req.MaxPlayers, h.now())
	if err != nil {
		writeDomainError(c, err)
		return
	}
	writeData(c, http.StatusCreated, snapshot)
}

func (h *Handler) lookupRoom(c *gin.Context) {
	if _, ok := userIDParam(c); !ok {
		return
	}
	code, ok := roomCodeParam(c)
	if !ok {
		return
	}

	summary, err := h.svc.FindRoomByCode(code)
	if err != nil {
		writeDomainError(c, err)
		return
	}
	writeData(c, http.StatusOK, summary)
}

func (h *Handler) roomSnapshot(c *gin.Context) {
	userID, ok := userIDParam(c)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(c)
	if !ok {
		return
	}
	snapshot, err := h.svc.Snapshot(roomID, userID, h.now())
	if err != nil {
		writeDomainError(c, err)
		return
	}
	writeData(c, http.StatusOK, snapshot)
}

func (h *Handler) joinRoom(c *gin.Context) {
	userID, ok := userIDParam(c)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(c)
	if !ok {
		return
	}
	snapshot, err := h.svc.Join(roomID, userID, h.now())
	if err != nil {
		writeDomainError(c, err)
		return
	}
	writeData(c, http.StatusCreated, snapshot)
}

func (h *Handler) leaveRoom(c *gin.Context) {
	userID, ok := userIDParam(c)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(c)
	if !ok {
		return
	}
	if _, err := h.svc.Leave(roomID, userID, room.DepartureExited, h.now()); err != nil {
		writeDomainError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
