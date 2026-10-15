// Command accutil is the portable helper behind the acceptance scripts in
// scripts/*.sh. Bash alone cannot do JSON, process timeouts, peak-RSS
// measurement or content hashing the same way on macOS, Linux and Windows
// Git Bash, so these few operations live here. It is built once per script
// run into a temporary directory and never touches anything outside the
// paths it is given.
//
// When the executable is invoked under the name of a build tool (go, npm,
// cargo, mvnw, ...) it behaves as a stub toolchain for scripts/matrix.sh:
// it appends its argv to $ACC_STUB_LOG and exits with $ACC_STUB_EXIT.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const usage = `usage: accutil <command> [args]

  run      [-timeout D] [-stdin F] [-stdout F] [-stderr F] [-stats F] -- cmd args...
  json     get|len|has|append|valid FILE [PATH [VALUE]]
  jsonl    count|valid PATH [key=value ...]
  tree     DIR [skip-name ...]
  sleep    SECONDS
  hold-lock LOCKFILE SECONDS READYFILE
  wait-file FILE [SECONDS]
  exit-file FILE
  ctl      DIR
  replay-check   REPORT.json...
  replay-label   REPORT.json LABELS_DIR N
  labels-check   LABELS_DIR MIN REPORT.json...
  gen-transcript OUT.jsonl CWD SESSION SIZE_MB
  m0-check ROOT
`

// stubNames are the tool names under which accutil acts as a stub toolchain.
var stubNames = map[string]bool{
	"go": true, "npm": true, "pnpm": true, "yarn": true, "python": true, "python3": true,
	"cargo": true, "mvn": true, "mvnw": true, "gradle": true, "gradlew": true,
	"dotnet": true, "swift": true, "bundle": true,
}

func main() {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if stubNames[base] {
		os.Exit(runStub(base, os.Args[1:]))
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(125)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var code int
	switch cmd {
	case "run":
		code = cmdRun(args)
	case "json":
		code = cmdJSON(args)
	case "jsonl":
		code = cmdJSONL(args)
	case "tree":
		code = cmdTree(args)
	case "sleep":
		code = cmdSleep(args)
	case "hold-lock":
		code = cmdHoldLock(args)
	case "wait-file":
		code = cmdWaitFile(args)
	case "exit-file":
		code = cmdExitFile(args)
	case "ctl":
		code = cmdCtl(args)
	case "replay-check":
		code = cmdReplayCheck(args)
	case "replay-label":
		code = cmdReplayLabel(args)
	case "labels-check":
		code = cmdLabelsCheck(args)
	case "gen-transcript":
		code = cmdGenTranscript(args)
	case "m0-check":
		code = cmdM0Check(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "accutil: unknown command %q\n%s", cmd, usage)
		code = 125
	}
	os.Exit(code)
}

// fail prints an accutil error and returns the internal-error exit code.
func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "accutil: "+format+"\n", args...)
	return 125
}
