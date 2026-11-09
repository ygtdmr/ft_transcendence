package room

import "errors"

var (
	ErrRoomNotFound            = errors.New("ROOM_NOT_FOUND")
	ErrNotRoomMember           = errors.New("NOT_ROOM_MEMBER")
	ErrTargetNotRoomMember     = errors.New("TARGET_NOT_ROOM_MEMBER")
	ErrAlreadyInRoom           = errors.New("ALREADY_IN_ROOM")
	ErrRoomFull                = errors.New("ROOM_FULL")
	ErrRoomNotJoinable         = errors.New("ROOM_NOT_JOINABLE")
	ErrInvalidRoomState        = errors.New("INVALID_ROOM_STATE")
	ErrNotHost                 = errors.New("NOT_HOST")
	ErrCannotKickSelf          = errors.New("CANNOT_KICK_SELF")
	ErrTeamRequired            = errors.New("TEAM_REQUIRED")
	ErrRoleRequired            = errors.New("ROLE_REQUIRED")
	ErrRoleConflict            = errors.New("ROLE_CONFLICT")
	ErrPostGamePending         = errors.New("POST_GAME_PENDING")
	ErrPostGameDeadlineExpired = errors.New("POST_GAME_DEADLINE_EXPIRED")
	ErrUserNotFound            = errors.New("USER_NOT_FOUND")
	ErrNotFriends              = errors.New("NOT_FRIENDS")
	ErrUserOffline             = errors.New("USER_OFFLINE")
	ErrUserInRoom              = errors.New("USER_IN_ROOM")
	ErrValidationError         = errors.New("VALIDATION_ERROR")
	ErrInvalidPayload          = errors.New("INVALID_PAYLOAD")
)

// Room-specific errors
var (
	ErrRoomNotStartable      = errors.New("room is not startable")
	ErrMemberProfileNotFound = errors.New("room member profile not found")
	ErrGameNotFound          = errors.New("room's game not found")
	ErrUnknownMemberState    = errors.New("unknown room member state")
	ErrGraceNotExpired       = errors.New("room member grace not expired")
)
