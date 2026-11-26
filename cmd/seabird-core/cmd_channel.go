package main

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newChannelCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	channelFlags := ff.NewFlagSet("channel").SetParent(rootFlags)

	return &ff.Command{
		Name:      "channel",
		Usage:     "seabird-core channel [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect the channels core knows about",
		Flags:     channelFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core channel list [FLAGS]",
				ShortHelp: "list every channel core has seen",
				Flags:     ff.NewFlagSet("list").SetParent(channelFlags),
				Exec:      withDB(rootConfig, listChannels),
			},
		},
	}
}

func listChannels(ctx context.Context, db *core.DB, args []string) error {
	backends, err := backendNames(ctx, db)
	if err != nil {
		return err
	}
	channels, err := db.ListChannels(ctx, "", true)
	if err != nil {
		return err
	}

	// Names and external IDs can contain spaces, so they come last.
	w := newTable()
	fmt.Fprintln(w, "ID\tBACKEND\tJOINED\tLAST SEEN\tNAME\tEXTERNAL ID")
	for _, channel := range channels {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
			channel.ID, backends[channel.BackendID], channel.Joined,
			formatLastSeen(channel.LastSeen), channel.DisplayName, channel.ExternalID)
	}
	return w.Flush()
}
