package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/server"
)

const shutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "ims.yaml", "path to the configuration file")

	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		newLogger(slog.LevelInfo).Error("ims stopped", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Logging.Level)

	if err := run(cfg, logger); err != nil {
		logger.Error("ims stopped", "error", err)
		os.Exit(1)
	}
}

// JSON with Ella Core's ts and lowercase level.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				a.Key = "ts"
			case slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			}

			return a
		},
	}))
}

func run(cfg config.Config, logger *slog.Logger) error {
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
