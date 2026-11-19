package roomaction

import (
	"time"

	"game-session/room"
	"game-session/service"
)

func (handler *Handler) Join(tx service.RoomStore, roomID int64, userID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, 0, now)
	if err != nil {
		return Outcome{}, err
	}
	context.userID = userID
	snapshot, err := handler.rooms.Join(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	event := eventDraft{eventType: "room.player.joined", payload: map[string]any{
		"player":       findPlayer(afterView, userID),
		"host_user_id": afterView.HostUserID,
		"player_count": afterView.PlayerCount,
		"room_status":  afterView.Status,
	}}
	return handler.complete(tx, context, actionResult{actorReply: snapshot, events: []eventDraft{event}, after: after, afterView: afterView})
}

func playerLeftEvent(afterView room.RoomSnapshot, departedPlayer *room.RoomMemberView, reason room.DepartureReason, kickedByUserID int64) eventDraft {
	payload := map[string]any{
		"player":       departedPlayer,
		"reason":       reason,
		"host_user_id": afterView.HostUserID,
		"player_count": afterView.PlayerCount,
		"room_status":  afterView.Status,
	}
	if kickedByUserID != 0 {
		payload["kicked_by_user_id"] = kickedByUserID
	}
	return eventDraft{eventType: "room.player.left", payload: payload}
}

func staffingRequiredEvent(snapshot room.RoomSnapshot, departed *room.RoomMemberView, reason room.DepartureReason) eventDraft {
	departure := map[string]any{"reason": reason}
	if departed != nil {
		departure["user_id"] = departed.UserID
		departure["username"] = departed.Username
		departure["team"] = departed.Team
		departure["role"] = departed.Role
	}
	payload := map[string]any{
		"game_id":   snapshot.Game.GameID,
		"phase":     snapshot.Game.CurrentTurn.Phase,
		"staffing":  snapshot.Game.Staffing,
		"departure": departure,
	}
	return eventDraft{eventType: "game.staffing.required", payload: payload}
}

func memberLeftEvents(afterView room.RoomSnapshot, departedPlayer *room.RoomMemberView, leave room.LeaveResult, kickedByUserID int64) []eventDraft {
	var events []eventDraft
	if leave.CountdownCancelled {
		cancelReason := "PLAYER_LEFT"
		if kickedByUserID != 0 {
			cancelReason = "PLAYER_KICKED"
		}
		events = append(events, countdownCancelledEvent(cancelReason, kickedByUserID))
	}
	events = append(events, playerLeftEvent(afterView, departedPlayer, leave.Reason, kickedByUserID))
	if leave.PostGameCompleted {
		events = append(events, postGameCompletedEvent(afterView))
	}
	if leave.GamePaused {
		events = append(events, staffingRequiredEvent(afterView, departedPlayer, leave.Reason))
	}
	return events
}

func (handler *Handler) KickMember(tx service.RoomStore, roomID int64, hostUserID int64, targetUserID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, hostUserID, now)
	if err != nil {
		return Outcome{}, err
	}
	leave, err := handler.rooms.Kick(tx, roomID, hostUserID, targetUserID, now)
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	kickedPlayer := findPlayer(context.before, targetUserID)
	reply := map[string]any{"kicked_user_id": targetUserID, "room_status": afterView.Status}
	events := memberLeftEvents(afterView, kickedPlayer, leave, hostUserID)
	closures := []SocketClosure{{UserID: targetUserID, Code: 4403, Reason: "KICKED_FROM_ROOM"}}
	return handler.complete(tx, context, actionResult{actorReply: reply, events: events, closures: closures, after: after, afterView: afterView})
}

func (handler *Handler) Leave(tx service.RoomStore, roomID int64, userID int64, reason room.DepartureReason, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	leave, err := handler.rooms.Leave(tx, roomID, userID, reason, now)
	if err != nil {
		return Outcome{}, err
	}
	closures := []SocketClosure{{UserID: userID, Code: 4403, Reason: string(reason)}}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	if len(after.input.Members) == 0 {
		return handler.complete(tx, context, actionResult{closures: closures})
	}
	departedPlayer := findPlayer(context.before, userID)
	events := memberLeftEvents(afterView, departedPlayer, leave, 0)
	return handler.complete(tx, context, actionResult{events: events, closures: closures, after: after, afterView: afterView})
}
