package worker_test

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"game-session/game"
	"game-session/postgres"
	"game-session/room"
	"game-session/service"
	"game-session/worker"
)

var now = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

type harness struct {
	store  *postgres.Store
	svc    *service.RoomService
	worker *worker.Worker
}

func newHarness(t *testing.T) harness {
	t.Helper()
	dsn := os.Getenv("GS_DB")
	if dsn == "" {
		t.Skip("GS_DB not set")
	}
	store, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.ResetForTests(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	svc := service.NewRoomService(store, placeholderProfiles{}, nil)
	return harness{store: store, svc: svc, worker: worker.New(store, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))}
}

func readyRoom(t *testing.T, h harness) int64 {
	t.Helper()
	capacity := 4
	snapshot, err := h.svc.Create(1, &capacity, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := snapshot.RoomID
	for _, id := range []int64{2, 3, 4} {
		if _, err := h.svc.Join(roomID, id, now); err != nil {
			t.Fatalf("join %d: %v", id, err)
		}
	}
	seats := []struct {
		user int64
		team game.Team
		role game.Role
	}{{1, game.TeamRed, game.RoleSpymaster}, {2, game.TeamRed, game.RoleOperative}, {3, game.TeamBlue, game.RoleSpymaster}, {4, game.TeamBlue, game.RoleOperative}}
	for _, s := range seats {
		if _, err := h.svc.SelectRole(roomID, s.user, s.team, s.role, now); err != nil {
			t.Fatalf("seat %d: %v", s.user, err)
		}
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if _, err := h.svc.SetReady(roomID, id, true, now); err != nil {
			t.Fatalf("ready %d: %v", id, err)
		}
	}
	return roomID
}

func TestTickStartsGameWhenCountdownEnds(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h)

	h.worker.Tick(now.Add(time.Second))
	r, _, err := h.store.RoomByID(roomID)
	if err != nil || r.Status != room.StatusCountDown {
		t.Fatalf("before countdown end: want COUNTDOWN, got %v %v", r, err)
	}

	h.worker.Tick(now.Add(5 * time.Second))
	r, members, _ := h.store.RoomByID(roomID)
	if r.Status != room.StatusInGame {
		t.Fatalf("after countdown end: want IN_GAME, got %s", r.Status)
	}
	for _, m := range members {
		if m.State != room.StateInGame {
			t.Fatalf("member %d must be IN_GAME", m.UserID)
		}
	}
	g, err := h.store.LoadCurrentGame(roomID)
	if err != nil || g == nil {
		t.Fatalf("game must exist after start: %v %v", g, err)
	}
}

func TestTickRemovesMemberWhenGraceExpires(t *testing.T) {
	h := newHarness(t)
	capacity := 4
	snapshot, _ := h.svc.Create(1, &capacity, now)
	if _, err := h.svc.Join(snapshot.RoomID, 2, now); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := h.svc.MarkDisconnected(snapshot.RoomID, 2, now); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	h.worker.Tick(now.Add(10 * time.Second))
	if _, members, _ := h.store.RoomByID(snapshot.RoomID); len(members) != 2 {
		t.Fatalf("grace not over yet: want 2 members, got %d", len(members))
	}

	h.worker.Tick(now.Add(31 * time.Second))
	_, members, _ := h.store.RoomByID(snapshot.RoomID)
	if len(members) != 1 || members[0].UserID != 1 {
		t.Fatalf("after grace: want only host, got %+v", members)
	}
}

func TestTickClosesEmptyRoomAfterTimeout(t *testing.T) {
	h := newHarness(t)
	capacity := 4
	snapshot, _ := h.svc.Create(1, &capacity, now)
	if _, err := h.svc.Leave(snapshot.RoomID, 1, room.DepartureExited, now); err != nil {
		t.Fatalf("leave: %v", err)
	}

	h.worker.Tick(now.Add(299 * time.Second))
	if r, _, _ := h.store.RoomByID(snapshot.RoomID); r.Status == room.StatusClosed {
		t.Fatal("room closed too early")
	}

	h.worker.Tick(now.Add(301 * time.Second))
	r, _, _ := h.store.RoomByID(snapshot.RoomID)
	if r.Status != room.StatusClosed {
		t.Fatalf("empty room must close after 300s, got %s", r.Status)
	}
	if _, _, err := h.store.RoomByCode(r.RoomCode); err == nil {
		t.Fatal("closed room must not resolve by code")
	}
}

// Testler için örnek profil kaynağı.
type placeholderProfiles struct{}

func (placeholderProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{
			Username:  fmt.Sprintf("user-%d", id),
			AvatarURL: fmt.Sprintf("/static/avatars/default/%d.svg", id),
		}
	}
	return profiles, nil
}
