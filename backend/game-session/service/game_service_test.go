package service

import (
	"errors"
	"testing"
	"time"

	"game-session/game"
	"game-session/room"
)

// Keep records separate so a rejected transaction cannot persist mutated objects.
type moveTestStore struct {
	RoomStore
	record  game.GameRecord
	members []room.RoomMember
	saveErr error
	saved   bool
}

func (s *moveTestStore) RoomByID(id int64) (*room.Room, []room.RoomMember, error) {
	return &room.Room{RoomID: id, Status: room.StatusInGame}, s.members, nil
}
func (s *moveTestStore) LoadCurrentGame(int64) (*game.Game, error) { return game.FromRecord(s.record) }
func (s *moveTestStore) SaveGame(g *game.Game) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.record = g.ToRecord()
	s.saved = true
	return nil
}

type moveTestProfiles struct{}

func (moveTestProfiles) ProfilesByUserIDs(ids []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile)
	for _, id := range ids {
		profiles[id] = room.Profile{Username: "player"}
	}
	return profiles, nil
}

func TestExpiredCommandsCommitTurnBeforeReturningRejection(t *testing.T) {
	calls := []struct {
		name string
		role game.Role
		call func(*RoomService, RoomStore, time.Time) error
	}{
		{"clue", game.RoleSpymaster, func(s *RoomService, tx RoomStore, now time.Time) error {
			_, err := s.GiveClue(tx, 1, 1, game.Clue{Word: "hint", Number: 1}, now)
			return err
		}},
		{"guess", game.RoleOperative, func(s *RoomService, tx RoomStore, now time.Time) error {
			_, err := s.GuessCard(tx, 1, 1, 1, now)
			return err
		}},
		{"pass", game.RoleOperative, func(s *RoomService, tx RoomStore, now time.Time) error {
			_, err := s.PassTurn(tx, 1, 1, now)
			return err
		}},
	}
	for _, tc := range calls {
		for _, failSave := range []bool{false, true} {
			name := tc.name
			if failSave {
				name += "/save_failure"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
				timer := time.Minute
				pack, err := game.DefaultWordPack()
				if err != nil {
					t.Fatal(err)
				}
				g, err := game.NewGameWithSeed(pack.Words, game.GameSettings{Language: "en", GameID: 1, TurnTimer: &timer, StaffingTimeout: time.Minute}, [32]byte{}, now)
				if err != nil {
					t.Fatal(err)
				}
				before := g.ToRecord()
				team := before.CurrentTurn.Team
				role := tc.role
				store := &moveTestStore{record: before, members: []room.RoomMember{{UserID: 1, Team: &team, Role: &role}}}
				dbErr := errors.New("save failed")
				if failSave {
					store.saveErr = dbErr
				}
				svc := NewRoomService(moveTestProfiles{}, nil)
				err = tc.call(svc, store, now.Add(timer))
				if failSave {
					if !errors.Is(err, dbErr) {
						t.Fatalf("want save error, got %v", err)
					}
					if store.saved || store.record.CurrentTurn.Team != team {
						t.Fatal("failed save changed the turn")
					}
					return
				}
				if !errors.Is(err, game.ErrTurnExpired) {
					t.Fatalf("want turn expired, got %v", err)
				}
				if !store.saved || store.record.CurrentTurn.Team == team {
					t.Fatal("expired turn was not saved")
				}
				if len(store.record.History) != len(before.History)+1 {
					t.Fatal("rejected command added unexpected history")
				}
				if store.record.CurrentTurn.Phase != game.PhaseWaitingForClue {
					t.Fatal("rejected command changed phase")
				}
			})
		}
	}
}
