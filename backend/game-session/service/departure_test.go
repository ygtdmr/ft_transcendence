package service

import (
	"errors"
	"testing"
	"time"

	"game-session/game"
	"game-session/room"
)

type unavailableProfiles struct{ calls int }

func (p *unavailableProfiles) ProfilesByUserIDs([]int64) (map[int64]room.Profile, error) {
	p.calls++
	return nil, errors.New("user service unavailable")
}

type departureStore struct {
	RoomStore
	r       room.Room
	members []room.RoomMember
	g       *game.Game
	saved   bool
	penalty *room.Penalty
}

func (s *departureStore) Transaction(fn func(RoomStore) error) error { return fn(s) }
func (s *departureStore) RoomByID(int64) (*room.Room, []room.RoomMember, error) {
	return &s.r, s.members, nil
}
func (s *departureStore) LoadCurrentGame(int64) (*game.Game, error) { return s.g, nil }
func (s *departureStore) SaveLeave(_ *room.Room, _ int64, g *game.Game, penalty *room.Penalty) error {
	s.saved = true
	s.g = g
	s.penalty = penalty
	return nil
}

func TestDeparturesUseStoredUsernameWithoutProfileService(t *testing.T) {
	for _, kick := range []bool{false, true} {
		name := "leave"
		if kick {
			name = "kick"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			pack, err := game.DefaultWordPack()
			if err != nil {
				t.Fatal(err)
			}
			g, err := game.NewGameWithSeed(pack.Words, game.GameSettings{Language: "en", GameID: 1, StaffingTimeout: 120 * time.Second}, [32]byte{}, now)
			if err != nil {
				t.Fatal(err)
			}
			red, blue := game.TeamRed, game.TeamBlue
			spy, op := game.RoleSpymaster, game.RoleOperative
			host := int64(1)
			store := &departureStore{
				r: room.Room{RoomID: 1, Status: room.StatusInGame, HostUserID: &host}, g: g,
				members: []room.RoomMember{
					{UserID: 1, Username: "host", Team: &red, Role: &op, State: room.StateInGame},
					{UserID: 2, Username: "stored_name", Team: &red, Role: &spy, State: room.StateInGame},
					{UserID: 3, Username: "blue_spy", Team: &blue, Role: &spy, State: room.StateInGame},
					{UserID: 4, Username: "blue_op", Team: &blue, Role: &op, State: room.StateInGame},
				},
			}
			profiles := &unavailableProfiles{}
			svc := NewRoomService(store, profiles)
			if kick {
				_, err = svc.Kick(1, 1, 2, now)
			} else {
				_, err = svc.Leave(1, 2, room.DepartureExited, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if profiles.calls != 0 || !store.saved || store.g == nil || !store.g.IsPaused() {
				t.Fatalf("profile calls=%d saved=%v game=%v", profiles.calls, store.saved, store.g)
			}
			history := store.g.ToRecord().History
			pause := history[len(history)-1]
			if pause.Type != game.HistoryGamePaused || pause.Username == nil || *pause.Username != "stored_name" {
				t.Fatalf("wrong pause history: %+v", pause)
			}
			if kick && store.penalty != nil {
				t.Fatal("kick must not create a penalty")
			}
			if !kick && (store.penalty == nil || store.penalty.UserID != 2) {
				t.Fatal("leave penalty missing")
			}
		})
	}
}
