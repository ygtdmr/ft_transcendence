package room

import (
	"encoding/json"
	"errors"
	"game-session/game"
	"os"
	"testing"
	"time"
)

var flowNow = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func loadFlowWordPack(t *testing.T) game.WordPack {
	t.Helper()
	data, err := os.ReadFile("../game/wordpack.json")
	if err != nil {
		t.Fatalf("read wordpack: %v", err)
	}
	var pack game.WordPack
	if err := json.Unmarshal(data, &pack); err != nil {
		t.Fatalf("parse wordpack: %v", err)
	}
	return pack
}

func mustErr(t *testing.T, step string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: want %v, got %v", step, want, err)
	}
}

func mustOK(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error %v", step, err)
	}
}

// join → seats → ready → countdown → start → leave (pause) → rejoin → play to a win
// → post-game → return to lobby → timeout → empty room.
func TestRoomFlow(t *testing.T) {
	pack := loadFlowWordPack(t)
	now := flowNow
	r := &Room{RoomID: 1001, RoomCode: "ABC123", Status: StatusWaiting, MaxPlayers: 20, Language: "en"}
	profiles := map[int64]Profile{}
	var members []RoomMember

	// --- join
	for id := int64(1); id <= 4; id++ {
		m, err := r.Join(RoomInput{Members: members}, id, now)
		mustOK(t, "join", err)
		members = append(members, m)
		profiles[id] = Profile{Username: "user" + string(rune('0'+id)), AvatarURL: "a"}
	}
	if r.HostUserID == nil || *r.HostUserID != 1 {
		t.Fatalf("host should be user 1, got %v", r.HostUserID)
	}
	_, err := r.Join(RoomInput{Members: members}, 2, now)
	mustErr(t, "join twice", err, ErrAlreadyInRoom)

	// --- seats
	seat := func(id int64, team game.Team, role game.Role) {
		t.Helper()
		res, err := r.SelectRole(RoomInput{Members: members}, id, team, role, now)
		mustOK(t, "select role", err)
		members = replaceMember(members, res.Member)
	}
	seat(1, game.TeamRed, game.RoleSpymaster)
	seat(2, game.TeamRed, game.RoleOperative)
	seat(3, game.TeamBlue, game.RoleSpymaster)
	seat(4, game.TeamBlue, game.RoleOperative)

	_, err = r.SelectRole(RoomInput{Members: members}, 2, game.TeamRed, game.RoleSpymaster, now)
	mustErr(t, "second spymaster", err, ErrRoleConflict)
	_, err = r.SelectRole(RoomInput{Members: members}, 2, "", game.RoleSpymaster, now)
	mustErr(t, "team required", err, ErrTeamRequired)

	same, err := r.SelectRole(RoomInput{Members: members}, 2, game.TeamRed, game.RoleOperative, now)
	mustOK(t, "same seat", err)
	if same.CountdownCancelled || same.Member.Ready {
		t.Fatalf("same seat must be a no-op")
	}

	res, err := r.SelectTeam(RoomInput{Members: members}, 2, game.TeamBlue, now)
	mustOK(t, "select team", err)
	if *res.Member.Team != game.TeamBlue || *res.Member.Role != game.RoleOperative {
		t.Fatalf("select team should keep role")
	}
	members = replaceMember(members, res.Member)
	res, err = r.SelectTeam(RoomInput{Members: members}, 2, game.TeamRed, now)
	mustOK(t, "select team back", err)
	members = replaceMember(members, res.Member)

	// --- ready and countdown
	ready := func(id int64, ready bool) ReadyResult {
		t.Helper()
		res, err := r.SetReady(RoomInput{Members: members}, id, ready, now)
		mustOK(t, "set ready", err)
		members = replaceMember(members, res.Member)
		return res
	}
	for id := int64(1); id <= 3; id++ {
		if rr := ready(id, true); rr.CountdownStarted {
			t.Fatalf("countdown must not start before everyone is ready")
		}
	}
	rr := ready(4, true)
	if !rr.CountdownStarted || r.Status != StatusCountDown || r.CountdownEndsAt == nil {
		t.Fatalf("countdown should start after last ready")
	}
	_, err = r.Join(RoomInput{Members: members}, 9, now)
	mustErr(t, "join during countdown", err, ErrRoomNotJoinable)
	rr = ready(1, false)
	if !rr.CountdownCancelled || r.Status != StatusWaiting {
		t.Fatalf("unready should cancel countdown")
	}

	// --- settings
	four, three, de := 4, 3, "de"
	_, err = r.UpdateSettings(RoomInput{Members: members}, 2, SettingsUpdate{MaxPlayers: &four})
	mustErr(t, "non-host settings", err, ErrNotHost)
	_, err = r.UpdateSettings(RoomInput{Members: members}, 1, SettingsUpdate{MaxPlayers: &three})
	mustErr(t, "capacity below members", err, ErrInvalidPayload)
	_, err = r.UpdateSettings(RoomInput{Members: members}, 1, SettingsUpdate{Language: &de})
	mustErr(t, "bad language", err, ErrInvalidPayload)
	_, err = r.UpdateSettings(RoomInput{Members: members}, 1, SettingsUpdate{})
	mustErr(t, "empty settings", err, ErrInvalidPayload)
	settings, err := r.UpdateSettings(RoomInput{Members: members}, 1, SettingsUpdate{MaxPlayers: &four, UpdateTurnTimer: true})
	mustOK(t, "settings", err)
	if settings.MaxPlayers != 4 || settings.TurnTimerSeconds != nil || r.MaxPlayers != 4 {
		t.Fatalf("settings not applied: %+v", settings)
	}
	_, err = r.Join(RoomInput{Members: members}, 9, now)
	mustErr(t, "room full", err, ErrRoomFull)

	// --- start
	ready(1, true)
	startAt := *r.CountdownEndsAt
	_, _, err = r.StartGame(RoomInput{Members: members}, pack.Words, 5001, now)
	mustErr(t, "start before countdown ends", err, ErrInvalidRoomState)
	g, inGame, err := r.StartGame(RoomInput{Members: members}, pack.Words, 5001, startAt)
	mustOK(t, "start game", err)
	members = inGame
	if r.Status != StatusInGame || g.GameID() != 5001 || members[0].State != StateInGame || !members[0].Ready {
		t.Fatalf("game did not start properly")
	}
	twenty := 20
	_, err = r.UpdateSettings(RoomInput{Members: members}, 1, SettingsUpdate{MaxPlayers: &twenty})
	mustErr(t, "settings in game", err, ErrInvalidRoomState)
	r.MaxPlayers = 20

	_, err = r.Join(RoomInput{Members: members, CurrentGame: g}, 5, startAt)
	mustErr(t, "join running game", err, ErrRoomNotJoinable)
	_, err = r.SelectRole(RoomInput{Members: members, CurrentGame: g}, 2, game.TeamBlue, game.RoleOperative, startAt)
	mustErr(t, "seat change in game", err, ErrInvalidRoomState)

	// Departures use the stored name and do not require a profile lookup.
	leaveAt := startAt.Add(10 * time.Second)
	for i := range members {
		members[i].Username = profiles[members[i].UserID].Username
	}
	lr, err := r.Leave(RoomInput{Members: members, CurrentGame: g}, 1, DepartureExited, leaveAt)
	mustOK(t, "leave spymaster", err)
	members = withoutMember(members, 1)
	if !lr.GamePaused || !g.IsPaused() {
		t.Fatalf("game should pause when spymaster leaves")
	}
	if lr.Penalty == nil || lr.Penalty.Role != game.RoleSpymaster || lr.Penalty.GameID != 5001 {
		t.Fatalf("penalty missing or wrong: %+v", lr.Penalty)
	}
	if lr.HostUserID == nil || *lr.HostUserID != 2 {
		t.Fatalf("host should transfer to user 2, got %v", lr.HostUserID)
	}

	snap, err := r.SnapshotForRoom(RoomInput{Members: members, CurrentGame: g, Profiles: profiles}, 2)
	mustOK(t, "snapshot paused", err)
	if snap.Game == nil || snap.Game.Staffing == nil || len(snap.Game.Staffing.Red.MissingRoles) != 1 || snap.Game.Staffing.Red.DeadlineAt == nil {
		t.Fatalf("snapshot should carry red staffing: %+v", snap.Game)
	}
	if snap.Game.Staffing.Blue.DeadlineAt != nil {
		t.Fatalf("blue should have no deadline")
	}

	// --- kick errors
	in := RoomInput{Members: members, CurrentGame: g, Profiles: profiles}
	_, err = r.Kick(in, 4, 3, leaveAt)
	mustErr(t, "kick by non-host", err, ErrNotHost)
	_, err = r.Kick(in, 2, 2, leaveAt)
	mustErr(t, "kick self", err, ErrCannotKickSelf)
	_, err = r.Kick(in, 2, 99, leaveAt)
	mustErr(t, "kick stranger", err, ErrTargetNotRoomMember)

	// --- rejoin into the paused game and fill the seat
	m5, err := r.Join(RoomInput{Members: members, CurrentGame: g}, 5, leaveAt)
	mustOK(t, "join paused game", err)
	if m5.State != StateInGame || m5.Team != nil {
		t.Fatalf("joiner should be unseated IN_GAME")
	}
	members = append(members, m5)
	profiles[5] = Profile{Username: "user5"}
	_, err = r.SelectRole(RoomInput{Members: members, CurrentGame: g}, 5, game.TeamBlue, game.RoleSpymaster, leaveAt)
	mustErr(t, "blue spymaster taken", err, ErrRoleConflict)
	sr, err := r.SelectRole(RoomInput{Members: members, CurrentGame: g}, 5, game.TeamRed, game.RoleSpymaster, leaveAt)
	mustOK(t, "claim red spymaster", err)
	members = replaceMember(members, sr.Member)
	if !sr.GameResumed || g.IsPaused() {
		t.Fatalf("game should resume")
	}

	// --- play: the starting team reveals all of its cards and wins
	spy := game.Player{UserID: 5, Username: "user5", Team: game.TeamRed, Role: game.RoleSpymaster}
	op := game.Player{UserID: 2, Username: "user2", Team: game.TeamRed, Role: game.RoleOperative}
	view, _ := g.SnapshotForRole(&spy.Role)
	if view.StartingTeam == game.TeamBlue {
		spy = game.Player{UserID: 3, Username: "user3", Team: game.TeamBlue, Role: game.RoleSpymaster}
		op = game.Player{UserID: 4, Username: "user4", Team: game.TeamBlue, Role: game.RoleOperative}
	}
	_, err = g.GiveClue(&spy, &game.Clue{Word: "zzz", Number: 9}, leaveAt)
	mustOK(t, "give clue", err)
	var last game.GuessResult
	for _, card := range view.Board {
		if card.Color != nil && game.Team(*card.Color) == view.StartingTeam {
			last, err = g.GuessCard(&op, card.CardID, leaveAt)
			mustOK(t, "guess", err)
		}
	}
	if last.Winner == nil || *last.Winner != view.StartingTeam {
		t.Fatalf("starting team should win, got %+v", last)
	}

	// --- end game → POST_GAME, last_game carries the stats fields too
	er, err := r.HandleGameEnd(RoomInput{Members: members, CurrentGame: g}, leaveAt)
	mustOK(t, "end game", err)
	members = er.Members
	if r.Status != StatusPostGame || er.LastGame == nil || er.LastGame.Winner != view.StartingTeam || members[0].State != StatePostGame || members[0].Ready {
		t.Fatalf("post game not set up: status=%s last=%+v", r.Status, er.LastGame)
	}
	if len(er.LastGame.Players) != 4 || er.LastGame.RoomID != 1001 || er.LastGame.Loser == er.LastGame.Winner {
		t.Fatalf("last game stats fields wrong: %+v", er.LastGame)
	}

	// --- return to lobby, pending ready, post-game leave, timeout
	ret, err := r.ReturnToLobby(RoomInput{Members: members}, 2, leaveAt)
	mustOK(t, "return to lobby", err)
	members = replaceMember(members, ret.Member)
	if r.Status != StatusWaiting || ret.PostGameCompleted {
		t.Fatalf("first return should move room to WAITING, others still pending")
	}
	_, err = r.SetReady(RoomInput{Members: members}, 2, true, leaveAt)
	mustErr(t, "ready while post-game pending", err, ErrPostGamePending)

	lr, err = r.Leave(RoomInput{Members: members}, 4, DepartureExited, leaveAt)
	mustOK(t, "post-game leave", err)
	members = withoutMember(members, 4)
	if lr.PostGameCompleted || lr.Penalty != nil {
		t.Fatalf("others still pending; no penalty outside IN_GAME")
	}

	tr, err := r.ExpirePostGame(RoomInput{Members: members}, leaveAt.Add(postGameTimeout))
	mustOK(t, "expire post game", err)
	if len(tr.RemovedUserIDs) != 2 || r.Status != StatusWaiting || r.PostGameEndsAt != nil {
		t.Fatalf("timeout should remove 3 and 5: %+v status=%s", tr, r.Status)
	}
	for _, id := range tr.RemovedUserIDs {
		members = withoutMember(members, id)
	}
	if r.HostUserID == nil || *r.HostUserID != 2 {
		t.Fatalf("host should stay user 2")
	}

	// --- last member leaves → empty-room closure timer
	lr, err = r.Leave(RoomInput{Members: members}, 2, DepartureExited, leaveAt)
	mustOK(t, "last leave", err)
	if lr.HostUserID != nil || r.EmptySince == nil || r.CloseAt == nil || !r.CloseAt.Equal(leaveAt.Add(emptyRoomTimeout)) {
		t.Fatalf("empty room should start closure timer")
	}
}
