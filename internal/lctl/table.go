package lctl

import (
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
)

// writeTable writes rows under header as a table (tablewriter's default
// box-drawing style), for list commands whose output is a terminal. The
// header is used as given (no auto-formatting, which would turn
// HOST:PORT into "HOST : PORT") and left-aligned like the cells.
func writeTable(w io.Writer, header []string, rows [][]string) error {
	t := tablewriter.NewTable(w,
		tablewriter.WithHeaderAutoFormat(tw.Off),
		tablewriter.WithHeaderAlignment(tw.AlignLeft),
	)
	t.Header(header)
	if err := t.Bulk(rows); err != nil {
		return err
	}
	return t.Render()
}
