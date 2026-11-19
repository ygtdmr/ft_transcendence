package roomaction

import (
	"time"

	"game-session/room"
)

// 4401 invalid session, 4403 membership ended, 4404 room closed.
type SocketClosure struct {
	UserID int64
	Code   int
	Reason string
}

type Outcome struct {
	ActorReply any
	ActorError error
}

type roomState struct {
	room  *room.Room
	input room.RoomInput
}

type actionContext struct {
	roomID      int64
	userID      int64
	now         time.Time
	before      room.RoomSnapshot
	beforeState *roomState
}

type eventDraft struct {
	eventType            string
	payload              any
	roomViewForRecipient bool
}

type actionResult struct {
	actorReply any
	events     []eventDraft
	closures   []SocketClosure
	after      *roomState
	afterView  room.RoomSnapshot
}
