package room

import (
	"game-session/game"
	"errors"
	"testing"
)

func TestPenaltyPointsByRole(t *testing.T) {
	cfg := PenaltyConfig{Base: 10, SpymasterMultiplier: 3, OperativeMultiplier: 2}

	if got, err := cfg.Points(game.RoleSpymaster); err != nil || got != 30 {
		t.Fatalf("spymaster = %d, %v; want 30", got, err)
	}
	if got, err := cfg.Points(game.RoleOperative); err != nil || got != 20 {
		t.Fatalf("operative = %d, %v; want 20", got, err)
	}
}

func TestPenaltyRejectsUnsetValues(t *testing.T) {
	if _, err := (PenaltyConfig{}).Points(game.RoleOperative); !errors.Is(err, ErrPenaltyNotConfigured) {
		t.Fatalf("err = %v, want ErrPenaltyNotConfigured", err)
	}
}

func TestPenaltySpymasterMustExceedOperative(t *testing.T) {
	cfg := PenaltyConfig{Base: 10, SpymasterMultiplier: 2, OperativeMultiplier: 2}
	if _, err := cfg.Points(game.RoleSpymaster); !errors.Is(err, ErrInvalidPenaltyConfig) {
		t.Fatalf("err = %v, want ErrInvalidPenaltyConfig", err)
	}
}

func TestPenaltyUnknownRole(t *testing.T) {
	cfg := PenaltyConfig{Base: 10, SpymasterMultiplier: 3, OperativeMultiplier: 2}
	if _, err := cfg.Points(game.Role("OBSERVER")); !errors.Is(err, game.ErrInvalidRole) {
		t.Fatalf("err = %v, want ErrInvalidRole", err)
	}
}
