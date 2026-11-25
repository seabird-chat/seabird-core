package main

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/belak/x/slogx"
	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

type serveConfig struct {
	BindHost    string
	DatabaseURL string
}

func newServeCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	var config serveConfig

	serveFlags := ff.NewFlagSet("serve").SetParent(rootFlags)
	serveFlags.StringVar(&config.BindHost, 0, "bind-host", ":11235", "address to listen on")
	serveFlags.StringVar(&config.DatabaseURL, 0, "database-url", "", "SQLite filename or file: URI (required)")

	return &ff.Command{
		Name:      "serve",
		Usage:     "seabird-core serve [FLAGS]",
		ShortHelp: "run the seabird-core server",
		Flags:     serveFlags,
		Exec: func(ctx context.Context, args []string) error {
			logger := slogx.New(rootConfig.LogFormat, rootConfig.LogLevel)

			logger.Debug("server configuration",
				slogx.String("bind_host", config.BindHost),
				slogx.String("database_url", config.DatabaseURL),
			)

			if config.DatabaseURL == "" {
				return errors.New("--database-url is required")
			}

			db, err := core.OpenDB(ctx, config.DatabaseURL, logger)
			if err != nil {
				return err
			}
			defer db.Close()

			listener, err := net.Listen("tcp", config.BindHost)
			if err != nil {
				return fmt.Errorf("listen: %w", err)
			}
			defer listener.Close()

			logger.Info("starting gRPC server", slogx.String("address", listener.Addr().String()))
			server := &core.Server{
				DB:     db,
				Broker: &core.MemoryBroker{},
				Logger: logger,
			}
			return server.Serve(ctx, listener)
		},
	}
}
