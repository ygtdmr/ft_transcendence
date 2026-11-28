package roomaction

import (
	"errors"
	"time"

	"game-session/game"
	"game-session/room"
	"game-session/service"
)

func (handler *Handler) turnExpiredDuringCommand(tx service.RoomStore, context actionContext, moveErr error) (Outcome, error) {
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	previousTeam := context.before.Game.CurrentTurn.Team
	event := turnChangedEvent(afterView.Game.GameID, previousTeam, game.ChangeReasonTurnTimerExpired, afterView.Game.CurrentTurn, afterView.Game.Score)
	outcome, err := handler.complete(tx, context, actionResult{events: []eventDraft{event}, after: after, afterView: afterView})
	if err != nil {
		return Outcome{}, err
	}
	outcome.ActorError = moveErr
	return outcome, nil
}

func buildClueReplyAndEvent(gameID int64, submittedByUserID int64, turn game.CurrentTurn) (map[string]any, eventDraft) {
	reply := map[string]any{
		"game_id":           gameID,
		"clue":              turn.Clue,
		"guesses_remaining": turn.GuessesRemaining,
		"current_turn":      turn,
	}
	event := eventDraft{eventType: "game.clue.submitted", payload: map[string]any{
		"game_id":              gameID,
		"submitted_by_user_id": submittedByUserID,
		"team":                 turn.Team,
		"clue":                 turn.Clue,
		"guesses_remaining":    turn.GuessesRemaining,
		"current_turn":         turn,
	}}
	return reply, event
}

func (handler *Handler) SubmitClue(tx service.RoomStore, roomID int64, userID int64, clue game.Clue, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	turn, err := handler.rooms.GiveClue(tx, roomID, userID, clue, now)
	if errors.Is(err, game.ErrTurnExpired) {
		return handler.turnExpiredDuringCommand(tx, context, err)
	}
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	reply, event := buildClueReplyAndEvent(afterView.Game.GameID, userID, turn)
	return handler.complete(tx, context, actionResult{actorReply: reply, events: []eventDraft{event}, after: after, afterView: afterView})
}

func gameEndedEvents(guess game.GuessResult, after room.RoomSnapshot) []eventDraft {
	loser := game.TeamRed
	if *guess.Winner == game.TeamRed {
		loser = game.TeamBlue
	}
	return []eventDraft{
		{eventType: "game.ended", payload: map[string]any{
			"game_id":               guess.GameID,
			"winner":                guess.Winner,
			"loser":                 loser,
			"end_reason":            guess.EndReason,
			"revealed_card":         guess.Card,
			"score":                 guess.Score,
			"room_status":           after.Status,
			"game_status":           game.GameStatusFinished,
			"phase":                 guess.CurrentTurn.Phase,
			"post_game_deadline_at": after.PostGame.DeadlineAt,
			"finished_at":           guess.FinishedAt,
		}},
		{eventType: "room.post_game.started", payload: map[string]any{
			"game_id": guess.GameID, "deadline_at": after.PostGame.DeadlineAt, "pending_user_ids": after.PostGame.PendingUserIDs,
		}},
	}
}

func guessEvents(guess game.GuessResult, guessedByUserID int64, after room.RoomSnapshot) []eventDraft {
	events := []eventDraft{{eventType: "game.card.revealed", payload: map[string]any{
		"game_id":            guess.GameID,
		"card":               guess.Card,
		"guessed_by_user_id": guessedByUserID,
		"guessing_team":      guess.GuessingTeam,
		"score":              guess.Score,
		"current_turn":       guess.CurrentTurn,
		"winner":             guess.Winner,
		"end_reason":         guess.EndReason,
	}}}
	if *guess.Card.Color == "RED" || *guess.Card.Color == "BLUE" {
		events = append(events, eventDraft{eventType: "game.score.updated", payload: map[string]any{
			"game_id": guess.GameID, "score": guess.Score, "changed_color": guess.Card.Color,
		}})
	}
	if guess.Winner != nil {
		return append(events, gameEndedEvents(guess, after)...)
	}
	if guess.ChangeReason != nil {
		events = append(events, turnChangedEvent(guess.GameID, guess.GuessingTeam, *guess.ChangeReason, guess.CurrentTurn, guess.Score))
	}
	return events
}

func buildGuessReply(guess game.GuessResult, after room.RoomSnapshot) map[string]any {
	reply := map[string]any{
		"game_id":       guess.GameID,
		"card":          guess.Card,
		"guessing_team": guess.GuessingTeam,
		"score":         guess.Score,
		"current_turn":  guess.CurrentTurn,
		"winner":        guess.Winner,
		"end_reason":    guess.EndReason,
	}
	if guess.Winner != nil {
		reply["game_status"] = game.GameStatusFinished
		reply["room_status"] = after.Status
		reply["post_game_deadline_at"] = after.PostGame.DeadlineAt
	}
	return reply
}

func (handler *Handler) GuessCard(tx service.RoomStore, roomID int64, userID int64, cardID int, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	guess, err := handler.rooms.GuessCard(tx, roomID, userID, cardID, now)
	if errors.Is(err, game.ErrTurnExpired) {
		return handler.turnExpiredDuringCommand(tx, context, err)
	}
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	reply := buildGuessReply(guess, afterView)
	events := guessEvents(guess, userID, afterView)
	return handler.complete(tx, context, actionResult{actorReply: reply, events: events, after: after, afterView: afterView})
}

func (handler *Handler) PassTurn(tx service.RoomStore, roomID int64, userID int64, now time.Time) (Outcome, error) {
	context, err := handler.beforeAction(tx, roomID, userID, now)
	if err != nil {
		return Outcome{}, err
	}
	pass, err := handler.rooms.PassTurn(tx, roomID, userID, now)
	if errors.Is(err, game.ErrTurnExpired) {
		return handler.turnExpiredDuringCommand(tx, context, err)
	}
	if err != nil {
		return Outcome{}, err
	}
	after, afterView, err := handler.afterAction(tx, context)
	if err != nil {
		return Outcome{}, err
	}
	previousTeam := context.before.Game.CurrentTurn.Team
	reply := map[string]any{
		"game_id":           pass.GameID,
		"passed_by_user_id": userID,
		"previous_team":     previousTeam,
		"current_turn":      pass.CurrentTurn,
		"score":             pass.Score,
	}
	event := turnChangedEvent(pass.GameID, previousTeam, pass.ChangeReason, pass.CurrentTurn, pass.Score)
	return handler.complete(tx, context, actionResult{actorReply: reply, events: []eventDraft{event}, after: after, afterView: afterView})
}

// helpers----------

func turnChangedEvent(gameID int64, previousTeam game.Team, reason game.ChangeReason, currentTurn game.CurrentTurn, score game.Score) eventDraft {
	payload := map[string]any{
		"game_id":       gameID,
		"previous_team": previousTeam,
		"reason":        reason,
		"current_turn":  currentTurn,
		"score":         score,
	}
	return eventDraft{eventType: "game.turn.changed", payload: payload}
}
