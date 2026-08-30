package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	app "homectl/internal/server"
)

//go:embed web/*
var embedded embed.FS

var version = "dev"

func main() {
	configPath := flag.String("config", defaultConfigPath(), "path to config.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		log.Printf("homectl-server %s", version)
		return
	}

	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0700); err != nil {
		log.Fatalf("create database directory: %v", err)
	}
	store, err := app.OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureAdmin(cfg.AdminUsername, cfg.AdminPassword); err != nil {
		log.Fatal(err)
	}
	if cfg.LegacyDeviceStore != "" {
		n, err := store.ImportLegacyDeviceJSON(cfg.LegacyDeviceStore)
		if err != nil {
			log.Fatal(err)
		}
		if n > 0 {
			log.Printf("migrated %d legacy device(s) from %s", n, cfg.LegacyDeviceStore)
		}
	}

	webRoot, err := fs.Sub(embedded, "web")
	if err != nil {
		log.Fatal(err)
	}

	s := app.New(cfg, store)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(http.FS(webRoot)),
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("homectl-server %s listening on %s (config=%s)", version, cfg.Addr, *configPath)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}
