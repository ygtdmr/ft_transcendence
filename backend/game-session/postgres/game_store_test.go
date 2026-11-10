package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"game-session/game"
	"game-session/room"
)

var gameNow = time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC)

type seatPlan struct {
	userID int64
	team   game.Team
	role   game.Role
}

var fourSeats = []seatPlan{
	{1, game.TeamRed, game.RoleSpymaster},
	{2, game.TeamRed, game.RoleOperative},
	{3, game.TeamBlue, game.RoleSpymaster},
	{4, game.TeamBlue, game.RoleOperative},
}

// Oda kur, 4 kişi katılsın, koltuk seçsin, hazır olsun, geri sayım bitsin, oyun başlasın. Hepsi depo üzerinden.
func startedGame(t *testing.T, s *Store) (*room.Room, *game.Game) {
	t.Helper()
	r := seedRoom(t, s, "GAME01", 1)
	now := gameNow

	for _, seat := range fourSeats[1:] {
		_, members, _ := s.RoomByID(r.RoomID)
		m, err := r.Join(room.RoomInput{Members: members}, seat.userID, now)
		if err != nil {
			t.Fatalf("join %d: %v", seat.userID, err)
		}
		if err := s.SaveJoin(r, m); err != nil {
			t.Fatalf("save join %d: %v", seat.userID, err)
		}
	}
	for _, seat := range fourSeats {
		_, members, _ := s.RoomByID(r.RoomID)
		res, err := r.SelectRole(room.RoomInput{Members: members}, seat.userID, seat.team, seat.role, now)
		if err != nil {
			t.Fatalf("select role %d: %v", seat.userID, err)
		}
		if err := s.SaveMember(r, res.Member); err != nil {
			t.Fatalf("save seat %d: %v", seat.userID, err)
		}
	}
	for _, seat := range fourSeats {
		_, members, _ := s.RoomByID(r.RoomID)
		res, err := r.SetReady(room.RoomInput{Members: members}, seat.userID, true, now)
		if err != nil {
			t.Fatalf("ready %d: %v", seat.userID, err)
		}
		if err := s.SaveMember(r, res.Member); err != nil {
			t.Fatalf("save ready %d: %v", seat.userID, err)
		}
	}
	if r.CountdownEndsAt == nil {
		t.Fatal("countdown must have started")
	}

	startAt := *r.CountdownEndsAt
	_, members, _ := s.RoomByID(r.RoomID)
	pack, err := game.DefaultWordPack()
	if err != nil {
		t.Fatalf("word pack: %v", err)
	}
	gameID, err := s.NextGameID()
	if err != nil {
		t.Fatalf("next game id: %v", err)
	}
	g, inGame, err := r.StartGame(room.RoomInput{Members: members}, pack.Words, gameID, startAt)
	if err != nil {
		t.Fatalf("start game: %v", err)
	}
	if err := s.SaveGameStart(r, inGame, g); err != nil {
		t.Fatalf("save game start: %v", err)
	}
	return r, g
}

func playerFor(t *testing.T, g *game.Game, team game.Team, role game.Role) *game.Player {
	t.Helper()
	for _, seat := range fourSeats {
		if seat.team == team && seat.role == role {
			return &game.Player{UserID: seat.userID, Username: "p", Team: team, Role: role}
		}
	}
	t.Fatalf("no seat for %s %s", team, role)
	return nil
}

func TestSaveGameStartAndLoadCurrentGame(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)

	loaded, err := s.LoadCurrentGame(r.RoomID)
	if err != nil || loaded == nil {
		t.Fatalf("game of: %v %v", loaded, err)
	}
	want, got := g.ToRecord(), loaded.ToRecord()
	if got.GameID != want.GameID || got.StartingTeam != want.StartingTeam ||
		got.CurrentTurn.Team != want.CurrentTurn.Team || got.CurrentTurn.Phase != want.CurrentTurn.Phase ||
		!got.StartedAt.Equal(want.StartedAt) || len(got.Cards) != 25 || len(got.History) != len(want.History) {
		t.Fatalf("round-trip mismatch:\nwant %+v\ngot  %+v", want, got)
	}
	for i := range want.Cards {
		if got.Cards[i] != want.Cards[i] {
			t.Fatalf("card %d mismatch: want %+v got %+v", i, want.Cards[i], got.Cards[i])
		}
	}

	got2, members, _ := s.RoomByID(r.RoomID)
	if got2.Status != room.StatusInGame {
		t.Fatalf("room must be IN_GAME, got %s", got2.Status)
	}
	for _, m := range members {
		if m.State != room.StateInGame {
			t.Fatalf("member %d must be IN_GAME, got %s", m.UserID, m.State)
		}
	}
}

func TestSaveGamePersistsMove(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)
	team := g.ToRecord().CurrentTurn.Team
	spy := playerFor(t, g, team, game.RoleSpymaster)
	op := playerFor(t, g, team, game.RoleOperative)
	now := gameNow.Add(10 * time.Second)

	if _, err := g.GiveClue(spy, &game.Clue{Word: "hint", Number: 2}, now); err != nil {
		t.Fatalf("clue: %v", err)
	}
	if _, err := g.GuessCard(op, 1, now.Add(time.Second)); err != nil {
		t.Fatalf("guess: %v", err)
	}
	if err := s.SaveGame(g); err != nil {
		t.Fatalf("save game: %v", err)
	}

	loaded, err := s.LoadCurrentGame(r.RoomID)
	if err != nil {
		t.Fatalf("game of: %v", err)
	}
	want, got := g.ToRecord(), loaded.ToRecord()
	if got.CurrentTurn.Phase != want.CurrentTurn.Phase || got.CurrentTurn.Team != want.CurrentTurn.Team {
		t.Fatalf("turn mismatch: want %+v got %+v", want.CurrentTurn, got.CurrentTurn)
	}
	if !got.Cards[0].Revealed {
		t.Fatal("card 1 must be revealed after save")
	}
	if len(got.History) != len(want.History) {
		t.Fatalf("history: want %d entries, got %d", len(want.History), len(got.History))
	}
}

func TestSaveGameEndSetsLastGameAndOutbox(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)

	now := gameNow.Add(10 * time.Second)
	team := g.ToRecord().CurrentTurn.Team
	revealAllTeamCards(t, g, now)

	_, members, _ := s.RoomByID(r.RoomID)
	result, err := r.HandleGameEnd(room.RoomInput{Members: members, CurrentGame: g}, now)
	if err != nil {
		t.Fatalf("handle end: %v", err)
	}
	if err := s.SaveGameEnd(r, result.Members, g, result.LastGame); err != nil {
		t.Fatalf("save game end: %v", err)
	}

	last, err := s.LoadLastGameResult(r.RoomID)
	if err != nil || last == nil {
		t.Fatalf("last game: %v %v", last, err)
	}
	if last.Winner != team || last.EndReason != game.EndReasonAllTeamCardsRevealed || len(last.Players) != 4 {
		t.Fatalf("unexpected last game: %+v", last)
	}
	if last.Score != g.Score() {
		t.Fatalf("score: want %+v got %+v", g.Score(), last.Score)
	}

	still, err := s.LoadCurrentGame(r.RoomID)
	if err != nil || still == nil || still.Status() != game.GameStatusFinished {
		t.Fatalf("post-game members still need the finished game: %v %v", still, err)
	}

	var events []outboxRow
	if err := s.db.Where("room_id = ?", r.RoomID).Find(&events).Error; err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "match.finalized" || events[0].PublishedAt != nil {
		t.Fatalf("want one unpublished match.finalized event, got %+v", events)
	}
}

func TestSaveLeaveInGameWritesPenaltyAndPause(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)
	now := gameNow.Add(10 * time.Second)

	_, members, _ := s.RoomByID(r.RoomID)
	profiles := map[int64]room.Profile{2: {Username: "two"}}
	result, err := r.Leave(room.RoomInput{Members: members, CurrentGame: g, Profiles: profiles}, 2, room.DepartureExited, now)
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if result.Penalty == nil || !result.GamePaused {
		t.Fatalf("seated in-game exit must penalize and pause, got %+v", result)
	}
	result.Penalty.Points = 20
	result.Penalty.PenalizedAt = now
	if err := s.SaveLeave(r, 2, g, result.Penalty); err != nil {
		t.Fatalf("save leave: %v", err)
	}

	if _, ok, _ := s.ActiveRoomOf(2); ok {
		t.Fatal("departed member must have no active room")
	}
	loaded, err := s.LoadCurrentGame(r.RoomID)
	if err != nil || loaded == nil || !loaded.IsPaused() {
		t.Fatalf("pause must be persisted: %v %v", loaded, err)
	}
	var events []outboxRow
	s.db.Where("room_id = ? AND event_type = ?", r.RoomID, "player.penalized").Find(&events)
	if len(events) != 1 {
		t.Fatalf("want one player.penalized event, got %d", len(events))
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].OutboxContent, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	for _, key := range []string{"user_id", "room_id", "game_id", "role", "reason", "points", "penalized_at"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %q: %s", key, events[0].OutboxContent)
		}
	}
	if payload["user_id"] != float64(2) || payload["points"] != float64(20) || payload["role"] != "OPERATIVE" {
		t.Fatalf("unexpected payload values: %s", events[0].OutboxContent)
	}
}

func TestNextGameIDIncrements(t *testing.T) {
	s := openTestStore(t)
	a, err := s.NextGameID()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	b, err := s.NextGameID()
	if err != nil || b <= a {
		t.Fatalf("ids must increase: %d %d %v", a, b, err)
	}
}

func revealAllTeamCards(t *testing.T, g *game.Game, now time.Time) {
	t.Helper()
	team := g.ToRecord().CurrentTurn.Team
	spy := playerFor(t, g, team, game.RoleSpymaster)
	op := playerFor(t, g, team, game.RoleOperative)
	spyRole := game.RoleSpymaster
	snapshot, _ := g.SnapshotForRole(&spyRole)
	if _, err := g.GiveClue(spy, &game.Clue{Word: "hint", Number: 9}, now); err != nil {
		t.Fatalf("clue: %v", err)
	}
	for _, card := range snapshot.Board {
		if card.Color == nil || game.Team(*card.Color) != team {
			continue
		}
		if _, err := g.GuessCard(op, card.CardID, now); err != nil {
			t.Fatalf("guess %d: %v", card.CardID, err)
		}
	}
	if g.Status() != game.GameStatusFinished {
		t.Fatalf("game must be finished, got %s", g.Status())
	}
}

func countOutbox(t *testing.T, s *Store, roomID int64, eventType string) int {
	t.Helper()
	var n int64
	if err := s.db.Model(&outboxRow{}).Where("room_id = ? AND event_type = ?", roomID, eventType).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", eventType, err)
	}
	return int(n)
}

func TestGameEndFinalizesOnlyOnce(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)
	now := gameNow.Add(10 * time.Second)
	revealAllTeamCards(t, g, now)

	_, members, _ := s.RoomByID(r.RoomID)
	result, err := r.HandleGameEnd(room.RoomInput{Members: members, CurrentGame: g}, now)
	if err != nil {
		t.Fatalf("first handle end: %v", err)
	}
	if err := s.SaveGameEnd(r, result.Members, g, result.LastGame); err != nil {
		t.Fatalf("first save: %v", err)
	}

	again, members2, _ := s.RoomByID(r.RoomID)
	g2, _ := s.LoadCurrentGame(r.RoomID)
	_, err = again.HandleGameEnd(room.RoomInput{Members: members2, CurrentGame: g2}, now.Add(time.Second))
	if !errors.Is(err, room.ErrInvalidRoomState) {
		t.Fatalf("second finalization must be rejected with ErrInvalidRoomState, got %v", err)
	}

	if n := countOutbox(t, s, r.RoomID, "match.finalized"); n != 1 {
		t.Fatalf("want exactly one match.finalized, got %d", n)
	}
}

func TestStaffingCancellationEmitsOnlyGameCancelled(t *testing.T) {
	s := openTestStore(t)
	r, g := startedGame(t, s)
	now := gameNow.Add(10 * time.Second)

	_, members, _ := s.RoomByID(r.RoomID)
	profiles := map[int64]room.Profile{1: {Username: "one"}}
	left, err := r.Leave(room.RoomInput{Members: members, CurrentGame: g, Profiles: profiles}, 1, room.DepartureExited, now)
	if err != nil || !left.GamePaused {
		t.Fatalf("spymaster leave must pause the game: %v %+v", err, left)
	}
	if err := s.SaveLeave(r, 1, g, left.Penalty); err != nil {
		t.Fatalf("save leave: %v", err)
	}

	later := now.Add(121 * time.Second)
	r2, members2, _ := s.RoomByID(r.RoomID)
	g2, _ := s.LoadCurrentGame(r.RoomID)
	cancelled, err := g2.CancelIfStaffingExpired(later)
	if err != nil || !cancelled {
		t.Fatalf("staffing deadline must cancel the game: %v %v", cancelled, err)
	}
	result, err := r2.HandleGameEnd(room.RoomInput{Members: members2, CurrentGame: g2}, later)
	if err != nil || !result.RoomClosed || result.LastGame != nil {
		t.Fatalf("cancellation must close the room without a last game: %v %+v", err, result)
	}
	if err := s.SaveGameEnd(r2, result.Members, g2, result.LastGame); err != nil {
		t.Fatalf("save cancelled end: %v", err)
	}

	if n := countOutbox(t, s, r.RoomID, "game.cancelled"); n != 1 {
		t.Fatalf("want exactly one game.cancelled, got %d", n)
	}
	if n := countOutbox(t, s, r.RoomID, "match.finalized"); n != 0 {
		t.Fatalf("cancellation must not emit match.finalized, got %d", n)
	}
	last, err := s.LoadLastGameResult(r.RoomID)
	if err != nil || last != nil {
		t.Fatalf("cancelled game must not become last_game: %v %+v", err, last)
	}
	closed, remaining, _ := s.RoomByID(r.RoomID)
	if closed.Status != room.StatusClosed || len(remaining) != 0 {
		t.Fatalf("room must be CLOSED and empty, got %s with %d members", closed.Status, len(remaining))
	}
}
