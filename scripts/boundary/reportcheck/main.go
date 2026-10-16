// Command reportcheck validates a file written by `ballast report --out`
// (docs/p2-design.md §4.4, §10 item 3): it must decode as report.Report and
// pass report.Validate (no ids, paths, secrets or free text). Exit 0 valid,
// 1 invalid (violations on stderr), 2 usage. It also refuses a file that
// contains a planted marker given with -forbid (repeatable), so accept-p2.sh
// can assert that seeded secrets, paths and session ids never reach it.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/libingtong/ballast/internal/report"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var forbid list
	flag.Var(&forbid, "forbid", "string that must not appear in the file (repeatable)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: reportcheck [-forbid S]... <report.json>")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "reportcheck:", err)
		os.Exit(2)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r report.Report
	if err := dec.Decode(&r); err != nil {
		fmt.Fprintln(os.Stderr, "reportcheck: not a report:", err)
		os.Exit(1)
	}
	bad := false
	for _, v := range report.Validate(&r) {
		fmt.Fprintln(os.Stderr, "reportcheck: violation:", v)
		bad = true
	}
	for _, f := range forbid {
		if f != "" && bytes.Contains(data, []byte(f)) {
			fmt.Fprintf(os.Stderr, "reportcheck: the report contains the planted marker %q\n", f)
			bad = true
		}
	}
	if bad {
		os.Exit(1)
	}
	fmt.Printf("reportcheck: ok (v%d, %d sessions, %d rules)\n", r.V, r.Sessions, len(r.Rules))
}
