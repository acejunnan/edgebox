package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"edgebox/internal/agent"
	"edgebox/internal/health"
	"edgebox/internal/logging"
	"edgebox/internal/management"
	"edgebox/internal/state"
	"edgebox/internal/store"
	"edgebox/internal/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	stateDirectory := flag.String("state-dir", "/var/lib/edgebox", "filesystem state directory")
	once := flag.Bool("once", false, "initialize, observe health once, and exit")
	refreshInterval := flag.Duration("health-refresh", 30*time.Second, "health observation interval")
	flag.Parse()
	if *refreshInterval <= 0 {
		return fmt.Errorf("health-refresh must be greater than zero")
	}

	logger := logging.With(logging.NewJSON(os.Stdout, slog.LevelInfo), logging.Fields{
		Component: "agent",
		Event:     "lifecycle",
	})
	stateStore, err := store.Open(*stateDirectory)
	if err != nil {
		return err
	}
	now := time.Now
	runtime, err := agent.New(
		stateStore,
		state.NewMachine(now),
		health.NewReport(now()),
		transport.NewUnconfigured(now),
		management.NewUnknownObserver(now),
		logger,
		now,
	)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtime.Start(ctx); err != nil {
		return err
	}
	if *once {
		return nil
	}

	ticker := time.NewTicker(*refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("agent stopped", "reason", "signal")
			return nil
		case <-ticker.C:
			if err := runtime.RefreshHealth(ctx); err != nil {
				logger.Error("health refresh failed", "error", "health_refresh_failed")
			}
		}
	}
}
