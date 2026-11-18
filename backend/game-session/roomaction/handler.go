package roomaction

import (
	"time"

	"game-session/room"
	"game-session/service"
)

type Handler struct {
	rooms    *service.RoomService
	profiles service.ProfileSource
}

func New(rooms *service.RoomService, profiles service.ProfileSource) *Handler {
	return &Handler{rooms: rooms, profiles: profiles}
}

func (handler *Handler) loadRoomState(tx service.RoomStore, roomID int64, now time.Time) (*roomState, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return nil, err
	}
	currentGame, err := tx.LoadCurrentGame(roomID)
	if err != nil {
		return nil, err
	}
	lastGame, err := tx.LoadLastGameResult(roomID)
	if err != nil {
		return nil, err
	}
	state := &roomState{room: r, input: room.RoomInput{Members: members, CurrentGame: currentGame, LastGame: lastGame, Countdown: r.Countdown(now)}}
	profiles, err := handler.profiles.ProfilesByUserIDs(memberUserIDs(state))
	if err != nil {
		return nil, err
	}
	state.input.Profiles = profiles
	return state, nil
}

func memberUserIDs(state *roomState) []int64 {
	userIDs := make([]int64, 0, len(state.input.Members))
	for _, member := range state.input.Members {
		userIDs = append(userIDs, member.UserID)
	}
	return userIDs
}

func playerUserIDs(snapshot room.RoomSnapshot) []int64 {
	userIDs := make([]int64, 0, len(snapshot.Players))
	for _, player := range snapshot.Players {
		userIDs = append(userIDs, player.UserID)
	}
	return userIDs
}

func findPlayer(snapshot room.RoomSnapshot, userID int64) *room.RoomMemberView {
	for i := range snapshot.Players {
		if snapshot.Players[i].UserID == userID {
			return &snapshot.Players[i]
		}
	}
	return nil
}

func (handler *Handler) beforeAction(tx service.RoomStore, roomID int64, viewerUserID int64, now time.Time) (actionContext, error) {
	state, err := handler.loadRoomState(tx, roomID, now)
	if err != nil {
		return actionContext{}, err
	}
	if viewerUserID == 0 && state.room.HostUserID != nil {
		viewerUserID = *state.room.HostUserID
	}
	var before room.RoomSnapshot
	if viewerUserID != 0 {
		before, err = state.room.SnapshotForRoom(state.input, viewerUserID)
		if err != nil {
			return actionContext{}, err
		}
	}
	return actionContext{roomID: roomID, userID: viewerUserID, now: now, before: before, beforeState: state}, nil
}

func (handler *Handler) afterAction(tx service.RoomStore, context actionContext) (*roomState, room.RoomSnapshot, error) {
	after, err := handler.loadRoomState(tx, context.roomID, context.now)
	if err != nil {
		return nil, room.RoomSnapshot{}, err
	}
	if len(after.input.Members) == 0 {
		return after, room.RoomSnapshot{}, nil
	}
	viewerUserID := context.userID
	stillInRoom := false
	for _, member := range after.input.Members {
		if member.UserID == viewerUserID {
			stillInRoom = true
			break
		}
	}
	if !stillInRoom && after.room.HostUserID != nil {
		viewerUserID = *after.room.HostUserID
	}
	view, err := after.room.SnapshotForRoom(after.input, viewerUserID)
	if err != nil {
		return nil, room.RoomSnapshot{}, err
	}
	return after, view, nil
}

func saveEventsForRecipients(tx service.RoomStore, roomRow *room.Room, drafts []eventDraft, state *roomState, recipientUserIDs []int64) error {
	for _, draft := range drafts {
		sequence, err := tx.NextEventSequence(roomRow.RoomID)
		if err != nil {
			return err
		}
		for _, recipientUserID := range recipientUserIDs {
			payload := draft.payload
			if draft.roomViewForRecipient {
				view, err := state.room.SnapshotForRoom(state.input, recipientUserID)
				if err != nil {
					return err
				}
				payload = map[string]any{"room": view}
			}
			if err := tx.SaveRecipientEvent(roomRow, recipientUserID, sequence, draft.eventType, payload); err != nil {
				return err
			}
		}
	}
	return nil
}

func saveSocketClosures(tx service.RoomStore, roomRow *room.Room, closures []SocketClosure) error {
	for _, closure := range closures {
		sequence, err := tx.NextEventSequence(roomRow.RoomID)
		if err != nil {
			return err
		}
		payload := map[string]any{"code": closure.Code, "reason": closure.Reason}
		if err := tx.SaveRecipientEvent(roomRow, closure.UserID, sequence, "socket.close", payload); err != nil {
			return err
		}
	}
	return nil
}

func historyAppendedEvents(before room.RoomSnapshot, after room.RoomSnapshot) []eventDraft {
	if after.Game == nil {
		return nil
	}
	lastSeqBefore := 0
	if before.Game != nil && len(before.Game.History) > 0 {
		lastSeqBefore = before.Game.History[len(before.Game.History)-1].Seq
	}
	var events []eventDraft
	for _, entry := range after.Game.History {
		if entry.Seq > lastSeqBefore {
			events = append(events, eventDraft{eventType: "game.history.appended", payload: map[string]any{
				"game_id": after.Game.GameID, "entry": entry,
			}})
		}
	}
	return events
}

func (handler *Handler) complete(tx service.RoomStore, context actionContext, result actionResult) (Outcome, error) {
	drafts := result.events
	recipientUserIDs := playerUserIDs(context.before)
	roomRow := context.beforeState.room
	if result.after != nil {
		drafts = append(drafts, historyAppendedEvents(context.before, result.afterView)...)
		drafts = append(drafts, eventDraft{eventType: "room.state", roomViewForRecipient: true})
		recipientUserIDs = memberUserIDs(result.after)
		roomRow = result.after.room
	}
	if err := saveEventsForRecipients(tx, roomRow, drafts, result.after, recipientUserIDs); err != nil {
		return Outcome{}, err
	}
	if err := saveSocketClosures(tx, roomRow, result.closures); err != nil {
		return Outcome{}, err
	}
	return Outcome{ActorReply: result.actorReply}, nil
}
