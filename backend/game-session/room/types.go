package room

import (
	"game-session/game"
	"time"
)

type Room struct {
	RoomID           int64 `gorm:"primaryKey;autoIncrement"`
	RoomCode         string
	Status           Status `gorm:"default:WAITING"`
	MaxPlayers       int    `gorm:"default:20"`
	HostUserID       *int64 `gorm:"column:host_user_id"`
	TurnTimerSeconds *TurnTimer
	CountdownEndsAt  *time.Time
	PostGameEndsAt   *time.Time
	EmptySince       *time.Time
	CloseAt          *time.Time
	Language         string `gorm:"default:en"`
	CreatedAt        time.Time
	RoomVersion      int
	EventSequence    int
}

type RoomMember struct {
	RoomID      int64 `gorm:"autoIncrement:false"`
	UserID      int64 `gorm:"primaryKey;autoIncrement:false"`
	Username    string
	State       State `gorm:"default:IN_LOBBY"`
	Team        *game.Team
	Role        *game.Role
	Ready       bool
	JoinedAt    time.Time `gorm:"autoCreateTime"`
	GraceEndsAt *time.Time
}

type Status string

const (
	StatusWaiting   Status = "WAITING"
	StatusCountDown Status = "COUNTDOWN"
	StatusInGame    Status = "IN_GAME"
	StatusPostGame  Status = "POST_GAME"
	StatusClosed    Status = "CLOSED"
)

type State string

const (
	StateInLobby  State = "IN_LOBBY"
	StateInGame   State = "IN_GAME"
	StatePostGame State = "POST_GAME"
)

type TurnTimer int

const (
	OneMinute        TurnTimer = 60
	OneAndHalfMinute TurnTimer = 90
	TwoMinute        TurnTimer = 120
)

type RoomInput struct {
	Members     []RoomMember
	CurrentGame *game.Game
	Countdown   *CountdownView
	LastGame    *LastGameView
	Profiles    map[int64]Profile
}

func findMember(members []RoomMember, userID int64) *RoomMember {
	for i := range members {
		if members[i].UserID == userID {
			return &members[i]
		}
	}
	return nil
}

func replaceMember(members []RoomMember, updated RoomMember) []RoomMember {
	replaced := append([]RoomMember{}, members...)
	for i := range replaced {
		if replaced[i].UserID == updated.UserID {
			replaced[i] = updated
		}
	}
	return replaced
}

func membersInJoinOrder(members []RoomMember) []RoomMember {
	roomMembers := append([]RoomMember{}, members...)

	for i := 1; i < len(roomMembers); i++ {
		member := roomMembers[i]
		j := i
		for j > 0 && (member.JoinedAt.Before(roomMembers[j-1].JoinedAt) ||
			(member.JoinedAt.Equal(roomMembers[j-1].JoinedAt) && member.UserID < roomMembers[j-1].UserID)) {
			roomMembers[j] = roomMembers[j-1]
			j--
		}
		roomMembers[j] = member
	}
	return roomMembers
}

func membersWithState(members []RoomMember, state State, ready bool) []RoomMember {
	updated := make([]RoomMember, 0, len(members))
	for _, member := range members {
		member.State = state
		member.Ready = ready
		updated = append(updated, member)
	}
	return updated
}

func hasPostGamePending(members []RoomMember) bool {
	for _, member := range members {
		if member.State == StatePostGame {
			return true
		}
	}
	return false
}

func copyPtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}
