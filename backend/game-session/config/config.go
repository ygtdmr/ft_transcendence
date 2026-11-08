package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL  string
	Port         int
	TickInterval time.Duration
}

// env isimleri Furkan'ın shared/config paketi merge olunca ona göre güncellenecek
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		Port:         8083,
		TickInterval: time.Second,
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
	if raw := os.Getenv("TICK_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return Config{}, errors.New("TICK_INTERVAL must be a positive duration like 1s")
		}
		cfg.TickInterval = interval
	}
	return cfg, nil
}
