package postgres

import (
	"errors"
	"os"
	"testing"
	"time"

	"game-session/game"
	"game-session/room"
	"game-session/service"
)

func openTestStore(t *testing.T) *Store {
	dsn := os.Getenv("GS_DB")
	if dsn == "" {
		t.Skip("GS_DB not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	resetTables(t, s)
	return s
}

func resetTables(t *testing.T, s *Store) {
	t.Helper()
	if err := s.ResetForTests(); err != nil {
		t.Fatalf("reset tables: %v", err)
	}
}

func TestRoomByIDNotFound(t *testing.T) {
	s := openTestStore(t)
	_, _, err := s.RoomByID(999999)
	if !errors.Is(err, room.ErrRoomNotFound) {
		t.Fatalf("want ErrRoomNotFound, got %v", err)
	}

}

func TestSaveNewRoomAndReadBack(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	r := &room.Room{RoomCode: "ABC123", Status: room.StatusWaiting, MaxPlayers: 8, Language: "en", CreatedAt: now}
	host, err := r.Join(room.RoomInput{}, 42, now)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := s.SaveNewRoom(r, host); err != nil {
		t.Fatalf("save: %v", err)
	}
	if r.RoomID == 0 {
		t.Fatal("RoomID must be set by the database")
	}

	got, members, err := s.RoomByID(r.RoomID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.RoomCode != "ABC123" || got.MaxPlayers != 8 || got.HostUserID == nil || *got.HostUserID != 42 {
		t.Fatalf("unexpected room: %+v", got)
	}
	if len(members) != 1 || members[0].UserID != 42 || members[0].RoomID != r.RoomID {
		t.Fatalf("unexpected members: %+v", members)
	}

	dup := &room.Room{RoomCode: "ABC123", Status: room.StatusWaiting, MaxPlayers: 8, Language: "en", CreatedAt: now}
	dupHost, _ := dup.Join(room.RoomInput{}, 43, now)
	if err := s.SaveNewRoom(dup, dupHost); !errors.Is(err, service.ErrRoomCodeTaken) {
		t.Fatalf("want ErrRoomCodeTaken, got %v", err)
	}
}

func seedRoom(t *testing.T, s *Store, code string, hostID int64) *room.Room {
	t.Helper()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	r := &room.Room{RoomCode: code, Status: room.StatusWaiting, MaxPlayers: 8, Language: "en", CreatedAt: now}
	host, err := r.Join(room.RoomInput{}, hostID, now)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := s.SaveNewRoom(r, host); err != nil {
		t.Fatalf("save new room: %v", err)
	}
	return r
}

func TestRoomByCode(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE01", 42)

	got, members, err := s.RoomByCode("CODE01")
	if err != nil || got.RoomID != r.RoomID || len(members) != 1 {
		t.Fatalf("by code: room %+v members %d err %v", got, len(members), err)
	}

	r.Status = room.StatusClosed
	if err := s.SaveRoom(r); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := s.RoomByCode("CODE01"); !errors.Is(err, room.ErrRoomNotFound) {
		t.Fatalf("closed room must not resolve, got %v", err)
	}
}

func TestActiveRoomOf(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE02", 42)

	roomID, ok, err := s.ActiveRoomOf(42)
	if err != nil || !ok || roomID != r.RoomID {
		t.Fatalf("host must have an active room: %d %v %v", roomID, ok, err)
	}
	roomID, ok, err = s.ActiveRoomOf(99)
	if err != nil || ok || roomID != 0 {
		t.Fatalf("stranger must have no active room: %d %v %v", roomID, ok, err)
	}
}

func TestSaveJoinAndDuplicateUser(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE03", 42)
	now := time.Date(2026, 11, 5, 12, 1, 0, 0, time.UTC)

	_, members, _ := s.RoomByID(r.RoomID)
	joined, err := r.Join(room.RoomInput{Members: members}, 43, now)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := s.SaveJoin(r, joined); err != nil {
		t.Fatalf("save join: %v", err)
	}
	_, members, _ = s.RoomByID(r.RoomID)
	if len(members) != 2 || members[1].UserID != 43 {
		t.Fatalf("want 2 members in join order, got %+v", members)
	}

	other := seedRoom(t, s, "CODE04", 50)
	again := room.RoomMember{RoomID: other.RoomID, UserID: 43, State: room.StateInLobby, JoinedAt: now}
	if err := s.SaveJoin(other, again); !errors.Is(err, room.ErrAlreadyInRoom) {
		t.Fatalf("user in another room must be rejected, got %v", err)
	}
}

func TestSaveMemberPersistsSeat(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE05", 42)
	now := time.Date(2026, 11, 5, 12, 2, 0, 0, time.UTC)

	_, members, _ := s.RoomByID(r.RoomID)
	seat, err := r.SelectRole(room.RoomInput{Members: members}, 42, game.TeamRed, game.RoleSpymaster, now)
	if err != nil {
		t.Fatalf("select role: %v", err)
	}
	if err := s.SaveMember(r, seat.Member); err != nil {
		t.Fatalf("save member: %v", err)
	}
	_, members, _ = s.RoomByID(r.RoomID)
	if members[0].Team == nil || *members[0].Team != game.TeamRed || members[0].Role == nil || *members[0].Role != game.RoleSpymaster {
		t.Fatalf("seat not persisted: %+v", members[0])
	}
}

func TestSaveRoomPersistsSettings(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE06", 42)

	timer := room.OneAndHalfMinute
	r.MaxPlayers = 10
	r.TurnTimerSeconds = &timer
	r.Language = "tr"
	if err := s.SaveRoom(r); err != nil {
		t.Fatalf("save room: %v", err)
	}
	got, _, _ := s.RoomByID(r.RoomID)
	if got.MaxPlayers != 10 || got.TurnTimerSeconds == nil || *got.TurnTimerSeconds != room.OneAndHalfMinute || got.Language != "tr" {
		t.Fatalf("settings not persisted: %+v", got)
	}
}

func TestSaveExpiredPostGameRemovesMembers(t *testing.T) {
	s := openTestStore(t)
	r := seedRoom(t, s, "CODE07", 42)
	now := time.Date(2026, 11, 5, 12, 3, 0, 0, time.UTC)
	for _, id := range []int64{43, 44} {
		_, members, _ := s.RoomByID(r.RoomID)
		m, err := r.Join(room.RoomInput{Members: members}, id, now)
		if err != nil {
			t.Fatalf("join %d: %v", id, err)
		}
		if err := s.SaveJoin(r, m); err != nil {
			t.Fatalf("save join %d: %v", id, err)
		}
	}

	if err := s.SaveExpiredPostGame(r, []int64{43, 44}); err != nil {
		t.Fatalf("expire: %v", err)
	}
	_, members, _ := s.RoomByID(r.RoomID)
	if len(members) != 1 || members[0].UserID != 42 {
		t.Fatalf("only host should remain, got %+v", members)
	}
	if _, ok, _ := s.ActiveRoomOf(43); ok {
		t.Fatal("removed member must have no active room")
	}
}
