package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/belak/x/slogx"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func main() {
	fs := ff.NewFlagSet("seabird-core")

	var (
		bindHost    = fs.StringLong("bind-host", "0.0.0.0:11235", "host/port to bind the gRPC service to")
		databaseURL = fs.StringLong("database-url", "", "url of the sqlite database to use")

		// Human-readable output on a terminal and JSON everywhere else, so
		// production logs stay machine readable.
		logFormat = defaultLogFormat()
		logLevel  = slogx.LevelInfo
	)

	fs.ValueLong("log-format", &logFormat, "log output format (json, pretty, or text)")
	fs.ValueLong("log-level", &logLevel, "log level (debug, info, warn, or error)")

	// Every flag is also readable from the environment, so --bind-host is
	// BIND_HOST and so on.
	if err := ff.Parse(fs, os.Args[1:], ff.WithEnvVars()); err != nil {
		if errors.Is(err, ff.ErrHelp) {
			fmt.Print(ffhelp.Flags(fs))
			return
		}

		fmt.Fprint(os.Stderr, ffhelp.Flags(fs))
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	logger := slogx.New(logFormat, logLevel)

	if *databaseURL == "" {
		logger.Error("--database-url is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server, err := core.New(ctx, core.Config{
		BindHost:    *bindHost,
		DatabaseURL: *databaseURL,
		Logger:      logger,
	})
	if err != nil {
		logger.Error("failed to start server", slogx.Err(err))
		os.Exit(1)
	}
	defer server.Close()

	if err := server.Run(ctx); err != nil {
		logger.Error("server exited with an error", slogx.Err(err))
		os.Exit(1)
	}
}

// defaultLogFormat picks pretty output when stdout is a terminal and JSON
// otherwise.
func defaultLogFormat() slogx.Format {
	if stat, err := os.Stdout.Stat(); err == nil && stat.Mode()&os.ModeCharDevice != 0 {
		return slogx.FormatPretty
	}

	return slogx.FormatJSON
}
