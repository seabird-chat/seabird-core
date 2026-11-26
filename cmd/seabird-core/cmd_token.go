package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newTokenCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	tokenFlags := ff.NewFlagSet("token").SetParent(rootFlags)

	return &ff.Command{
		Name:      "token",
		Usage:     "seabird-core token [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "manage auth tokens",
		Flags:     tokenFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "create",
				Usage:     "seabird-core token create [FLAGS] <NAME>",
				ShortHelp: "create a token for NAME and print it",
				Flags:     ff.NewFlagSet("create").SetParent(tokenFlags),
				Exec:      withDB(rootConfig, createToken),
			},
			{
				Name:      "list",
				Usage:     "seabird-core token list [FLAGS]",
				ShortHelp: "list token IDs and names",
				Flags:     ff.NewFlagSet("list").SetParent(tokenFlags),
				Exec:      withDB(rootConfig, listTokens),
			},
			{
				Name:      "delete",
				Usage:     "seabird-core token delete [FLAGS] <ID>",
				ShortHelp: "delete the token with the given ID",
				Flags:     ff.NewFlagSet("delete").SetParent(tokenFlags),
				Exec:      withDB(rootConfig, deleteToken),
			},
		},
	}
}

func createToken(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("create takes exactly one name")
	}

	key, err := db.CreateAuthToken(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

func listTokens(ctx context.Context, db *core.DB, args []string) error {
	tokens, err := db.ListAuthTokens(ctx)
	if err != nil {
		return err
	}

	w := newTable()
	fmt.Fprintln(w, "ID\tNAME")
	for _, token := range tokens {
		fmt.Fprintf(w, "%d\t%s\n", token.ID, token.Name)
	}
	return w.Flush()
}

func deleteToken(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("delete takes exactly one token ID")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid token ID %q", args[0])
	}

	err = db.DeleteAuthToken(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("no token with ID %d", id)
	}
	return err
}
