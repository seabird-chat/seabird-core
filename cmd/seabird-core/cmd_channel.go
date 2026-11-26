package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newChannelCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	channelFlags := ff.NewFlagSet("channel").SetParent(rootFlags)

	return &ff.Command{
		Name:      "channel",
		Usage:     "seabird-core channel [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect and seed the channels core knows about",
		Flags:     channelFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core channel list [FLAGS]",
				ShortHelp: "list channels and their addresses",
				Flags:     ff.NewFlagSet("list").SetParent(channelFlags),
				Exec:      withDB(rootConfig, listChannels),
			},
			{
				Name:      "seed",
				Usage:     "seabird-core channel seed [FLAGS] <ADDRESS>",
				ShortHelp: "create a channel core hasn't seen yet",
				LongHelp: "The address is the channel ID plugins see today, like irc://libera/%23chan. " +
					"A channel that already exists is left alone. Prints the channel's ID.",
				Flags: ff.NewFlagSet("seed").SetParent(channelFlags),
				Exec:  withDB(rootConfig, seedChannel),
			},
		},
	}
}

func listChannels(ctx context.Context, db *core.DB, args []string) error {
	backends, err := backendAddresses(ctx, db)
	if err != nil {
		return err
	}
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return err
	}

	// Addresses are percent-encoded, so the first two columns never contain
	// spaces and are easy to pull out with a script.
	w := newTable()
	fmt.Fprintln(w, "ID\tADDRESS\tLAST SEEN\tNAME")
	for _, channel := range channels {
		address := backends[channel.BackendID].Relative(channel.NativeID)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			channel.ID, address, formatLastSeen(channel.LastSeen), channel.DisplayName)
	}
	return w.Flush()
}

func seedChannel(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("seed takes exactly one address")
	}
	address, err := core.ParseFullID(args[0])
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", args[0], err)
	}

	backend, err := db.SeedBackend(ctx, address.Backend.Type, address.Backend.ID)
	if err != nil {
		return err
	}
	channel, err := db.SeedChannel(ctx, backend.ID, address.Path)
	if err != nil {
		return err
	}
	fmt.Println(channel.ID)
	return nil
}
