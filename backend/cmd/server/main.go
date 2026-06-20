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

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/entrypoints"
	"sub2api-origin-lg/backend/internal/probe"
	"sub2api-origin-lg/backend/internal/server"
	"sub2api-origin-lg/backend/internal/store"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	db, err := store.Open(cfg.Storage.DSN)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	admin := adminclient.New(cfg)
	cache := entrypoints.NewCache(cfg, admin)
	serverProbe := probe.NewServerProbe(cfg)

	app := server.New(cfg, db, cache, serverProbe)
	srv := &http.Server{
		Addr:              cfg.App.Listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("sub2api-origin-lg listening on %s", cfg.App.Listen)
		errc <- srv.ListenAndServe()
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigc:
		log.Printf("received %s, shutting down", sig)
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
