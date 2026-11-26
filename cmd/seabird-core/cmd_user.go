package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newUserCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	userFlags := ff.NewFlagSet("user").SetParent(rootFlags)

	return &ff.Command{
		Name:      "user",
		Usage:     "seabird-core user [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect and seed the users core knows about",
		Flags:     userFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core user list [FLAGS]",
				ShortHelp: "list users and their addresses",
				Flags:     ff.NewFlagSet("list").SetParent(userFlags),
				Exec:      withDB(rootConfig, listUsers),
			},
			{
				Name:      "seed",
				Usage:     "seabird-core user seed [FLAGS] <ADDRESS>",
				ShortHelp: "create a user core hasn't seen yet",
				LongHelp: "The address is the user ID plugins see today, like irc://libera/alice. " +
					"A user who already exists is left alone. Prints the user's ID.",
				Flags: ff.NewFlagSet("seed").SetParent(userFlags),
				Exec:  withDB(rootConfig, seedUser),
			},
		},
	}
}

func listUsers(ctx context.Context, db *core.DB, args []string) error {
	backends, err := backendAddresses(ctx, db)
	if err != nil {
		return err
	}
	users, err := db.ListUsers(ctx)
	if err != nil {
		return err
	}

	// Addresses are percent-encoded, so the first two columns never contain
	// spaces and are easy to pull out with a script.
	w := newTable()
	fmt.Fprintln(w, "ID\tADDRESS\tLAST SEEN\tNAME")
	for _, user := range users {
		address := backends[user.BackendID].Relative(user.NativeID)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			user.ID, address, formatLastSeen(user.LastSeen), user.DisplayName)
	}
	return w.Flush()
}

func seedUser(ctx context.Context, db *core.DB, args []string) error {
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
	user, err := db.SeedUser(ctx, backend.ID, address.Path)
	if err != nil {
		return err
	}
	fmt.Println(user.ID)
	return nil
}
