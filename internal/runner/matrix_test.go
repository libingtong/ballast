package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/sys"
)

// stubNames are the toolchain entry points the suggested commands start with.
var stubNames = []string{"go", "npm", "pnpm", "yarn", "python", "cargo", "mvn", "gradle", "dotnet", "swift", "bundle"}

// writeStubs creates stub executables on a fresh directory and returns it.
// Each stub exits with $BALLAST_STUB_EXIT (default 0) after printing its
// own name and arguments.
func writeStubs(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "stubbin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range stubNames {
		writeStub(t, filepath.Join(dir, n))
	}
	return dir
}

func writeStub(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\necho stub %~n0 %*\r\nexit /b %BALLAST_STUB_EXIT%\r\n"
		if err := os.WriteFile(path+".bat", []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	body := "#!/bin/sh\necho \"stub $(basename \"$0\") $*\"\nexit ${BALLAST_STUB_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMatrixStubMode is the stub-mode matrix of plan B3: for each of the 9
// stacks, Detect suggests the command, Propose writes project.json, and the
// runner records pass / fail for stubs that exit 0 / 1.
func TestMatrixStubMode(t *testing.T) {
	stubs := writeStubs(t)
	for _, st := range project.AllStacks {
		for _, want := range []struct {
			exit    string
			outcome model.Outcome
			code    int
		}{{"0", model.OutcomePass, 0}, {"1", model.OutcomeFail, 1}} {
			t.Run(string(st)+"/"+string(want.outcome), func(t *testing.T) {
				f := newFx(t)
				copyTree(t, filepath.Join("..", "project", "testdata", "stacks", string(st)), f.root)
				// Wrapper scripts shipped in the fixtures become stubs too.
				for _, w := range []string{"mvnw", "gradlew"} {
					if _, err := os.Stat(filepath.Join(f.root, w)); err == nil {
						if runtime.GOOS == "windows" {
							t.Skip("wrapper stubs are shell scripts")
						}
						writeStub(t, filepath.Join(f.root, w))
					}
				}
				f.vars["PATH"] = stubs + string(os.PathListSeparator) + f.vars["PATH"]
				f.vars["BALLAST_STUB_EXIT"] = want.exit
				f.rebuild()

				sug, err := project.Detect(sys.OSFS{}, f.root)
				if err != nil || len(sug) != 1 || sug[0].Stack != st {
					t.Fatalf("Detect = %+v, %v", sug, err)
				}
				cfg := project.Propose(sug)
				data, _ := json.MarshalIndent(cfg, "", " ")
				f.write(".ballast/project.json", string(data)+"\n")
				f.commit()

				rec, err := Foreground(f.opt, f.root, "test")
				if err != nil {
					t.Fatal(err)
				}
				if rec.Outcome != want.outcome {
					t.Fatalf("%s: outcome = %s (%s), want %s", sug[0].Verify.Argv, rec.Outcome, rec.Error, want.outcome)
				}
				if rec.ExitCode == nil || *rec.ExitCode != want.code {
					t.Errorf("exit code = %v", rec.ExitCode)
				}
				if !rec.ValidPass() == (want.outcome == model.OutcomePass) {
					t.Errorf("ValidPass = %v for %s", rec.ValidPass(), rec.Outcome)
				}
				log, _ := os.ReadFile(rec.LogPath)
				base := strings.TrimPrefix(filepath.Base(sug[0].Verify.Argv[0]), "./")
				if !strings.Contains(string(log), "stub "+base) {
					t.Errorf("log does not show the stub for %q: %q", sug[0].Verify.Argv, log)
				}
			})
		}
	}
}
