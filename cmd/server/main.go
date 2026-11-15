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

	"email-invoice-guard/internal/app"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local health endpoint and exit")
	backup := flag.String("backup", "", "write a consistent SQLite snapshot to a new file and exit")
	checkDB := flag.Bool("check-db", false, "check SQLite integrity, foreign keys and credential decryption and exit")
	showVersion := flag.Bool("version", false, "print release version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("Email-Invoice-Guard %s commit=%s built=%s\n", version, commit, buildTime)
		return
	}

	if *healthcheck {
		os.Exit(runHealthcheck(os.Getenv, nil, os.Stderr))
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := app.LoadConfig()
	if err != nil {
		// Configuration errors name the offending variable and never echo
		// secret values.
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *backup != "" || *checkDB {
		opCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := databaseOperation(opCtx, cfg, *backup, *checkDB, os.Stdout); err != nil {
			logger.Error("database operation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}
