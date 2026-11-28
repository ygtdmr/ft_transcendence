package room

import (
	"testing"
	"time"

	"game-session/game"
)

var graceNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newGraceRoom(status Status) (*Room, []RoomMember) {
	r := &Room{RoomID: 1, Status: status, MaxPlayers: 20}
	host := int64(1)
	r.HostUserID = &host
	members := []RoomMember{
		{RoomID: 1, UserID: 1, State: StateInLobby, JoinedAt: graceNow},
		{RoomID: 1, UserID: 2, State: StateInLobby, JoinedAt: graceNow},
	}
	return r, members
}

func TestDisconnectSetsGraceByStatus(t *testing.T) {
	cases := []struct {
		status Status
		want   time.Duration
	}{
		{StatusWaiting, lobbyGraceTimeout},
		{StatusCountDown, lobbyGraceTimeout},
		{StatusInGame, gameGraceTimeout},
	}
	for _, c := range cases {
		r, members := newGraceRoom(c.status)
		updated, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
		mustOK(t, "disconnect "+string(c.status), err)
		if updated.GraceEndsAt == nil || !updated.GraceEndsAt.Equal(graceNow.Add(c.want)) {
			t.Fatalf("%s: want grace %v, got %v", c.status, graceNow.Add(c.want), updated.GraceEndsAt)
		}
	}
}

func TestDisconnectKeepsSeatAndDoesNotExtend(t *testing.T) {
	r, members := newGraceRoom(StatusWaiting)
	team, role := game.TeamRed, game.RoleSpymaster
	members[1].Team = &team
	members[1].Role = &role
	members[1].Ready = true

	first, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "first disconnect", err)
	if first.Team == nil || first.Role == nil || !first.Ready {
		t.Fatalf("disconnect must keep team/role/ready, got %+v", first)
	}

	members = replaceMember(members, first)
	second, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow.Add(10*time.Second))
	mustOK(t, "second disconnect", err)
	if !second.GraceEndsAt.Equal(*first.GraceEndsAt) {
		t.Fatalf("second disconnect must not extend grace: first %v, second %v", first.GraceEndsAt, second.GraceEndsAt)
	}
}

func TestReconnectWithinGraceClearsDeadline(t *testing.T) {
	r, members := newGraceRoom(StatusInGame)
	gone, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "disconnect", err)
	members = replaceMember(members, gone)

	back, err := r.MarkReconnected(RoomInput{Members: members}, 2, graceNow.Add(59*time.Second))
	mustOK(t, "reconnect", err)
	if back.GraceEndsAt != nil {
		t.Fatalf("reconnect must clear grace, got %v", back.GraceEndsAt)
	}
}

func TestReconnectAfterGraceIsRejected(t *testing.T) {
	r, members := newGraceRoom(StatusInGame)
	gone, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "disconnect", err)
	members = replaceMember(members, gone)

	_, err = r.MarkReconnected(RoomInput{Members: members}, 2, graceNow.Add(gameGraceTimeout))
	mustErr(t, "late reconnect", err, ErrInvalidRoomState)
}

func TestPostGameMemberGetsNoGrace(t *testing.T) {
	r, members := newGraceRoom(StatusPostGame)
	members[1].State = StatePostGame

	updated, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "disconnect in post-game", err)
	if updated.GraceEndsAt != nil {
		t.Fatalf("post-game member must not get grace, got %v", updated.GraceEndsAt)
	}
}

func TestGraceErrors(t *testing.T) {
	r, members := newGraceRoom(StatusWaiting)
	_, err := r.MarkDisconnected(RoomInput{Members: members}, 99, graceNow)
	mustErr(t, "disconnect non-member", err, ErrNotRoomMember)
	_, err = r.MarkReconnected(RoomInput{Members: members}, 99, graceNow)
	mustErr(t, "reconnect non-member", err, ErrNotRoomMember)

	connected, err := r.MarkReconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "reconnect while connected", err)
	if connected.GraceEndsAt != nil {
		t.Fatalf("connected member must stay nil, got %v", connected.GraceEndsAt)
	}

	r.Status = StatusClosed
	_, err = r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustErr(t, "disconnect in closed room", err, ErrRoomNotFound)
}

func TestDisconnectedLeaveRequiresExpiredGrace(t *testing.T) {
	r, members := newGraceRoom(StatusWaiting)

	_, err := r.Leave(RoomInput{Members: members}, 2, DepartureDisconnected, graceNow)
	mustErr(t, "leave without grace", err, ErrGraceNotExpired)

	gone, err := r.MarkDisconnected(RoomInput{Members: members}, 2, graceNow)
	mustOK(t, "disconnect", err)
	members = replaceMember(members, gone)

	_, err = r.Leave(RoomInput{Members: members}, 2, DepartureDisconnected, graceNow.Add(10*time.Second))
	mustErr(t, "leave before grace ends", err, ErrGraceNotExpired)

	_, err = r.Leave(RoomInput{Members: members}, 2, DepartureDisconnected, graceNow.Add(lobbyGraceTimeout))
	mustOK(t, "leave after grace ends", err)
}
