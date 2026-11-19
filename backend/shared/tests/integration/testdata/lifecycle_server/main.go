// Command lifecycle_server is a tiny service used by the lifecycle integration
// test. It wires lifecycle.Serve exactly like a real service's main: a signal
// context, health routes, ordered components. It prints markers to stdout that
// the test inspects, and exits 0 only after a clean shutdown.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	health := lifecycle.NewHealth()
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Liveness())
	mux.Handle("/readyz", health.Readiness())
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("slow-started")
		time.Sleep(1200 * time.Millisecond)
		fmt.Println("slow-finished")
		_, _ = w.Write([]byte("done"))
	})

	err := lifecycle.Serve(ctx, &http.Server{Addr: os.Getenv("ADDR"), Handler: mux}, lifecycle.Options{
		Health:          health,
		ShutdownTimeout: 5 * time.Second,
		Components: []lifecycle.Component{
			lifecycle.Func("database", func() { fmt.Println("closed:database") }),
			lifecycle.Func("worker", func() { fmt.Println("closed:worker") }),
		},
	})
	if err != nil {
		fmt.Println("serve-error")
		os.Exit(1)
	}
	fmt.Println("exit-clean")
}
