package rest

import (
	"net/http"
	"time"

	"game-session/room"
	"game-session/roomaction"
	"game-session/service"

	"github.com/gin-gonic/gin"
)

type Store interface {
	service.RoomStore
	Ping() error
}

type Handler struct {
	svc     *service.RoomService
	actions *roomaction.Handler
	store   Store
	now     func() time.Time
}

func NewHandler(svc *service.RoomService, actions *roomaction.Handler, store Store) *Handler {
	return &Handler{
		svc:     svc,
		actions: actions,
		store:   store,
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

func (h *Handler) InternalRoutes(access *ServiceAccessCheck) http.Handler {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/internal/v1/memberships/:user_id", access.RequireScope("membership.read"), h.membership)
	router.PUT("/internal/v1/account-cleanups/:operation_id", access.RequireScope("account.cleanup"), h.accountCleanup)
	return router
}

func (h *Handler) health(c *gin.Context) {
	writeData(c, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ready(c *gin.Context) {
	if err := h.store.Ping(); err != nil {
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

	var snapshot room.RoomSnapshot
	err := h.store.Transaction(func(tx service.RoomStore) error {
		var err error
		snapshot, err = h.svc.Create(tx, userID, req.MaxPlayers, h.now())
		return err
	})
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

	var summary service.RoomSummary
	err := h.store.ReadTransaction(func(tx service.RoomStore) error {
		var err error
		summary, err = h.svc.FindRoomByCode(tx, code)
		return err
	})
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
	var snapshot room.RoomSnapshot
	err := h.store.ReadTransaction(func(tx service.RoomStore) error {
		var err error
		snapshot, err = h.svc.Snapshot(tx, roomID, userID, h.now())
		return err
	})
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
	var outcome roomaction.Outcome
	err := h.store.Transaction(func(tx service.RoomStore) error {
		var err error
		outcome, err = h.actions.Join(tx, roomID, userID, h.now())
		return err
	})
	if err != nil {
		writeDomainError(c, err)
		return
	}
	writeData(c, http.StatusCreated, outcome.ActorReply)
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
	err := h.store.Transaction(func(tx service.RoomStore) error {
		_, err := h.actions.Leave(tx, roomID, userID, room.DepartureExited, h.now())
		return err
	})
	if err != nil {
		writeDomainError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
