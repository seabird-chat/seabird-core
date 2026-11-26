package main

import (
	"context"
	"fmt"
	"net"

	"github.com/belak/x/slogx"
	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

type serveConfig struct {
	BindHost string
}

func newServeCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	var config serveConfig

	serveFlags := ff.NewFlagSet("serve").SetParent(rootFlags)
	serveFlags.StringVar(&config.BindHost, 0, "bind-host", ":11235", "address to listen on")

	return &ff.Command{
		Name:      "serve",
		Usage:     "seabird-core serve [FLAGS]",
		ShortHelp: "run the seabird-core server",
		Flags:     serveFlags,
		Exec: withDB(rootConfig, func(ctx context.Context, db *core.DB, args []string) error {
			logger := rootConfig.logger()

			logger.Debug("server configuration",
				slogx.String("bind_host", config.BindHost),
				slogx.String("database_url", rootConfig.DatabaseURL),
			)

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
		}),
	}
}
