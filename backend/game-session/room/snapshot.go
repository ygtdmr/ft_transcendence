package room

import (
	"game-session/game"
	"time"
)

type RoomSnapshot struct {
	RoomID           int64              `json:"room_id"`
	RoomCode         string             `json:"room_code"`
	Status           Status             `json:"status"`
	HostUserID       *int64             `json:"host_user_id"`
	PlayerCount      int                `json:"player_count"`
	MaxPlayers       int                `json:"max_players"`
	TurnTimerSeconds *TurnTimer         `json:"turn_timer_seconds"`
	Language         string             `json:"language"`
	Startable        bool               `json:"startable"`
	Countdown        *CountdownView     `json:"countdown,omitempty"`
	Players          []RoomMemberView   `json:"players"`
	Game             *game.GameSnapshot `json:"game"`
	CreatedAt        time.Time          `json:"created_at"`
	PostGame         *PostGameView      `json:"post_game,omitempty"`
	LastGame         *LastGameView      `json:"last_game,omitempty"`
	EmptySince       *time.Time         `json:"empty_since"`
	CloseAt          *time.Time         `json:"close_at"`
}

type RoomMemberView struct {
	UserID    int64      `json:"user_id"`
	Username  string     `json:"username"`
	AvatarURL string     `json:"avatar_url"`
	Team      *game.Team `json:"team"`
	Role      *game.Role `json:"role"`
	Ready     bool       `json:"ready"`
	State     State      `json:"state"`
	IsHost    bool       `json:"is_host"`
	JoinedAt  time.Time  `json:"joined_at"`
}

type Profile struct {
	Username  string
	AvatarURL string
}

func newRoomMemberView(member RoomMember, username, avatarURL string, hostUserID *int64) RoomMemberView {
	return RoomMemberView{
		UserID:    member.UserID,
		Username:  username,
		AvatarURL: avatarURL,
		Team:      copyPtr(member.Team),
		Role:      copyPtr(member.Role),
		Ready:     member.Ready,
		State:     member.State,
		IsHost:    hostUserID != nil && member.UserID == *hostUserID,
		JoinedAt:  member.JoinedAt,
	}
}

func gameDataFor(player *RoomMember, currentGame *game.Game, lastGame *LastGameView) (*game.GameSnapshot, *LastGameView, error) {
	switch player.State {
	case StateInGame, StatePostGame:
		if currentGame == nil {
			return nil, nil, ErrGameNotFound
		}
		if err := currentGame.ValidateGameState(); err != nil {
			return nil, nil, err
		}
		snapshot, err := currentGame.SnapshotForRole(player.Role)
		if err != nil {
			return nil, nil, err
		}
		return &snapshot, nil, nil
	case StateInLobby:
		return nil, copyPtr(lastGame), nil
	default:
		return nil, nil, ErrUnknownMemberState
	}
}

func (r *Room) membersWithProfiles(members []RoomMember, profiles map[int64]Profile) ([]RoomMemberView, error) {
	players := make([]RoomMemberView, 0, len(members))
	for _, member := range members {
		profile, ok := profiles[member.UserID]
		if !ok {
			return nil, ErrMemberProfileNotFound
		}
		players = append(players, newRoomMemberView(member, profile.Username, profile.AvatarURL, r.HostUserID))
	}
	return players, nil
}

func (r *Room) SnapshotForRoom(input RoomInput, userID int64) (RoomSnapshot, error) {
	if r.Status == StatusClosed {
		return RoomSnapshot{}, ErrRoomNotFound
	}
	player := findMember(input.Members, userID)
	if player == nil {
		return RoomSnapshot{}, ErrNotRoomMember
	}
	roomMembers := membersInJoinOrder(input.Members)
	players, err := r.membersWithProfiles(roomMembers, input.Profiles)
	if err != nil {
		return RoomSnapshot{}, err
	}
	gameView, lastGameView, err := gameDataFor(player, input.CurrentGame, input.LastGame)
	if err != nil {
		return RoomSnapshot{}, err
	}
	var countdownView *CountdownView
	if r.Status == StatusCountDown {
		countdownView = copyPtr(input.Countdown)
	}

	return RoomSnapshot{
		RoomID:           r.RoomID,
		RoomCode:         r.RoomCode,
		Status:           r.Status,
		HostUserID:       copyPtr(r.HostUserID),
		PlayerCount:      len(roomMembers),
		MaxPlayers:       r.MaxPlayers,
		TurnTimerSeconds: copyPtr(r.TurnTimerSeconds),
		Language:         r.Language,
		Startable:        r.isStartableRoom(roomMembers),
		Countdown:        countdownView,
		Players:          players,
		Game:             gameView,
		CreatedAt:        r.CreatedAt,
		PostGame:         r.postGame(roomMembers),
		LastGame:         lastGameView,
		EmptySince:       copyPtr(r.EmptySince),
		CloseAt:          copyPtr(r.CloseAt),
	}, nil
}
