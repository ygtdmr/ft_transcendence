package room

import (
	"game-session/game"
	"time"
)

const staffingTimeout = 120 * time.Second

func missingRoles(members []RoomMember, team game.Team) []game.Role {
	hasSpymaster := false
	hasOperative := false
	for _, member := range members {
		if member.Team == nil || member.Role == nil {
			continue
		}
		if *member.Team != team {
			continue
		}
		switch *member.Role {
		case game.RoleSpymaster:
			hasSpymaster = true
		case game.RoleOperative:
			hasOperative = true
		}
	}
	missing := []game.Role{}
	if !hasSpymaster {
		missing = append(missing, game.RoleSpymaster)
	}
	if !hasOperative {
		missing = append(missing, game.RoleOperative)
	}
	return missing
}

func sendTeamStatusToGame(currentGame *game.Game, members []RoomMember, departed *game.Player, now time.Time) error {
	redMissing := missingRoles(members, game.TeamRed)
	blueMissing := missingRoles(members, game.TeamBlue)
	return currentGame.UpdateStaffing(redMissing, blueMissing, departed, now)
}
