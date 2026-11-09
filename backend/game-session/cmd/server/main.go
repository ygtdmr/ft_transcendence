package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"game-session/config"
	"game-session/postgres"
	"game-session/rest"
	"game-session/room"
	"game-session/service"
	"game-session/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	store, err := postgres.Open(cfg.DatabaseURL)
	if err != nil {
		log.Error("database open", "err", err)
		os.Exit(1)
	}

	profiles := placeholderProfiles{} // istemci hazır olana kadar
	svc := service.NewRoomService(store, profiles)
	handler := rest.NewHandler(svc, store)
	deadlines := worker.New(store, svc, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		deadlines.Run(ctx, cfg.TickInterval)
	}()

	addr := fmt.Sprintf(":%d", cfg.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("starting HTTP server", "addr", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
		if err := server.Close(); err != nil {
			log.Error("http close", "err", err)
		}
	}

	select {
	case <-workerDone:
		log.Info("worker stopped")
	case <-shutdownCtx.Done():
		log.Error("worker shutdown timed out")
	}
}

// User Service bağlantısı hazır olana kadar yalnızca deneme/kontrol için örnek profiller üretir.
type placeholderProfiles struct{}

func (placeholderProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{
			Username:  fmt.Sprintf("user-%d", id),
			AvatarURL: fmt.Sprintf("/static/avatars/default/%d.svg", id),
		}
	}
	return profiles, nil
}
