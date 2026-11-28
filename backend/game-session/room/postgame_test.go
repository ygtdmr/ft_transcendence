package room

import (
	"testing"
	"time"
)

func TestPostGameExpiryRetainsEmptyRoom(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	host := int64(42)
	r := Room{Status: StatusPostGame, MaxPlayers: 4, HostUserID: &host, PostGameEndsAt: &now}
	result, err := r.ExpirePostGame(RoomInput{Members: []RoomMember{{UserID: host, State: StatePostGame}}}, now)
	if err != nil || len(result.RemovedUserIDs) != 1 {
		t.Fatalf("expiry: result=%+v err=%v", result, err)
	}
	if r.Status != StatusWaiting || r.HostUserID != nil || r.PostGameEndsAt != nil ||
		r.EmptySince == nil || !r.EmptySince.Equal(now) || r.CloseAt == nil || !r.CloseAt.Equal(now.Add(300*time.Second)) {
		t.Fatalf("unexpected retained room: %+v", r)
	}

	t.Run("closes at deadline", func(t *testing.T) {
		retained := r
		if retained.CloseIfEmpty(RoomInput{}, now.Add(299*time.Second)) {
			t.Fatal("room closed before deadline")
		}
		if !retained.CloseIfEmpty(RoomInput{}, now.Add(300*time.Second)) {
			t.Fatal("room did not close at deadline")
		}
	})
	t.Run("join cancels closure", func(t *testing.T) {
		retained := r
		member, err := retained.Join(RoomInput{}, 43, now.Add(time.Second))
		if err != nil || retained.HostUserID == nil || *retained.HostUserID != 43 || retained.CloseAt != nil || retained.EmptySince != nil {
			t.Fatalf("join: room=%+v err=%v", retained, err)
		}
		if retained.CloseIfEmpty(RoomInput{Members: []RoomMember{member}}, now.Add(300*time.Second)) {
			t.Fatal("occupied room closed")
		}
	})
}
