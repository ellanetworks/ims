package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/server"
)

const shutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "ims.yaml", "path to the configuration file")

	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*configPath, logger); err != nil {
		logger.Error("ims stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &server.Server{Config: cfg, Logger: logger}

	if err := srv.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	srv.Shutdown(shutdownCtx)

	return nil
}
