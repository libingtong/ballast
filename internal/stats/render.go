package stats

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
)

// RenderTable renders rows as an aligned text table with the columns
// rule | action | target | evals | hits | would | unknown | sessions. For
// --by session|repo a leading key column is added.
func RenderTable(rows []Row, by GroupBy) []byte {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	withKey := by == BySession || by == ByRepo
	head := []string{"rule", "action", "target", "evals", "hits", "would", "unknown", "sessions"}
	if withKey {
		head = append([]string{string(by)}, head...)
	}
	fmt.Fprintln(w, strings.Join(head, "\t"))
	for _, r := range rows {
		cols := []string{r.Rule, dash(string(r.Action)), dash(string(r.Target)),
			fmt.Sprint(r.Evals), fmt.Sprint(r.Hits), fmt.Sprint(r.Would), fmt.Sprint(r.Unknown), fmt.Sprint(r.Sessions)}
		if withKey {
			cols = append([]string{r.Key}, cols...)
		}
		fmt.Fprintln(w, strings.Join(cols, "\t"))
	}
	_ = w.Flush()
	return buf.Bytes()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
