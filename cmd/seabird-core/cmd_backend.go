package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newBackendCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	backendFlags := ff.NewFlagSet("backend").SetParent(rootFlags)

	return &ff.Command{
		Name:      "backend",
		Usage:     "seabird-core backend [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect and seed the backends core knows about",
		Flags:     backendFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core backend list [FLAGS]",
				ShortHelp: "list backends and their addresses",
				Flags:     ff.NewFlagSet("list").SetParent(backendFlags),
				Exec:      withDB(rootConfig, listBackends),
			},
			{
				Name:      "seed",
				Usage:     "seabird-core backend seed [FLAGS] <ADDRESS>",
				ShortHelp: "create a backend core hasn't seen yet",
				LongHelp: "The address is the backend ID plugins see today, like irc://libera. " +
					"A backend that already exists is left alone. Prints the backend's ID.",
				Flags: ff.NewFlagSet("seed").SetParent(backendFlags),
				Exec:  withDB(rootConfig, seedBackend),
			},
		},
	}
}

func listBackends(ctx context.Context, db *core.DB, args []string) error {
	backends, err := db.ListBackends(ctx)
	if err != nil {
		return err
	}

	w := newTable()
	fmt.Fprintln(w, "ID\tADDRESS\tLAST SEEN")
	for _, backend := range backends {
		fmt.Fprintf(w, "%s\t%s\t%s\n", backend.ID, backendAddress(backend), formatLastSeen(backend.LastSeen))
	}
	return w.Flush()
}

func seedBackend(ctx context.Context, db *core.DB, args []string) error {
	if len(args) != 1 {
		return errors.New("seed takes exactly one address")
	}
	// A backend address has no path, so anything that parses with one is a
	// channel or user.
	if _, err := core.ParseFullID(args[0]); err == nil {
		return fmt.Errorf("%q is a channel or user address, not a backend", args[0])
	}
	address, err := core.ParseBackendID(args[0])
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", args[0], err)
	}

	backend, err := db.SeedBackend(ctx, address.Type, address.ID)
	if err != nil {
		return err
	}
	fmt.Println(backend.ID)
	return nil
}

// backendAddress returns the address plugins use today for a backend.
func backendAddress(backend core.Backend) core.BackendID {
	return core.BackendID{Type: backend.Type, ID: backend.NativeID}
}

// backendAddresses returns the address of every backend, by ID.
func backendAddresses(ctx context.Context, db *core.DB) (map[string]core.BackendID, error) {
	backends, err := db.ListBackends(ctx)
	if err != nil {
		return nil, err
	}

	addresses := make(map[string]core.BackendID, len(backends))
	for _, backend := range backends {
		addresses[backend.ID] = backendAddress(backend)
	}
	return addresses, nil
}
