package room

import (
	"game-session/game"
	"errors"
	"fmt"
	"strconv"
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

const (
	EnvPenaltyBase                = "LEAVE_PENALTY_BASE"
	EnvPenaltySpymasterMultiplier = "LEAVE_PENALTY_SPYMASTER_MULTIPLIER"
	EnvPenaltyOperativeMultiplier = "LEAVE_PENALTY_OPERATIVE_MULTIPLIER"
)

// PenaltyConfigFromEnv reads the three values from the environment and
// validates them. Unset values yield ErrPenaltyNotConfigured.
func PenaltyConfigFromEnv(getenv func(string) string) (PenaltyConfig, error) {
	var cfg PenaltyConfig
	for _, field := range []struct {
		name string
		dst  *int64
	}{
		{EnvPenaltyBase, &cfg.Base},
		{EnvPenaltySpymasterMultiplier, &cfg.SpymasterMultiplier},
		{EnvPenaltyOperativeMultiplier, &cfg.OperativeMultiplier},
	} {
		raw := getenv(field.name)
		if raw == "" {
			return PenaltyConfig{}, ErrPenaltyNotConfigured
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return PenaltyConfig{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.dst = value
	}
	if err := cfg.Validate(); err != nil {
		return PenaltyConfig{}, err
	}
	return cfg, nil
}
