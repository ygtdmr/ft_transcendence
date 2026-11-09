package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"game-session/room"
	"game-session/service"
)

type GraceMember struct {
	RoomID int64
	UserID int64
}

type Due struct {
	CountdownRooms []int64
	TurnRooms      []int64
	StaffingRooms  []int64
	PostGameRooms  []int64
	CloseRooms     []int64
	GraceMembers   []GraceMember
}

type DeadlineStore interface {
	DueDeadlines(now time.Time) (Due, error)
}

type Worker struct {
	store DeadlineStore
	svc   *service.RoomService
	log   *slog.Logger
}

func New(store DeadlineStore, svc *service.RoomService, log *slog.Logger) *Worker {
	return &Worker{store: store, svc: svc, log: log}
}

func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.Tick(now.UTC())
		}
	}
}

func (w *Worker) Tick(now time.Time) {
	due, err := w.store.DueDeadlines(now)
	if err != nil {
		w.log.Error("deadline scan failed", "err", err)
		return
	}

	w.startGames(due.CountdownRooms, now)
	w.expireTurns(due.TurnRooms, now)
	w.expireStaffing(due.StaffingRooms, now)
	w.expireGrace(due.GraceMembers, now)
	w.expirePostGames(due.PostGameRooms, now)
	w.closeEmptyRooms(due.CloseRooms, now)
}

func (w *Worker) startGames(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		if started, err := w.svc.StartGame(roomID, now); err != nil {
			w.log.Error("start game failed", "room_id", roomID, "err", err)
		} else if started != nil {
			w.log.Info("game started", "room_id", roomID)
		}
	}
}

func (w *Worker) expireTurns(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		if passed, err := w.svc.PassTurnIfExpired(roomID, now); err != nil {
			w.log.Error("turn expiry failed", "room_id", roomID, "err", err)
		} else if passed != nil {
			w.log.Info("expired turn passed", "room_id", roomID)
		}
	}
}

func (w *Worker) expireStaffing(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		if cancelled, err := w.svc.CancelIfStaffingExpired(roomID, now); err != nil {
			w.log.Error("staffing expiry failed", "room_id", roomID, "err", err)
		} else if cancelled {
			w.log.Info("game cancelled after staffing expiry", "room_id", roomID)
		}
	}
}

func (w *Worker) expireGrace(members []GraceMember, now time.Time) {
	for _, member := range members {
		_, err := w.svc.Leave(member.RoomID, member.UserID, room.DepartureDisconnected, now)
		if errors.Is(err, room.ErrGraceNotExpired) || errors.Is(err, room.ErrNotRoomMember) {
			continue
		}
		if err != nil {
			w.log.Error("grace expiry failed", "room_id", member.RoomID, "user_id", member.UserID, "err", err)
			continue
		}
		w.log.Info("member removed after grace expiry", "room_id", member.RoomID, "user_id", member.UserID)
	}
}

func (w *Worker) expirePostGames(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		if result, err := w.svc.ExpirePostGame(roomID, now); err != nil {
			w.log.Error("post-game expiry failed", "room_id", roomID, "err", err)
		} else if len(result.RemovedUserIDs) > 0 {
			w.log.Info("post-game members removed", "room_id", roomID)
		}
	}
}

func (w *Worker) closeEmptyRooms(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		if closed, err := w.svc.CloseIfEmpty(roomID, now); err != nil {
			w.log.Error("empty-room close failed", "room_id", roomID, "err", err)
		} else if closed {
			w.log.Info("empty room closed", "room_id", roomID)
		}
	}
}
