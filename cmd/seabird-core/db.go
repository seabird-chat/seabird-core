package main

import (
	"context"
	"errors"

	"github.com/seabird-chat/seabird-core/internal/core"
)

// dbFunc is a subcommand that works on the database.
type dbFunc func(ctx context.Context, db *core.DB, args []string) error

// withDB turns run into an Exec function that opens the database named by
// --database-url for it.
func withDB(rootConfig *rootConfig, run dbFunc) func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error {
		if rootConfig.DatabaseURL == "" {
			return errors.New("--database-url is required")
		}

		db, err := core.OpenDB(ctx, rootConfig.DatabaseURL, rootConfig.logger())
		if err != nil {
			return err
		}
		defer db.Close()

		return run(ctx, db, args)
	}
}
