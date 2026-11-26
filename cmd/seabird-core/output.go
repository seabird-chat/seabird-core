package main

import (
	"os"
	"text/tabwriter"
)

// newTable returns a writer that lines up tab-separated columns on stdout.
func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}
