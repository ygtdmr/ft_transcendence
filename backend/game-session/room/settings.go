package room

const (
	minPlayers = 4
	maxPlayers = 20
)

var supportedLanguages = []string{"en", "tr", "fr"}

type SettingsUpdate struct {
	MaxPlayers       *int
	UpdateTurnTimer  bool
	TurnTimerSeconds *TurnTimer
	Language         *string
}

type RoomSettings struct {
	MaxPlayers       int        `json:"max_players"`
	TurnTimerSeconds *TurnTimer `json:"turn_timer_seconds"`
	Language         string     `json:"language"`
}

func validTurnTimer(timer *TurnTimer) bool {
	if timer == nil {
		return true
	}
	return *timer == OneMinute || *timer == OneAndHalfMinute || *timer == TwoMinute
}

func validLanguage(language string) bool {
	for _, supported := range supportedLanguages {
		if language == supported {
			return true
		}
	}
	return false
}

func (r *Room) canUpdateSettings(members []RoomMember, userID int64) error {
	if r.Status == StatusClosed {
		return ErrRoomNotFound
	}
	member := findMember(members, userID)
	if member == nil {
		return ErrNotRoomMember
	}
	if r.HostUserID == nil || *r.HostUserID != userID {
		return ErrNotHost
	}
	if r.Status != StatusWaiting || member.State != StateInLobby {
		return ErrInvalidRoomState
	}
	return nil
}

func validateSettingsUpdate(update SettingsUpdate, memberCount int) error {
	if update.MaxPlayers == nil && !update.UpdateTurnTimer && update.Language == nil {
		return ErrInvalidPayload
	}
	if update.MaxPlayers != nil {
		capacity := *update.MaxPlayers
		if capacity < minPlayers || capacity > maxPlayers || capacity < memberCount {
			return ErrInvalidPayload
		}
	}
	if update.UpdateTurnTimer && !validTurnTimer(update.TurnTimerSeconds) {
		return ErrInvalidPayload
	}
	if update.Language != nil && !validLanguage(*update.Language) {
		return ErrInvalidPayload
	}
	return nil
}

func (r *Room) UpdateSettings(input RoomInput, userID int64, update SettingsUpdate) (RoomSettings, error) {
	if err := r.canUpdateSettings(input.Members, userID); err != nil {
		return RoomSettings{}, err
	}
	if err := validateSettingsUpdate(update, len(input.Members)); err != nil {
		return RoomSettings{}, err
	}

	if update.MaxPlayers != nil {
		r.MaxPlayers = *update.MaxPlayers
	}
	if update.UpdateTurnTimer {
		r.TurnTimerSeconds = copyPtr(update.TurnTimerSeconds)
	}
	if update.Language != nil {
		r.Language = *update.Language
	}

	return RoomSettings{
		MaxPlayers:       r.MaxPlayers,
		TurnTimerSeconds: copyPtr(r.TurnTimerSeconds),
		Language:         r.Language,
	}, nil
}
