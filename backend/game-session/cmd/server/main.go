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

func createServers(cfg config.Config, handler *rest.Handler, log *slog.Logger) (*http.Server, *http.Server) {
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serviceScopes := map[string][]string{
		"auth-service": {"membership.read", "session.departure"},
		"user-service": {"membership.read", "account.cleanup"},
	}
	if len(cfg.ServiceTokens) == 0 {
		log.Warn("no service credential files configured, internal API will reject every caller")
	}
	access := rest.NewServiceAccessCheck(cfg.ServiceTokens, serviceScopes)

	internal := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.InternalPort),
		Handler:           handler.InternalRoutes(access),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server, internal
}

func startServer(server *http.Server, name string, log *slog.Logger, stop context.CancelFunc) {
	log.Info("starting HTTP server", "server", name, "addr", server.Addr)

	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("HTTP server failed", "server", name, "err", err)
		stop()
	}
}

func shutdownServer(server *http.Server, name string, log *slog.Logger, shutdownCtx context.Context) {
	err := server.Shutdown(shutdownCtx)
	if err != nil {
		log.Error("HTTP server shutdown failed, closing", "server", name, "err", err)
		if err := server.Close(); err != nil {
			log.Error("HTTP server close failed", "server", name, "err", err)
		}
	}
}

// shutdown iki sunucuyu nazikçe kapatır ve worker'ın durmasını bekler; hepsi için toplam 10 saniye.
func shutdown(server, internalServer *http.Server, workerDone <-chan struct{}, log *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shutdownServer(server, "public", log, shutdownCtx)
	shutdownServer(internalServer, "internal", log, shutdownCtx)

	select {
	case <-workerDone:
		log.Info("worker stopped")
	case <-shutdownCtx.Done():
		log.Error("worker shutdown timed out")
	}
}

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
	if cfg.Penalty == nil {
		log.Warn("leave penalty is not configured, penalties will not be recorded")
	}
	svc := service.NewRoomService(store, profiles, cfg.Penalty)
	handler := rest.NewHandler(svc, store)
	deadlines := worker.New(store, svc, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		deadlines.Run(ctx, cfg.TickInterval)
	}()
	
	server, internalServer := createServers(cfg, handler, log)
	go startServer(server, "public", log, stop)
	go startServer(internalServer, "internal", log, stop)

	<-ctx.Done()
	log.Info("shutting down")
	shutdown(server, internalServer, workerDone, log)
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
