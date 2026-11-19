package roomaction

import (
	"errors"
	"time"

	"game-session/game"
	"game-session/room"
	"game-session/service"
)

func (handler *Handler) seatChanged(tx service.RoomStore, context actionContext, seat room.SeatResult, changedFields []string, cancelReason string) (Outcome, error) {
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	if previous := findPlayer(context.before, context.userID); previous != nil && previous.Ready {
		changedFields = append(changedFields, "ready")
	}
	reply := map[string]any{
		"user_id":     context.userID,
		"team":        seat.Member.Team,
		"role":        seat.Member.Role,
		"ready":       seat.Member.Ready,
		"room_status": afterView.Status,
	}
	events := []eventDraft{playerUpdatedEvent(afterView, context.userID, changedFields)}
	if seat.CountdownCancelled {
		events = append(events, countdownCancelledEvent(cancelReason, context.userID))
	}
	if seat.GameResumed {
		events = append(events, eventDraft{eventType: "game.staffing.restored", payload: map[string]any{
			"game_id":        afterView.Game.GameID,
			"restored_teams": []game.Team{*seat.Member.Team},
			"current_turn":   afterView.Game.CurrentTurn,
		}})
	}
	return handler.complete(tx, context, actionResult{actorReply: reply, events: events, after: after, afterView: afterView})
}

func (handler *Handler) SelectTeam(tx service.RoomStore, roomID int64, userID int64, team game.Team, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	seat, err := handler.rooms.SelectTeam(tx, roomID, userID, team, now)
	if err != nil {
		return Outcome{}, err
	}
	return handler.seatChanged(tx, context, seat, []string{"team"}, "TEAM_CHANGED")
}

func (handler *Handler) SelectRole(tx service.RoomStore, roomID int64, userID int64, team game.Team, role game.Role, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	seat, err := handler.rooms.SelectRole(tx, roomID, userID, team, role, now)
	if err != nil {
		return Outcome{}, err
	}
	return handler.seatChanged(tx, context, seat, []string{"team", "role"}, "ROLE_CHANGED")
}

func (handler *Handler) SetReady(tx service.RoomStore, roomID int64, userID int64, isReady bool, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	ready, err := handler.rooms.SetReady(tx, roomID, userID, isReady, now)
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	reply := map[string]any{
		"user_id":     userID,
		"ready":       ready.Member.Ready,
		"room_status": afterView.Status,
		"startable":   afterView.Startable,
	}
	events := []eventDraft{playerUpdatedEvent(afterView, userID, []string{"ready"})}
	if ready.CountdownStarted {
		events = append(events, eventDraft{eventType: "room.countdown.started", payload: map[string]any{"seconds_remaining": 3}})
	}
	if ready.CountdownCancelled {
		events = append(events, countdownCancelledEvent("PLAYER_UNREADY", userID))
	}
	return handler.complete(tx, context, actionResult{actorReply: reply, events: events, after: after, afterView: afterView})
}

func (handler *Handler) UpdateSettings(tx service.RoomStore, roomID int64, userID int64, update room.SettingsUpdate, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	settings, err := handler.rooms.UpdateSettings(tx, roomID, userID, update)
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	reply := map[string]any{
		"max_players":        settings.MaxPlayers,
		"turn_timer_seconds": settings.TurnTimerSeconds,
		"language":           settings.Language,
		"room_status":        afterView.Status,
	}
	event := eventDraft{eventType: "room.settings.updated", payload: map[string]any{
		"max_players":        settings.MaxPlayers,
		"turn_timer_seconds": settings.TurnTimerSeconds,
		"language":           settings.Language,
		"player_count":       afterView.PlayerCount,
		"changed_by_user_id": userID,
	}}
	return handler.complete(tx, context, actionResult{actorReply: reply, events: []eventDraft{event}, after: after, afterView: afterView})
}

func buildReturnReplyAndEvents(afterView room.RoomSnapshot, userID int64, returned room.ReturnResult) (map[string]any, []eventDraft) {
	var previousGameID *int64
	if afterView.LastGame != nil {
		previousGameID = &afterView.LastGame.GameID
	}
	reply := map[string]any{
		"room_id":          afterView.RoomID,
		"previous_game_id": previousGameID,
		"room_status":      afterView.Status,
		"member_state":     returned.Member.State,
	}
	events := []eventDraft{{eventType: "room.player.returned_to_lobby", payload: map[string]any{
		"user_id":          userID,
		"previous_game_id": previousGameID,
		"room_status":      afterView.Status,
		"member_state":     returned.Member.State,
	}}}
	if returned.PostGameCompleted {
		events = append(events, postGameCompletedEvent(afterView))
	}
	return reply, events
}

func (handler *Handler) ReturnToLobby(tx service.RoomStore, roomID int64, userID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	returned, err := handler.rooms.ReturnToLobby(tx, roomID, userID, now)
	if errors.Is(err, room.ErrPostGameDeadlineExpired) {
		outcome, expireErr := handler.expirePostGame(tx, roomID, now)
		if expireErr != nil {
			return Outcome{}, expireErr
		}
		outcome.ActorError = err
		return outcome, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	reply, events := buildReturnReplyAndEvents(afterView, userID, returned)
	return handler.complete(tx, context, actionResult{actorReply: reply, events: events, after: after, afterView: afterView})
}

// helpers----------

func playerUpdatedEvent(snapshot room.RoomSnapshot, userID int64, changedFields []string) eventDraft {
	payload := map[string]any{
		"player":         findPlayer(snapshot, userID),
		"changed_fields": changedFields,
		"room_status":    snapshot.Status,
		"startable":      snapshot.Startable,
	}
	return eventDraft{eventType: "room.player.updated", payload: payload}
}

func countdownCancelledEvent(reason string, changedByUserID int64) eventDraft {
	payload := map[string]any{
		"reason":             reason,
		"changed_by_user_id": changedByUserID,
	}
	return eventDraft{eventType: "room.countdown.cancelled", payload: payload}
}

func postGameCompletedEvent(snapshot room.RoomSnapshot) eventDraft {
	payload := map[string]any{
		"room_status":          snapshot.Status,
		"remaining_member_ids": playerUserIDs(snapshot),
	}
	return eventDraft{eventType: "room.post_game.completed", payload: payload}
}
