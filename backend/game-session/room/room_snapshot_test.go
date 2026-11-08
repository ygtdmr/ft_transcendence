package room

import (
	"game-session/game"
	"testing"
	"time"
)

func newGameWithOneRevealedCard(t *testing.T, now time.Time) (*game.Game, game.Team) {
	t.Helper()
	pack := loadFlowWordPack(t)

	settings := game.GameSettings{Language: "tr", StaffingTimeout: staffingTimeout, GameID: 7001}
	g, err := game.NewGameWithSeed(pack.Words, settings, [32]byte{1}, now)
	mustOK(t, "new game", err)

	spyRole := game.RoleSpymaster
	view, err := g.SnapshotForRole(&spyRole)
	mustOK(t, "spymaster view", err)
	starting := view.StartingTeam

	spy := game.Player{UserID: 1, Username: "user1", Team: starting, Role: game.RoleSpymaster}
	op := game.Player{UserID: 2, Username: "user2", Team: starting, Role: game.RoleOperative}
	_, err = g.GiveClue(&spy, &game.Clue{Word: "zzz", Number: 9}, now)
	mustOK(t, "clue", err)
	res, err := g.GuessCard(&op, 1, now)
	mustOK(t, "guess", err)
	if res.Winner != nil {
		t.Fatalf("seed must not end the game on the first guess")
	}

	return g, starting
}

func newRoomWithGameAndPlayers(t *testing.T) (*Room, RoomInput) {
	t.Helper()
	now := flowNow
	g, starting := newGameWithOneRevealedCard(t, now)
	r := &Room{RoomID: 1001, RoomCode: "SNAP01", Status: StatusInGame, MaxPlayers: 10, Language: "tr"}
	spy := game.RoleSpymaster
	op := game.RoleOperative
	members := []RoomMember{
		{RoomID: 1001, UserID: 1, State: StateInGame, Team: &starting, Role: &spy, Ready: true, JoinedAt: now},
		{RoomID: 1001, UserID: 2, State: StateInGame, Team: &starting, Role: &op, Ready: true, JoinedAt: now},
	}
	profiles := map[int64]Profile{
		1: {Username: "user1", AvatarURL: "a"},
		2: {Username: "user2", AvatarURL: "a"},
	}
	input := RoomInput{Members: members, CurrentGame: g, Profiles: profiles}
	return r, input
}

func Test_SnapshotErrors(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)

	_, err := r.SnapshotForRoom(input, 99)
	mustErr(t, "non member", err, ErrNotRoomMember)

	r.Status = StatusClosed
	_, err = r.SnapshotForRoom(input, 1)
	mustErr(t, "closed room", err, ErrRoomNotFound)
}

func Test_SpySnapshot(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)
	snap, err := r.SnapshotForRoom(input, 1)
	mustOK(t, "spymaster snapshot", err)

	if snap.Game == nil {
		t.Fatalf("spymaster snapshot should include game data")
	}
	if len(snap.Game.Board) != game.BoardSize {
		t.Fatalf("board should have %d cards, got %d", game.BoardSize, len(snap.Game.Board))
	}

	revealedCount := 0
	for _, card := range snap.Game.Board {
		if card.Color == nil {
			t.Fatalf("spymaster must see color of card %d", card.CardID)
		}
		if card.Revealed {
			revealedCount++
		}
	}
	if revealedCount != 1 {
		t.Fatalf("spy | exactly one card should be revealed, got %d", revealedCount)
	}
}

func Test_OpSnapshot(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)
	snap, err := r.SnapshotForRoom(input, 2)
	mustOK(t, "operative snapshot", err)

	if snap.Game == nil {
		t.Fatalf("operative snapshot should include game data")
	}
	if len(snap.Game.Board) != game.BoardSize {
		t.Fatalf("board should have %d cards, got %d", game.BoardSize, len(snap.Game.Board))
	}
	revealedCount := 0
	for _, card := range snap.Game.Board {
		if card.Revealed {
			revealedCount++
			if card.Color == nil {
				t.Fatalf("Revealed card should have a color")
			}
		}
		if !card.Revealed && card.Color != nil {
			t.Fatalf("operative shouldnot see unrevealed card color")
		}
	}
	if revealedCount != 1 {
		t.Fatalf("op | exactly one card should be revealed, got %d", revealedCount)
	}
}

func Test_UnseatedSnapshot(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)
	input.Members = append(input.Members, RoomMember{RoomID: 1001, UserID: 3, State: StateInGame, Ready: false, JoinedAt: flowNow})
	input.Profiles[3] = Profile{Username: "user3", AvatarURL: "a"}
	snap, err := r.SnapshotForRoom(input, 3)
	mustOK(t, "unseated snapshot", err)

	if snap.Game == nil {
		t.Fatalf("unseated snapshot should include game data")
	}
	if len(snap.Game.Board) != game.BoardSize {
		t.Fatalf("board should have %d cards, got %d", game.BoardSize, len(snap.Game.Board))
	}
	revealedCount := 0
	for _, card := range snap.Game.Board {
		if card.Revealed {
			revealedCount++
			if card.Color == nil {
				t.Fatalf("unseated | Revealed card should have a color")
			}
		}
		if !card.Revealed && card.Color != nil {
			t.Fatalf("unseated shouldnot see unrevealed card color")
		}
	}
	if revealedCount != 1 {
		t.Fatalf("unseated | exactly one card should be revealed, got %d", revealedCount)
	}
}

func finishGameWithAssassin(t *testing.T, r *Room, input *RoomInput) GameEndResult {
	t.Helper()
	g := input.CurrentGame

	spy := game.RoleSpymaster
	view, err := g.SnapshotForRole(&spy)
	mustOK(t, "spymaster view", err)
	op := game.Player{UserID: 2, Username: "user2", Team: view.CurrentTurn.Team, Role: game.RoleOperative}
	var last game.GuessResult
	for _, card := range view.Board {
		if card.Color != nil && *card.Color == game.CardColorAssassin {
			last, err = g.GuessCard(&op, card.CardID, flowNow)
			mustOK(t, "guess assassin", err)
		}
	}
	if last.Winner == nil {
		t.Fatalf("assassin should end the game")
	}

	endResult, err := r.HandleGameEnd(*input, flowNow)
	mustOK(t, "handle game end", err)
	input.Members = endResult.Members
	return endResult
}

func Test_PostGameSnapshot(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)
	endResult := finishGameWithAssassin(t, r, &input)
	snap, err := r.SnapshotForRoom(input, 1)
	mustOK(t, "post game snapshot", err)
	if snap.Status != StatusPostGame {
		t.Fatalf("room status should be POST_GAME, got %s", snap.Status)
	}
	if snap.Game == nil || snap.Game.Winner == nil || *snap.Game.Winner != endResult.LastGame.Winner {
		t.Fatalf("post game snapshot should carry the finished game with its winner")
	}
	if snap.PostGame == nil || len(snap.PostGame.PendingUserIDs) != 2 {
		t.Fatalf("post game view should list both pending members, got %+v", snap.PostGame)
	}
	if snap.LastGame != nil {
		t.Fatalf("last_game must not be sent to a POST_GAME member")
	}
}

func Test_InLobbyWithLastGameSnapshot(t *testing.T) {
	r, input := newRoomWithGameAndPlayers(t)
	endResult := finishGameWithAssassin(t, r, &input)

	ret, err := r.ReturnToLobby(input, 2, flowNow)
	mustOK(t, "return to lobby", err)
	input.Members = replaceMember(input.Members, ret.Member)
	input.LastGame = endResult.LastGame

	snap, err := r.SnapshotForRoom(input, 2)
	mustOK(t, "in lobby snapshot", err)
	if snap.Status != StatusWaiting {
		t.Fatalf("room status should be WAITING after first return, got %s", snap.Status)
	}
	if snap.Game != nil {
		t.Fatalf("in lobby member must not receive game data")
	}
	if snap.LastGame == nil || snap.LastGame.GameID != 7001 || snap.LastGame.Winner != endResult.LastGame.Winner {
		t.Fatalf("in lobby member should receive last_game, got %+v", snap.LastGame)
	}
	if snap.PostGame == nil || len(snap.PostGame.PendingUserIDs) != 1 || snap.PostGame.PendingUserIDs[0] != 1 {
		t.Fatalf("post_game should still list user 1 as pending, got %+v", snap.PostGame)
	}

	pendingSnap, err := r.SnapshotForRoom(input, 1)
	mustOK(t, "pending member snapshot", err)
	if pendingSnap.Status != StatusWaiting {
		t.Fatalf("pending member sees the shared room status WAITING, got %s", pendingSnap.Status)
	}
	if pendingSnap.Game == nil || pendingSnap.Game.Winner == nil {
		t.Fatalf("pending POST_GAME member must still receive the finished game")
	}
	if pendingSnap.LastGame != nil {
		t.Fatalf("pending POST_GAME member must not receive last_game")
	}
	if pendingSnap.PostGame == nil || len(pendingSnap.PostGame.PendingUserIDs) != 1 {
		t.Fatalf("post_game should still be present for the pending member, got %+v", pendingSnap.PostGame)
	}
}
