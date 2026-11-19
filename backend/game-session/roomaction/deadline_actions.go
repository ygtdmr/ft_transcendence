package roomaction

import (
	"errors"
	"fmt"
	"time"

	"game-session/game"
	"game-session/room"
	"game-session/service"
)

type DeadlineKind string

const (
	DeadlineStartGame      DeadlineKind = "start_game"
	DeadlineExpireTurn     DeadlineKind = "expire_turn"
	DeadlineExpireStaffing DeadlineKind = "expire_staffing"
	DeadlineExpireGrace    DeadlineKind = "expire_grace"
	DeadlineExpirePostGame DeadlineKind = "expire_post_game"
)

func (handler *Handler) startGame(tx service.RoomStore, roomID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, 0, now)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := handler.rooms.StartGame(tx, roomID, now); err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	event := eventDraft{eventType: "game.started", roomViewForRecipient: true}
	return handler.complete(tx, context, actionResult{events: []eventDraft{event}, after: after, afterView: afterView})
}

func (handler *Handler) expireTurn(tx service.RoomStore, roomID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, 0, now)
	if err != nil {
		return Outcome{}, err
	}
	pass, err := handler.rooms.PassTurnIfExpired(tx, roomID, now)
	if err != nil {
		return Outcome{}, err
	}
	if pass == nil {
		return Outcome{}, nil
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	previousTeam := context.before.Game.CurrentTurn.Team
	event := turnChangedEvent(pass.GameID, previousTeam, pass.ChangeReason, pass.CurrentTurn, pass.Score)
	return handler.complete(tx, context, actionResult{events: []eventDraft{event}, after: after, afterView: afterView})
}

func gameCancelledEvents(cancelledGame *game.GameSnapshot, deficient game.Team, now time.Time) []eventDraft {
	cancelled := map[string]any{
		"game_id":        cancelledGame.GameID,
		"winner":         nil,
		"loser":          nil,
		"end_reason":     game.EndReasonInsufficientPlayers,
		"deficient_team": deficient,
		"revealed_card":  nil,
		"score":          cancelledGame.Score,
		"room_status":    room.StatusClosed,
		"game_status":    game.GameStatusCancelled,
		"phase":          game.PhaseGameOver,
		"finished_at":    now,
	}
	closed := map[string]any{
		"reason":         game.EndReasonInsufficientPlayers,
		"game_id":        cancelledGame.GameID,
		"deficient_team": deficient,
		"closed_at":      now,
	}
	return []eventDraft{
		{eventType: "game.cancelled", payload: cancelled},
		{eventType: "room.closed", payload: closed},
	}
}

func (handler *Handler) expireStaffing(tx service.RoomStore, roomID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, 0, now)
	if err != nil {
		return Outcome{}, err
	}
	cancelled, err := handler.rooms.CancelIfStaffingExpired(tx, roomID, now)
	if err != nil {
		return Outcome{}, err
	}
	if !cancelled {
		return Outcome{}, nil
	}
	cancelledGame := context.before.Game
	red := cancelledGame.Staffing.Red.DeadlineAt
	blue := cancelledGame.Staffing.Blue.DeadlineAt
	deficient := game.TeamRed
	if red == nil || (blue != nil && blue.Before(*red)) {
		deficient = game.TeamBlue
	}
	events := gameCancelledEvents(cancelledGame, deficient, now)
	var closures []SocketClosure
	for _, player := range context.before.Players {
		closures = append(closures, SocketClosure{UserID: player.UserID, Code: 4404, Reason: "ROOM_CLOSED"})
	}
	return handler.complete(tx, context, actionResult{events: events, closures: closures})
}

func (handler *Handler) expireGrace(tx service.RoomStore, roomID int64, userID int64, now time.Time) (Outcome, error) {
	outcome, err := handler.Leave(tx, roomID, userID, room.DepartureDisconnected, now)
	if errors.Is(err, room.ErrGraceNotExpired) || errors.Is(err, room.ErrNotRoomMember) {
		return Outcome{}, nil
	}
	return outcome, err
}

func (handler *Handler) expirePostGame(tx service.RoomStore, roomID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, 0, now)
	if err != nil {
		return Outcome{}, err
	}
	expired, err := handler.rooms.ExpirePostGame(tx, roomID, now)
	if err != nil {
		return Outcome{}, err
	}
	if len(expired.RemovedUserIDs) == 0 {
		return Outcome{}, nil
	}
	var closures []SocketClosure
	for _, removedUserID := range expired.RemovedUserIDs {
		closures = append(closures, SocketClosure{UserID: removedUserID, Code: 4403, Reason: string(room.DeparturePostGameTimeout)})
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	if len(after.input.Members) == 0 {
		return handler.complete(tx, context, actionResult{closures: closures})
	}
	var events []eventDraft
	for _, removedUserID := range expired.RemovedUserIDs {
		events = append(events, playerLeftEvent(afterView, findPlayer(context.before, removedUserID), room.DeparturePostGameTimeout, 0))
	}
	events = append(events, postGameCompletedEvent(afterView))
	return handler.complete(tx, context, actionResult{events: events, closures: closures, after: after, afterView: afterView})
}

func (handler *Handler) RunDeadline(tx service.RoomStore, kind DeadlineKind, roomID int64, userID int64, now time.Time) (Outcome, error) {
	switch kind {
	case DeadlineStartGame:
		return handler.startGame(tx, roomID, now)
	case DeadlineExpireTurn:
		return handler.expireTurn(tx, roomID, now)
	case DeadlineExpireStaffing:
		return handler.expireStaffing(tx, roomID, now)
	case DeadlineExpireGrace:
		return handler.expireGrace(tx, roomID, userID, now)
	case DeadlineExpirePostGame:
		return handler.expirePostGame(tx, roomID, now)
	default:
		return Outcome{}, fmt.Errorf("unknown deadline kind %q", kind)
	}
}
