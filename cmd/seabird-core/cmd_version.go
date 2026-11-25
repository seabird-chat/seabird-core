package main

import (
	"context"
	"fmt"

	"github.com/belak/x/versionx"
	"github.com/peterbourgon/ff/v4"
)

var Version = ""

func newVersionCommand() *ff.Command {
	return &ff.Command{
		Name:      "version",
		Usage:     "seabird-core version",
		ShortHelp: "print version information",
		Exec: func(ctx context.Context, args []string) error {
			fmt.Printf("%s (%s)\n", versionx.Get(Version), versionx.GoVersion())
			return nil
		},
	}
}
