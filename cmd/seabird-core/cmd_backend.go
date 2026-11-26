package main

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/seabird-chat/seabird-core/internal/core"
)

func newBackendCommand(rootFlags *ff.FlagSet, rootConfig *rootConfig) *ff.Command {
	backendFlags := ff.NewFlagSet("backend").SetParent(rootFlags)

	return &ff.Command{
		Name:      "backend",
		Usage:     "seabird-core backend [FLAGS] <SUBCOMMAND> ...",
		ShortHelp: "inspect the backends core knows about",
		Flags:     backendFlags,
		Subcommands: []*ff.Command{
			{
				Name:      "list",
				Usage:     "seabird-core backend list [FLAGS]",
				ShortHelp: "list every backend core has seen",
				Flags:     ff.NewFlagSet("list").SetParent(backendFlags),
				Exec:      withDB(rootConfig, listBackends),
			},
		},
	}
}

func listBackends(ctx context.Context, db *core.DB, args []string) error {
	backends, err := db.ListBackends(ctx, true)
	if err != nil {
		return err
	}

	// Display names can contain spaces, so they come last.
	w := newTable()
	fmt.Fprintln(w, "ID\tBACKEND\tCONNECTED\tLAST SEEN\tDISPLAY NAME")
	for _, backend := range backends {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n",
			backend.ID, backendName(backend), backend.Connected, formatLastSeen(backend.LastSeen), backend.DisplayName)
	}
	return w.Flush()
}

// backendName describes a backend by the type and name it connects with.
func backendName(backend core.Backend) string {
	return backend.Type + ":" + backend.Name
}

// backendNames returns every backend's name, by ID.
func backendNames(ctx context.Context, db *core.DB) (map[string]string, error) {
	backends, err := db.ListBackends(ctx, true)
	if err != nil {
		return nil, err
	}

	names := make(map[string]string, len(backends))
	for _, backend := range backends {
		names[backend.ID] = backendName(backend)
	}
	return names, nil
}
