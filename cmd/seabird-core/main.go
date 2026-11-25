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
)

type rootConfig struct {
	LogFormat slogx.Format
	LogLevel  slogx.Level
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Flags can also be set from the environment, e.g. SEABIRD_BIND_HOST.
	rootCmd := newRootCommand()
	err := rootCmd.ParseAndRun(ctx, os.Args[1:], ff.WithEnvVarPrefix("SEABIRD"))
	if err == nil {
		return
	}

	target := rootCmd.GetSelected()
	if target == nil {
		target = rootCmd
	}

	switch {
	case errors.Is(err, ff.ErrHelp):
		fmt.Fprintf(os.Stdout, "%s\n", ffhelp.Command(target))
		return
	case errors.Is(err, ff.ErrNoExec):
		fmt.Fprintf(os.Stderr, "%s\n", ffhelp.Command(target))
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}

	os.Exit(1)
}

func newRootCommand() *ff.Command {
	rootConfig := &rootConfig{
		LogFormat: slogx.FormatJSON,
		LogLevel:  slogx.LevelInfo,
	}

	fs := ff.NewFlagSet("seabird-core")

	fs.ValueLong("log-format", &rootConfig.LogFormat, "log output format (json, pretty, or text)")
	fs.ValueLong("log-level", &rootConfig.LogLevel, "log level (debug, info, warn, or error)")

	return &ff.Command{
		Name:  "seabird-core",
		Usage: "seabird-core [FLAGS] <SUBCOMMAND> ...",
		Flags: fs,
		Subcommands: []*ff.Command{
			newServeCommand(fs, rootConfig),
			newVersionCommand(),
		},
	}
}
