package worker

import (
	"context"
	"log/slog"
	"time"

	"game-session/roomaction"
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
	Transaction(fn func(tx service.RoomStore) error) error
}

type Worker struct {
	store   DeadlineStore
	svc     *service.RoomService
	actions *roomaction.Handler
	log     *slog.Logger
}

func New(store DeadlineStore, svc *service.RoomService, actions *roomaction.Handler, log *slog.Logger) *Worker {
	return &Worker{store: store, svc: svc, actions: actions, log: log}
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
	for _, roomID := range due.CountdownRooms {
		w.runDeadline(roomaction.DeadlineStartGame, roomID, 0, now)
	}
	for _, roomID := range due.TurnRooms {
		w.runDeadline(roomaction.DeadlineExpireTurn, roomID, 0, now)
	}
	for _, roomID := range due.StaffingRooms {
		w.runDeadline(roomaction.DeadlineExpireStaffing, roomID, 0, now)
	}
	for _, member := range due.GraceMembers {
		w.runDeadline(roomaction.DeadlineExpireGrace, member.RoomID, member.UserID, now)
	}
	for _, roomID := range due.PostGameRooms {
		w.runDeadline(roomaction.DeadlineExpirePostGame, roomID, 0, now)
	}
	w.closeEmptyRooms(due.CloseRooms, now)
}

func (w *Worker) runDeadline(kind roomaction.DeadlineKind, roomID int64, userID int64, now time.Time) {
	err := w.store.Transaction(func(tx service.RoomStore) error {
		_, err := w.actions.RunDeadline(tx, kind, roomID, userID, now)
		return err
	})
	if err != nil {
		w.log.Error("deadline failed", "kind", kind, "room_id", roomID, "user_id", userID, "err", err)
	} else {
		w.log.Info("deadline applied", "kind", kind, "room_id", roomID, "user_id", userID)
	}
}

func (w *Worker) closeEmptyRooms(roomIDs []int64, now time.Time) {
	for _, roomID := range roomIDs {
		closed := false
		err := w.store.Transaction(func(tx service.RoomStore) error {
			var err error
			closed, err = w.svc.CloseIfEmpty(tx, roomID, now)
			if err != nil || !closed {
				return err
			}
			return tx.DeleteCommandResults(roomID)
		})
		if err != nil {
			w.log.Error("empty-room close failed", "room_id", roomID, "err", err)
		} else if closed {
			w.log.Info("empty room closed", "room_id", roomID)
		}
	}
}
