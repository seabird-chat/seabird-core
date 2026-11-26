package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newGroupCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	groupFlags := ff.NewFlagSet("group").SetParent(rootFlags)

	return &ff.Command{
		Name:      "group",
		Usage:     "seabird-core group [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "manage channel groups, which plugins see as one channel",
		Flags:     groupFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "create",
				Usage:     "seabird-core group create [FLAGS] <NAME>",
				ShortHelp: "create an empty group and print its ID",
				Flags:     ff.NewFlagSet("create").SetParent(groupFlags),
				Exec:      withDB(rootConfig, createGroup),
			},
			{
				Name:      "add",
				Usage:     "seabird-core group add [FLAGS] <NAME> <CHANNEL ID>...",
				ShortHelp: "add channels to a group",
				Flags:     ff.NewFlagSet("add").SetParent(groupFlags),
				Exec:      withDB(rootConfig, addToGroup),
			},
			{
				Name:      "remove",
				Usage:     "seabird-core group remove [FLAGS] <NAME> <CHANNEL ID>...",
				ShortHelp: "remove channels from a group",
				Flags:     ff.NewFlagSet("remove").SetParent(groupFlags),
				Exec:      withDB(rootConfig, removeFromGroup),
			},
			{
				Name:      "list",
				Usage:     "seabird-core group list [FLAGS]",
				ShortHelp: "list every group and its channels",
				Flags:     ff.NewFlagSet("list").SetParent(groupFlags),
				Exec:      withDB(rootConfig, listGroups),
			},
			{
				Name:      "delete",
				Usage:     "seabird-core group delete [FLAGS] <NAME>",
				ShortHelp: "delete a group, leaving its channels ungrouped",
				Flags:     ff.NewFlagSet("delete").SetParent(groupFlags),
				Exec:      withDB(rootConfig, deleteGroup),
			},
		},
	}
}

func createGroup(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("create takes exactly one name")
	}

	group, err := db.CreateChannelGroup(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Println(group.ID)
	return nil
}

func addToGroup(ctx context.Context, db *core.DB, args []string) error {
	if len(args) < 2 {
		return errors.New("add takes a group name and at least one channel ID")
	}

	group, err := groupNamed(ctx, db, args[0])
	if err != nil {
		return err
	}
	return db.AddToChannelGroup(ctx, group.ID, args[1:]...)
}

func removeFromGroup(ctx context.Context, db *core.DB, args []string) error {
	if len(args) < 2 {
		return errors.New("remove takes a group name and at least one channel ID")
	}

	group, err := groupNamed(ctx, db, args[0])
	if err != nil {
		return err
	}
	return db.RemoveFromChannelGroup(ctx, group.ID, args[1:]...)
}

func listGroups(ctx context.Context, db *core.DB, args []string) error {
	backends, err := backendNames(ctx, db)
	if err != nil {
		return err
	}
	groups, err := db.ListChannelGroups(ctx)
	if err != nil {
		return err
	}

	// One row per member, so a group with no members gets a row of its own.
	w := newTable()
	fmt.Fprintln(w, "ID\tGROUP\tCHANNEL ID\tBACKEND\tCHANNEL NAME")
	for _, group := range groups {
		members, err := db.ListGroupMembers(ctx, group.ID)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\n", group.ID, group.Name)
		}
		for _, member := range members {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				group.ID, group.Name, member.ID, backends[member.BackendID], member.DisplayName)
		}
	}
	return w.Flush()
}

func deleteGroup(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("delete takes exactly one group name")
	}

	group, err := groupNamed(ctx, db, args[0])
	if err != nil {
		return err
	}
	return db.DeleteChannelGroup(ctx, group.ID)
}

// groupNamed looks up a group by name, with an error that says which.
func groupNamed(ctx context.Context, db *core.DB, name string) (*core.ChannelGroup, error) {
	group, err := db.GetChannelGroupByName(ctx, name)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("no group named %q", name)
	}
	return group, err
}

// groupNames returns every group's name, by ID.
func groupNames(ctx context.Context, db *core.DB) (map[string]string, error) {
	groups, err := db.ListChannelGroups(ctx)
	if err != nil {
		return nil, err
	}

	names := make(map[string]string, len(groups))
	for _, group := range groups {
		names[group.ID] = group.Name
	}
	return names, nil
}
