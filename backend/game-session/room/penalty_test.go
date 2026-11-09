package room

import (
	"errors"
	"game-session/game"
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

func TestPenaltyConfigFromEnv(t *testing.T) {
	env := map[string]string{
		EnvPenaltyBase:                "5",
		EnvPenaltySpymasterMultiplier: "3",
		EnvPenaltyOperativeMultiplier: "2",
	}
	cfg, err := PenaltyConfigFromEnv(func(k string) string { return env[k] })
	if err != nil || cfg.Base != 5 || cfg.SpymasterMultiplier != 3 || cfg.OperativeMultiplier != 2 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}

	delete(env, EnvPenaltyBase)
	if _, err := PenaltyConfigFromEnv(func(k string) string { return env[k] }); !errors.Is(err, ErrPenaltyNotConfigured) {
		t.Fatalf("err = %v, want ErrPenaltyNotConfigured", err)
	}

	env[EnvPenaltyBase] = "abc"
	if _, err := PenaltyConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected parse error")
	}
}
