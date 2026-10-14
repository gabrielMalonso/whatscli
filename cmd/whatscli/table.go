package main

import (
	"io"
	"text/tabwriter"
)

func newTableWriter(dst io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(dst, 2, 4, 2, ' ', 0)
}

func tableCell(s string, max int, full bool) string {
	if full {
		return sanitize(s)
	}
	return truncate(s, max)
}
