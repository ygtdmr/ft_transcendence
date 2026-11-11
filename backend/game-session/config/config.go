package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"game-session/room"
)

type Config struct {
	DatabaseURL   string
	Port          int
	InternalPort  int
	TickInterval  time.Duration
	Penalty       *room.PenaltyConfig
	ServiceTokens map[string]string
}

func loadServiceTokens() (map[string]string, error) {
	tokens := map[string]string{}
	for serviceName, envName := range map[string]string{
		"auth-service": "AUTH_SERVICE_CREDENTIAL_FILE",
		"user-service": "USER_SERVICE_CREDENTIAL_FILE",
	} {
		path := os.Getenv(envName)
		if path == "" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New(envName + " could not be read: " + err.Error())
		}
		tokens[serviceName] = strings.TrimSpace(string(content))
	}
	return tokens, nil
}

// env isimleri Furkan'ın shared/config paketi merge olunca ona göre güncellenecek
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Port:          8083,
		InternalPort:  8093,
		TickInterval:  time.Second,
		ServiceTokens: map[string]string{},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if raw := os.Getenv("PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 || port > 65535 {
			return Config{}, errors.New("PORT must be a number between 1 and 65535")
		}
		cfg.Port = port
	}
	if raw := os.Getenv("INTERNAL_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 || port > 65535 {
			return Config{}, errors.New("INTERNAL_PORT must be a number between 1 and 65535")
		}
		cfg.InternalPort = port
	}
	if raw := os.Getenv("TICK_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return Config{}, errors.New("TICK_INTERVAL must be a positive duration like 1s")
		}
		cfg.TickInterval = interval
	}
	serviceTokens, err := loadServiceTokens()
	if err != nil {
		return Config{}, err
	}
	cfg.ServiceTokens = serviceTokens
	penalty, err := room.PenaltyConfigFromEnv(os.Getenv)
	if err == nil {
		cfg.Penalty = &penalty
	} else if !errors.Is(err, room.ErrPenaltyNotConfigured) {
		return Config{}, err
	}
	return cfg, nil
}
