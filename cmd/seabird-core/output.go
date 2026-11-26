package main

import (
	"os"
	"text/tabwriter"
	"time"
)

// newTable returns a writer that lines up tab-separated columns on stdout.
func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// formatLastSeen describes when something was last seen.
func formatLastSeen(lastSeen *time.Time) string {
	if lastSeen == nil {
		return "never"
	}
	return lastSeen.Format(time.DateTime)
}
