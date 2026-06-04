package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	app, err := NewApp(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.StartCleanup(ctx)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("quick-file-share listening on http://%s", cfg.Addr)
	log.Printf("data dir: %s, max upload: %s, default ttl: %s", cfg.DataDir, formatBytes(cfg.MaxUploadBytes), cfg.DefaultTTL)
	if cfg.AccessPassword != "" {
		log.Printf("access password: enabled")
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
