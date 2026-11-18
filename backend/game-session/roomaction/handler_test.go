package roomaction_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"game-session/game"
	"game-session/postgres"
	"game-session/room"
	"game-session/roomaction"
	"game-session/service"
)

var now = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

type placeholderProfiles struct{}

func (placeholderProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{Username: fmt.Sprintf("user-%d", id), AvatarURL: fmt.Sprintf("/static/avatars/default/%d.svg", id)}
	}
	return profiles, nil
}

type harness struct {
	store   *postgres.Store
	svc     *service.RoomService
	actions *roomaction.Handler
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
	svc := service.NewRoomService(placeholderProfiles{}, nil)
	return harness{store: store, svc: svc, actions: roomaction.New(svc, placeholderProfiles{})}
}

// Every action runs inside one transaction, the way the callers open it.
func (h harness) run(action func(tx service.RoomStore) (roomaction.Outcome, error)) (roomaction.Outcome, error) {
	var outcome roomaction.Outcome
	err := h.store.Transaction(func(tx service.RoomStore) error {
		var err error
		outcome, err = action(tx)
		return err
	})
	return outcome, err
}

// Four seated and ready members: the room is in COUNTDOWN. turnTimer nil keeps turns untimed.
func readyRoom(t *testing.T, h harness, turnTimer *room.TurnTimer) int64 {
	t.Helper()
	capacity := 4
	snapshot, err := h.svc.Create(h.store, 1, &capacity, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := snapshot.RoomID
	if turnTimer != nil {
		update := room.SettingsUpdate{UpdateTurnTimer: true, TurnTimerSeconds: turnTimer}
		if _, err := h.svc.UpdateSettings(h.store, roomID, 1, update); err != nil {
			t.Fatalf("settings: %v", err)
		}
	}
	for _, id := range []int64{2, 3, 4} {
		if _, err := h.svc.Join(h.store, roomID, id, now); err != nil {
			t.Fatalf("join %d: %v", id, err)
		}
	}
	seats := []struct {
		user int64
		team game.Team
		role game.Role
	}{{1, game.TeamRed, game.RoleSpymaster}, {2, game.TeamRed, game.RoleOperative}, {3, game.TeamBlue, game.RoleSpymaster}, {4, game.TeamBlue, game.RoleOperative}}
	for _, s := range seats {
		if _, err := h.svc.SelectRole(h.store, roomID, s.user, s.team, s.role, now); err != nil {
			t.Fatalf("seat %d: %v", s.user, err)
		}
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if _, err := h.svc.SetReady(h.store, roomID, id, true, now); err != nil {
			t.Fatalf("ready %d: %v", id, err)
		}
	}
	return roomID
}

// Spymaster gives a clue for 9, the operative reveals all 9 starting-team cards: the game ends.
func finishGame(t *testing.T, h harness, roomID int64, at time.Time) {
	t.Helper()
	snapshot, err := h.svc.Snapshot(h.store, roomID, 1, at)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	spymaster, operative := int64(1), int64(2)
	if snapshot.Game.StartingTeam == game.TeamBlue {
		spymaster, operative = 3, 4
	}
	if _, err := h.svc.GiveClue(h.store, roomID, spymaster, game.Clue{Word: "qwertyuiop", Number: 9}, at); err != nil {
		t.Fatalf("clue: %v", err)
	}
	spymasterView, err := h.svc.Snapshot(h.store, roomID, spymaster, at)
	if err != nil {
		t.Fatalf("spymaster snapshot: %v", err)
	}
	for _, card := range spymasterView.Game.Board {
		if card.Color != nil && string(*card.Color) == string(snapshot.Game.StartingTeam) {
			if _, err := h.svc.GuessCard(h.store, roomID, operative, card.CardID, at); err != nil {
				t.Fatalf("guess %d: %v", card.CardID, err)
			}
		}
	}
}

// Outbox rows written after the first `skip` rows.
func eventsAfter(t *testing.T, h harness, roomID int64, skip int) []postgres.OutboxEvent {
	t.Helper()
	events, err := h.store.PendingRecipientEvents(roomID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return events[skip:]
}

func countEvents(events []postgres.OutboxEvent, eventType string) int {
	n := 0
	for _, e := range events {
		if e.EventType == eventType {
			n++
		}
	}
	return n
}

func recipientsOf(events []postgres.OutboxEvent, eventType string) map[int64]bool {
	recipients := map[int64]bool{}
	for _, e := range events {
		if e.EventType == eventType {
			recipients[e.RecipientUserID] = true
		}
	}
	return recipients
}

func payloadOf(t *testing.T, events []postgres.OutboxEvent, eventType string) map[string]any {
	t.Helper()
	for _, e := range events {
		if e.EventType == eventType {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("payload of %s: %v", eventType, err)
			}
			return payload
		}
	}
	return nil
}

func eventTypesFor(events []postgres.OutboxEvent, recipientUserID int64) []string {
	var types []string
	for _, e := range events {
		if e.RecipientUserID == recipientUserID {
			types = append(types, e.EventType)
		}
	}
	return types
}

func hiddenCards(t *testing.T, payload map[string]any) int {
	t.Helper()
	board := payload["room"].(map[string]any)["game"].(map[string]any)["board"].([]any)
	hidden := 0
	for _, card := range board {
		if card.(map[string]any)["color"] == nil {
			hidden++
		}
	}
	return hidden
}

func TestJoinAndLeaveTellTheOtherMembers(t *testing.T) {
	h := newHarness(t)
	capacity := 4
	created, err := h.svc.Create(h.store, 1, &capacity, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := created.RoomID

	joined, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) { return h.actions.Join(tx, roomID, 2, now) })
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if snapshot, ok := joined.ActorReply.(room.RoomSnapshot); !ok || snapshot.PlayerCount != 2 {
		t.Fatalf("join reply must be the newcomer's snapshot, got %+v", joined.ActorReply)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "room.player.joined") != 2 || countEvents(events, "room.state") != 2 {
		t.Fatalf("join events: %+v", events)
	}
	if payloadOf(t, events, "room.player.joined")["player"].(map[string]any)["user_id"] != float64(2) {
		t.Fatalf("joined payload must describe user 2")
	}
	written := len(events)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.Leave(tx, roomID, 2, room.DepartureExited, now)
	}); err != nil {
		t.Fatalf("leave: %v", err)
	}
	events = eventsAfter(t, h, roomID, written)
	if countEvents(events, "room.player.left") != 1 || !recipientsOf(events, "room.player.left")[1] {
		t.Fatalf("only the remaining host must hear player.left: %+v", events)
	}
	if payloadOf(t, events, "room.player.left")["reason"] != string(room.DepartureExited) {
		t.Fatalf("left reason: %+v", payloadOf(t, events, "room.player.left"))
	}
	if !recipientsOf(events, "socket.close")[2] || payloadOf(t, events, "socket.close")["code"] != float64(4403) {
		t.Fatalf("departed member's socket must close with 4403: %+v", events)
	}
	written += len(events)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.Leave(tx, roomID, 1, room.DepartureExited, now)
	}); err != nil {
		t.Fatalf("last leave: %v", err)
	}
	events = eventsAfter(t, h, roomID, written)
	if len(events) != 1 || events[0].EventType != "socket.close" {
		t.Fatalf("empty room has nobody to tell, only the socket closes: %+v", events)
	}
}

// A departure during COUNTDOWN: countdown.cancelled comes first, then player.left, then room.state.
func TestDepartureDuringCountdownCancelsFirst(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h, nil)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.Leave(tx, roomID, 2, room.DepartureExited, now)
	}); err != nil {
		t.Fatalf("leave: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	want := []string{"room.countdown.cancelled", "room.player.left", "room.state"}
	got := eventTypesFor(events, 1)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order for the host: want %v, got %v", want, got)
	}
	if payloadOf(t, events, "room.countdown.cancelled")["reason"] != "PLAYER_LEFT" {
		t.Fatalf("cancel reason: %+v", payloadOf(t, events, "room.countdown.cancelled"))
	}
	written := len(events)

	// User 2 comes back and gets ready (the other three still are), countdown restarts; then the host kicks.
	if _, err := h.svc.Join(h.store, roomID, 2, now); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if _, err := h.svc.SelectRole(h.store, roomID, 2, game.TeamRed, game.RoleOperative, now); err != nil {
		t.Fatalf("seat: %v", err)
	}
	if _, err := h.svc.SetReady(h.store, roomID, 2, true, now); err != nil {
		t.Fatalf("ready 2: %v", err)
	}
	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.KickMember(tx, roomID, 1, 2, now)
	}); err != nil {
		t.Fatalf("kick: %v", err)
	}
	events = eventsAfter(t, h, roomID, written)
	got = eventTypesFor(events, 3)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order after kick: want %v, got %v", want, got)
	}
	if payloadOf(t, events, "room.countdown.cancelled")["reason"] != "PLAYER_KICKED" {
		t.Fatalf("kick cancel reason: %+v", payloadOf(t, events, "room.countdown.cancelled"))
	}
}

func TestStartGameDeadlineSendsEachMemberTheirOwnBoard(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h, nil)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineStartGame, roomID, 0, now.Add(5*time.Second))
	}); err != nil {
		t.Fatalf("start game: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "game.started") != 4 || countEvents(events, "room.state") != 4 {
		t.Fatalf("start events: %+v", events)
	}
	for _, e := range events {
		if e.EventType != "game.started" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		hidden := hiddenCards(t, payload)
		isSpymaster := e.RecipientUserID == 1 || e.RecipientUserID == 3
		if isSpymaster && hidden != 0 || !isSpymaster && hidden != 25 {
			t.Fatalf("user %d sees %d hidden cards", e.RecipientUserID, hidden)
		}
	}
}

func TestExpireTurnDeadlineChangesTurnOnlyWhenTimerRanOut(t *testing.T) {
	h := newHarness(t)
	timer := room.OneMinute
	roomID := readyRoom(t, h, &timer)
	startAt := now.Add(5 * time.Second)
	started, err := h.svc.StartGame(h.store, roomID, startAt)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	startingTeam := started.ToRecord().CurrentTurn.Team

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireTurn, roomID, 0, startAt.Add(30*time.Second))
	}); err != nil {
		t.Fatalf("timer still running: %v", err)
	}
	if events := eventsAfter(t, h, roomID, 0); len(events) != 0 {
		t.Fatalf("timer still running, no events expected: %+v", events)
	}

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireTurn, roomID, 0, startAt.Add(61*time.Second))
	}); err != nil {
		t.Fatalf("expire turn: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "game.turn.changed") != 4 || countEvents(events, "room.state") != 4 {
		t.Fatalf("turn events: %+v", events)
	}
	payload := payloadOf(t, events, "game.turn.changed")
	if payload["previous_team"] != string(startingTeam) || payload["reason"] != string(game.ChangeReasonTurnTimerExpired) {
		t.Fatalf("turn.changed payload: %+v", payload)
	}
}

func TestExpireGraceDeadlineRemovesTheDisconnectedMember(t *testing.T) {
	h := newHarness(t)
	capacity := 4
	created, _ := h.svc.Create(h.store, 1, &capacity, now)
	roomID := created.RoomID
	if _, err := h.svc.Join(h.store, roomID, 2, now); err != nil {
		t.Fatalf("join: %v", err)
	}
	err := h.store.Transaction(func(tx service.RoomStore) error {
		return h.actions.HandleLastSocketClosed(tx, roomID, 2, now)
	})
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireGrace, roomID, 2, now.Add(10*time.Second))
	}); err != nil {
		t.Fatalf("grace still running: %v", err)
	}
	if events := eventsAfter(t, h, roomID, 0); len(events) != 0 {
		t.Fatalf("grace still running, no events expected: %+v", events)
	}

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireGrace, roomID, 2, now.Add(31*time.Second))
	}); err != nil {
		t.Fatalf("expire grace: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "room.player.left") != 1 || payloadOf(t, events, "room.player.left")["reason"] != string(room.DepartureDisconnected) {
		t.Fatalf("grace events: %+v", events)
	}
	if !recipientsOf(events, "socket.close")[2] {
		t.Fatalf("grace closures: %+v", events)
	}
	written := len(events)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireGrace, roomID, 2, now.Add(32*time.Second))
	}); err != nil {
		t.Fatalf("already removed member must be a no-op: %v", err)
	}
	if events := eventsAfter(t, h, roomID, written); len(events) != 0 {
		t.Fatalf("already removed member must write nothing: %+v", events)
	}
}

func TestExpireStaffingDeadlineCancelsGameAndClosesRoom(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h, nil)
	startAt := now.Add(5 * time.Second)
	if _, err := h.svc.StartGame(h.store, roomID, startAt); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.Leave(tx, roomID, 2, room.DepartureExited, startAt)
	}); err != nil {
		t.Fatalf("leave in game: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "game.staffing.required") != 3 {
		t.Fatalf("leaving operative must pause the game for the other three: %+v", events)
	}
	written := len(events)

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpireStaffing, roomID, 0, startAt.Add(121*time.Second))
	}); err != nil {
		t.Fatalf("expire staffing: %v", err)
	}
	events = eventsAfter(t, h, roomID, written)
	if countEvents(events, "game.cancelled") != 3 || countEvents(events, "room.closed") != 3 {
		t.Fatalf("cancel events: %+v", events)
	}
	if countEvents(events, "room.state") != 0 {
		t.Fatalf("a closed room has no room.state: %+v", events)
	}
	if payloadOf(t, events, "game.cancelled")["deficient_team"] != string(game.TeamRed) {
		t.Fatalf("red lost its operative: %+v", payloadOf(t, events, "game.cancelled"))
	}
	if countEvents(events, "socket.close") != 3 || payloadOf(t, events, "socket.close")["code"] != float64(4404) {
		t.Fatalf("every remaining socket closes with 4404: %+v", events)
	}
}

func TestExpirePostGameDeadlineRemovesUndecidedMembers(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h, nil)
	startAt := now.Add(5 * time.Second)
	if _, err := h.svc.StartGame(h.store, roomID, startAt); err != nil {
		t.Fatalf("start: %v", err)
	}
	finishGame(t, h, roomID, startAt)
	if _, err := h.svc.ReturnToLobby(h.store, roomID, 1, startAt); err != nil {
		t.Fatalf("return to lobby: %v", err)
	}

	if _, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.RunDeadline(tx, roomaction.DeadlineExpirePostGame, roomID, 0, startAt.Add(61*time.Second))
	}); err != nil {
		t.Fatalf("expire post game: %v", err)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "room.player.left") != 3 || !recipientsOf(events, "room.player.left")[1] {
		t.Fatalf("host must hear three timeouts: %+v", events)
	}
	if payloadOf(t, events, "room.player.left")["reason"] != string(room.DeparturePostGameTimeout) {
		t.Fatalf("left reason: %+v", payloadOf(t, events, "room.player.left"))
	}
	completed := payloadOf(t, events, "room.post_game.completed")
	if completed == nil || len(completed["remaining_member_ids"].([]any)) != 1 {
		t.Fatalf("post_game.completed: %+v", completed)
	}
	if countEvents(events, "room.state") != 1 || countEvents(events, "socket.close") != 3 {
		t.Fatalf("one room.state for the host, three closures: %+v", events)
	}
}

// A clue sent after the timer ran out: the turn switches for everyone, the sender gets the rejection, nothing is lost.
func TestLateClueSwitchesTurnAndRejectsSender(t *testing.T) {
	h := newHarness(t)
	timer := room.OneMinute
	roomID := readyRoom(t, h, &timer)
	startAt := now.Add(5 * time.Second)
	started, err := h.svc.StartGame(h.store, roomID, startAt)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	startingTeam := started.ToRecord().CurrentTurn.Team
	spymaster := int64(1)
	if startingTeam == game.TeamBlue {
		spymaster = 3
	}

	outcome, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.SubmitClue(tx, roomID, spymaster, game.Clue{Word: "qwertyuiop", Number: 1}, startAt.Add(61*time.Second))
	})
	if err != nil {
		t.Fatalf("late clue must not be a hard error: %v", err)
	}
	if !errors.Is(outcome.ActorError, game.ErrTurnExpired) || outcome.ActorReply != nil {
		t.Fatalf("sender must be rejected without an ack: %+v", outcome)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "game.turn.changed") != 4 || payloadOf(t, events, "game.turn.changed")["previous_team"] != string(startingTeam) {
		t.Fatalf("everyone must hear the timer switch: %+v", events)
	}
}

// A return after the deadline: the undecided members are removed and announced, the late member is rejected.
func TestLateReturnCleansUpAndRejectsSender(t *testing.T) {
	h := newHarness(t)
	roomID := readyRoom(t, h, nil)
	startAt := now.Add(5 * time.Second)
	if _, err := h.svc.StartGame(h.store, roomID, startAt); err != nil {
		t.Fatalf("start: %v", err)
	}
	finishGame(t, h, roomID, startAt)
	if _, err := h.svc.ReturnToLobby(h.store, roomID, 1, startAt); err != nil {
		t.Fatalf("return to lobby: %v", err)
	}

	outcome, err := h.run(func(tx service.RoomStore) (roomaction.Outcome, error) {
		return h.actions.ReturnToLobby(tx, roomID, 2, startAt.Add(61*time.Second))
	})
	if err != nil {
		t.Fatalf("late return must not be a hard error: %v", err)
	}
	if !errors.Is(outcome.ActorError, room.ErrPostGameDeadlineExpired) || outcome.ActorReply != nil {
		t.Fatalf("late member must be rejected without an ack: %+v", outcome)
	}
	events := eventsAfter(t, h, roomID, 0)
	if countEvents(events, "room.player.left") != 3 || countEvents(events, "room.post_game.completed") != 1 || countEvents(events, "socket.close") != 3 {
		t.Fatalf("host must hear the cleanup and three sockets close: %+v", events)
	}
}
