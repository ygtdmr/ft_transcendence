package room

import (
	"game-session/game"
	"errors"
)

var (
	ErrPenaltyNotConfigured = errors.New("leave penalty values are not configured")
	ErrInvalidPenaltyConfig = errors.New("spymaster multiplier must be greater than operative multiplier")
)

// PenaltyConfig holds the server-configured leave penalty values.
// The contract leaves all three unset until the team agrees on them.
type PenaltyConfig struct {
	Base                int64
	SpymasterMultiplier int64
	OperativeMultiplier int64
}

func (c PenaltyConfig) Validate() error {
	if c.Base <= 0 || c.SpymasterMultiplier <= 0 || c.OperativeMultiplier <= 0 {
		return ErrPenaltyNotConfigured
	}
	if c.SpymasterMultiplier <= c.OperativeMultiplier {
		return ErrInvalidPenaltyConfig
	}
	return nil
}

// Points returns leave_penalty_base × role_multiplier for the departed role.
func (c PenaltyConfig) Points(role game.Role) (int64, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	switch role {
	case game.RoleSpymaster:
		return c.Base * c.SpymasterMultiplier, nil
	case game.RoleOperative:
		return c.Base * c.OperativeMultiplier, nil
	}
	return 0, game.ErrInvalidRole
}
