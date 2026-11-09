package rest

import (
	"net/http"
	"time"

	"game-session/room"
	"game-session/service"
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
	router := http.NewServeMux()
	router.HandleFunc("GET /healthz", h.health)
	router.HandleFunc("GET /readyz", h.ready)
	router.HandleFunc("POST /api/v2/rooms", h.createRoom)
	router.HandleFunc("GET /api/v2/rooms/lookup/{room_code}", h.lookupRoom)
	router.HandleFunc("GET /api/v2/rooms/{room_id}", h.roomSnapshot)
	router.HandleFunc("POST /api/v2/rooms/{room_id}/members", h.joinRoom)
	router.HandleFunc("DELETE /api/v2/rooms/{room_id}/members/me", h.leaveRoom)
	return router
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	if err := h.db.Ping(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Database unavailable.", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	req, ok := readCreateRoomRequest(w, r)
	if !ok {
		return
	}

	snapshot, err := h.svc.Create(userID, req.MaxPlayers, h.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeData(w, http.StatusCreated, snapshot)
}

func (h *Handler) lookupRoom(w http.ResponseWriter, r *http.Request) {
	if _, ok := userIDParam(w, r); !ok {
		return
	}
	code, ok := roomCodeParam(w, r)
	if !ok {
		return
	}

	summary, err := h.svc.FindRoomByCode(code)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeData(w, http.StatusOK, summary)
}

func (h *Handler) roomSnapshot(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(w, r)
	if !ok {
		return
	}
	snapshot, err := h.svc.Snapshot(roomID, userID, h.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (h *Handler) joinRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(w, r)
	if !ok {
		return
	}
	snapshot, err := h.svc.Join(roomID, userID, h.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeData(w, http.StatusCreated, snapshot)
}

func (h *Handler) leaveRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDParam(w, r)
	if !ok {
		return
	}
	roomID, ok := roomIDParam(w, r)
	if !ok {
		return
	}
	if _, err := h.svc.Leave(roomID, userID, room.DepartureExited, h.now()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
