package websocketprotocol_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"game-session/game"
	"game-session/postgres"
	"game-session/room"
	"game-session/roomaction"
	"game-session/service"
	"game-session/websocketprotocol"
)

var now = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

type placeholderProfiles struct{}

func (placeholderProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{Username: fmt.Sprintf("user-%d", id), AvatarURL: fmt.Sprintf("/static/avatars/default/%d.svg", id)}
	}
	return profiles, nil
}

// readyRoom: 4 kişilik oda, herkes oturmuş ve hazır -> oda COUNTDOWN'da.
func readyRoom(t *testing.T) (*service.RoomService, *websocketprotocol.CommandHandler, *postgres.Store, int64) {
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
	capacity := 4
	snapshot, err := svc.Create(store, 1, &capacity, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := snapshot.RoomID
	for _, id := range []int64{2, 3, 4} {
		if _, err := svc.Join(store, roomID, id, now); err != nil {
			t.Fatalf("join %d: %v", id, err)
		}
	}
	seats := []struct {
		user int64
		team game.Team
		role game.Role
	}{{1, game.TeamRed, game.RoleSpymaster}, {2, game.TeamRed, game.RoleOperative}, {3, game.TeamBlue, game.RoleSpymaster}, {4, game.TeamBlue, game.RoleOperative}}
	for _, s := range seats {
		if _, err := svc.SelectRole(store, roomID, s.user, s.team, s.role, now); err != nil {
			t.Fatalf("seat %d: %v", s.user, err)
		}
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if _, err := svc.SetReady(store, roomID, id, true, now); err != nil {
			t.Fatalf("ready %d: %v", id, err)
		}
	}
	actions := roomaction.New(svc, placeholderProfiles{})
	return svc, websocketprotocol.NewCommandHandler(actions, store), store, roomID
}

// Testlerde oturum kimliği: command_ledger.session_id UUID tipinde.
const session1 = "11111111-1111-1111-1111-111111111111"
const session2 = "22222222-2222-2222-2222-222222222222"

func outboxEvents(t *testing.T, store *postgres.Store, roomID int64) []postgres.OutboxEvent {
	t.Helper()
	events, err := store.PendingRecipientEvents(roomID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return events
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

func TestLobbyCommandAckEventsAndReplay(t *testing.T) {
	_, handler, store, roomID := readyRoom(t)

	// 2 numaralı üye hazır değilim der: countdown iptal olur.
	message := []byte(`{"type":"room.ready.set","request_id":"r1","payload":{"ready":false}}`)
	outcome := handler.HandleCommand(roomID, 2, session2, message, now)
	if outcome.Error != nil {
		t.Fatalf("unready: unexpected error %+v", outcome.Error.Error)
	}
	ack := outcome.Ack.Payload.(map[string]any)
	if outcome.Ack.RequestID != "r1" || ack["ready"] != false || ack["room_status"] != room.StatusWaiting {
		t.Fatalf("unready ack: got %+v", outcome.Ack)
	}
	// Herkese: player.updated, countdown.cancelled, room.state -> 4 üye x 3 olay, outbox'ta.
	events := outboxEvents(t, store, roomID)
	if len(events) != 12 {
		t.Fatalf("events: want 12, got %d", len(events))
	}
	if countEvents(events, "room.player.updated") != 4 || countEvents(events, "room.countdown.cancelled") != 4 || countEvents(events, "room.state") != 4 {
		t.Fatalf("event mix wrong: %+v", events)
	}
	// sıra numarası oda içinde artar: updated=1, cancelled=2, state=3.
	if events[0].Sequence != 1 || events[11].Sequence != 3 {
		t.Fatalf("sequences: first %d last %d", events[0].Sequence, events[11].Sequence)
	}

	// Aynı komut tekrar gelirse: aynı ack, yeni olay yok.
	replay := handler.HandleCommand(roomID, 2, session2, message, now)
	if replay.Ack == nil || replay.Ack.RequestID != "r1" || len(outboxEvents(t, store, roomID)) != 12 {
		t.Fatalf("replay: want same ack without new events, got %+v", replay)
	}

	// Aynı request_id farklı komutla: INVALID_EVENT.
	changed := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"room.ready.set","request_id":"r1","payload":{"ready":true}}`), now)
	if changed.Error == nil || changed.Error.Error.Code != "INVALID_EVENT" {
		t.Fatalf("changed replay: want INVALID_EVENT, got %+v", changed)
	}

	// Bilinmeyen tip, bozuk alan, request_id yok.
	unknown := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"room.start","request_id":"r2","payload":{}}`), now)
	if unknown.Error == nil || unknown.Error.Error.Code != "INVALID_EVENT" || unknown.Error.Error.Details["type"] != "room.start" {
		t.Fatalf("unknown type: got %+v", unknown.Error)
	}
	badField := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"room.ready.set","request_id":"r3","payload":{"ready":"yes"}}`), now)
	if badField.Error == nil || badField.Error.Error.Code != "INVALID_PAYLOAD" || badField.Error.Error.Details["field"] != "payload.ready" {
		t.Fatalf("bad field: got %+v", badField.Error)
	}
	noID := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"room.ready.set","payload":{"ready":true}}`), now)
	if noID.Error == nil || noID.Error.Error.Code != "INVALID_EVENT" || noID.Error.RequestID != nil {
		t.Fatalf("no request_id: got %+v", noID.Error)
	}
	noPayload := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"game.turn.pass","request_id":"r4"}`), now)
	if noPayload.Error == nil || noPayload.Error.Error.Code != "INVALID_EVENT" || noPayload.Error.Error.Details["field"] != "payload" {
		t.Fatalf("no payload: got %+v", noPayload.Error)
	}
	extraField := handler.HandleCommand(roomID, 2, session2, []byte(`{"type":"game.turn.pass","request_id":"r5","payload":{"extra":true}}`), now)
	if extraField.Error == nil || extraField.Error.Error.Code != "INVALID_PAYLOAD" {
		t.Fatalf("extra field on empty-payload command: got %+v", extraField.Error)
	}
}

func TestGameCommandsHideColorsFromOperatives(t *testing.T) {
	svc, handler, store, roomID := readyRoom(t)
	startAt := now.Add(5 * time.Second)
	if _, err := svc.StartGame(store, roomID, startAt); err != nil {
		t.Fatalf("start game: %v", err)
	}
	snapshot, err := svc.Snapshot(store, roomID, 1, startAt)
	if err != nil || snapshot.Game == nil {
		t.Fatalf("snapshot after start: %v %+v", err, snapshot)
	}
	spymaster, operative := int64(1), int64(2)
	if snapshot.Game.StartingTeam == game.TeamBlue {
		spymaster, operative = 3, 4
	}

	// Operative ipucu veremez.
	forbidden := handler.HandleCommand(roomID, operative, session1, []byte(`{"type":"game.clue.submit","request_id":"c0","payload":{"word":"qwertyuiop","number":1}}`), startAt)
	if forbidden.Error == nil || forbidden.Error.Error.Code != "ROLE_FORBIDDEN" {
		t.Fatalf("operative clue: want ROLE_FORBIDDEN, got %+v", forbidden)
	}

	clue := handler.HandleCommand(roomID, spymaster, session1, []byte(`{"type":"game.clue.submit","request_id":"c1","payload":{"word":"qwertyuiop","number":1}}`), startAt)
	events := outboxEvents(t, store, roomID)
	if clue.Error != nil || countEvents(events, "game.clue.submitted") != 4 || countEvents(events, "game.history.appended") != 4 {
		t.Fatalf("clue: got %+v %+v", clue, events)
	}
	written := len(events)

	// Tahta rastgele: oyun bitmesin diye spymaster'ın gördüğü tahtadan kendi takımının bir kartını seçiyoruz.
	spymasterView, err := svc.Snapshot(store, roomID, spymaster, startAt)
	if err != nil {
		t.Fatalf("spymaster snapshot: %v", err)
	}
	ownCardID := 0
	for _, card := range spymasterView.Game.Board {
		if card.Color != nil && string(*card.Color) == string(spymasterView.Game.StartingTeam) {
			ownCardID = card.CardID
			break
		}
	}
	guess := handler.HandleCommand(roomID, operative, session1, []byte(fmt.Sprintf(`{"type":"game.card.guess","request_id":"g1","payload":{"card_id":%d}}`, ownCardID)), startAt)
	events = outboxEvents(t, store, roomID)[written:]
	if guess.Error != nil || countEvents(events, "game.card.revealed") != 4 || countEvents(events, "game.score.updated") != 4 {
		t.Fatalf("guess: got %+v %+v", guess, events)
	}

	// room.state: operative açılmamış kartların rengini görmez, spymaster hepsini görür.
	for _, e := range events {
		if e.EventType != "room.state" {
			continue
		}
		var payload struct {
			Room room.RoomSnapshot `json:"room"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("room.state payload: %v", err)
		}
		hidden := 0
		for _, card := range payload.Room.Game.Board {
			if card.Color == nil {
				hidden++
			}
		}
		switch e.RecipientUserID {
		case 1, 3: // spymaster'lar
			if hidden != 0 {
				t.Fatalf("spymaster %d sees %d hidden cards", e.RecipientUserID, hidden)
			}
		default: // operative'ler: 25 kart, 1 açıldı
			if hidden != 24 {
				t.Fatalf("operative %d sees %d hidden cards, want 24", e.RecipientUserID, hidden)
			}
		}
	}
}

// Program yeniden başlasa da defter ve sayaç PostgreSQL'de: yeni CommandHandler eski cevabı bilir, numara devam eder.
func TestLedgerAndSequenceSurviveRestart(t *testing.T) {
	svc, first, firstStore, roomID := readyRoom(t)
	message := []byte(`{"type":"room.ready.set","request_id":"r1","payload":{"ready":false}}`)
	original := first.HandleCommand(roomID, 2, session2, message, now)
	if original.Ack == nil {
		t.Fatalf("original: %+v", original.Error)
	}
	before := outboxEvents(t, firstStore, roomID)
	lastSequence := before[len(before)-1].Sequence

	store, err := postgres.Open(os.Getenv("GS_DB"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	second := websocketprotocol.NewCommandHandler(roomaction.New(svc, placeholderProfiles{}), store)
	replay := second.HandleCommand(roomID, 2, session2, message, now)
	if replay.Ack == nil || replay.Ack.RequestID != "r1" || len(outboxEvents(t, store, roomID)) != len(before) {
		t.Fatalf("replay after restart: %+v", replay)
	}
	next := second.HandleCommand(roomID, 2, session2, []byte(`{"type":"room.ready.set","request_id":"r2","payload":{"ready":true}}`), now)
	after := outboxEvents(t, store, roomID)
	if next.Ack == nil || after[len(before)].Sequence <= lastSequence {
		t.Fatalf("sequence after restart: last %d, next %d", lastSequence, after[len(before)].Sequence)
	}
}
