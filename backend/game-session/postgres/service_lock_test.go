package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"game-session/room"
	"game-session/service"
)

type fakeProfiles struct{}

func (fakeProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{Username: "user", AvatarURL: "/a.svg"}
	}
	return profiles, nil
}

// TestConcurrentJoinsCannotExceedCapacity checks that two joins racing for the last free seat
// cannot both succeed.
//
// The joins run inside Transaction, as every REST join does (rest/handler.go): RoomByID takes the
// room row's FOR UPDATE lock, and a lock only holds inside a transaction. Called on the bare store
// each statement commits on its own, the lock is released at once, and both joins pass the capacity
// check (joined=2 in about a third of the runs), so the test would prove nothing about the code.
func TestConcurrentJoinsCannotExceedCapacity(t *testing.T) {
	s := openTestStore(t)
	svc := service.NewRoomService(fakeProfiles{}, nil)
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	capacity := 4
	snapshot, err := svc.Create(s, 1, &capacity, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, id := range []int64{2, 3} {
		if _, err := svc.Join(s, snapshot.RoomID, id, now); err != nil {
			t.Fatalf("join %d: %v", id, err)
		}
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range []int64{4, 5} {
		wg.Add(1)
		go func(slot int, userID int64) {
			defer wg.Done()
			results[slot] = s.Transaction(func(tx service.RoomStore) error {
				_, err := svc.Join(tx, snapshot.RoomID, userID, now)
				return err
			})
		}(i, id)
	}
	wg.Wait()

	joined, full := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			joined++
		case errors.Is(err, room.ErrRoomFull):
			full++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if joined != 1 || full != 1 {
		t.Fatalf("want exactly one join and one ROOM_FULL, got joined=%d full=%d", joined, full)
	}
	_, members, _ := s.RoomByID(snapshot.RoomID)
	if len(members) != 4 {
		t.Fatalf("room must hold exactly 4 members, got %d", len(members))
	}
}
