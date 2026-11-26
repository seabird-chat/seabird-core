package main

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newUserCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	userFlags := ff.NewFlagSet("user").SetParent(rootFlags)

	return &ff.Command{
		Name:      "user",
		Usage:     "seabird-core user [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect the users core knows about",
		Flags:     userFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core user list [FLAGS]",
				ShortHelp: "list every user core has seen",
				Flags:     ff.NewFlagSet("list").SetParent(userFlags),
				Exec:      withDB(rootConfig, listUsers),
			},
		},
	}
}

func listUsers(ctx context.Context, db *core.DB, args []string) error {
	backends, err := backendNames(ctx, db)
	if err != nil {
		return err
	}
	users, err := db.ListUsers(ctx)
	if err != nil {
		return err
	}

	// Names and external IDs can contain spaces, so they come last.
	w := newTable()
	fmt.Fprintln(w, "ID\tBACKEND\tLAST SEEN\tNAME\tEXTERNAL ID")
	for _, user := range users {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			user.ID, backends[user.BackendID], formatLastSeen(user.LastSeen), user.DisplayName, user.ExternalID)
	}
	return w.Flush()
}
