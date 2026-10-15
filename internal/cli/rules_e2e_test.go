package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/model"
)

// contentTargets are the rule content files the integration acceptance of
// architecture §8.3 covers (U3 + U11): every directory and file below must
// lint and test-rules cleanly through the full binary.
func contentTargets() []string {
	return []string{
		filepath.Join(repoRoot, "schema", "examples"),
		filepath.Join(repoRoot, "rules"),
		filepath.Join(repoRoot, "rules", "optional"),
		filepath.Join(repoRoot, "examples"),
		filepath.Join(repoRoot, "testdata", "bash-regress.json"),
	}
}

func TestLintAllContentFiles(t *testing.T) {
	s := newSandbox(t)
	args := append([]string{"lint", "--client", "all", "--examples"}, contentTargets()...)
	r := s.mustExit(0, "", "", args...)
	if !strings.Contains(r.stdout, "0 个错误") {
		t.Errorf("lint summary missing: %s", r.stdout)
	}
	// Every target is looked at (17 files in the repository at the time the
	// acceptance list was frozen; more files are fine, fewer are not).
	var files, errs, warns int
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasPrefix(line, "lint: ") {
			if _, err := fmt.Sscanf(line, "lint: %d 个文件,%d 个错误,%d 个警告", &files, &errs, &warns); err != nil {
				t.Fatalf("cannot parse the summary %q: %v", line, err)
			}
		}
	}
	if files < 17 {
		t.Errorf("lint covered %d files, want at least 17\n%s", files, tail(r.stdout))
	}
}

func tail(s string) string {
	if len(s) > 800 {
		return s[len(s)-800:]
	}
	return s
}

func TestLintJSONAndPerClient(t *testing.T) {
	s := newSandbox(t)
	for _, c := range []string{"claude-code", "codex", "cursor", "hermes"} {
		r := s.run("", "", "lint", "--json", "--client", c, filepath.Join(repoRoot, "rules"), filepath.Join(repoRoot, "rules", "optional"))
		if r.code != 0 {
			t.Errorf("lint --client %s: exit %d\n%s%s", c, r.code, r.stdout, r.stderr)
			continue
		}
		var out struct {
			Diagnostics []model.Diagnostic `json:"diagnostics"`
			Errors      int                `json:"errors"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatalf("lint --json output: %v\n%s", err, r.stdout)
		}
		if out.Errors != 0 {
			t.Errorf("lint --client %s: %d errors", c, out.Errors)
		}
	}
}

func TestLintReportsErrorsWithExitOne(t *testing.T) {
	s := newSandbox(t)
	// hermes cannot warn on PostToolUse: a rule file for all clients must be
	// rejected under --client hermes but accepted for claude-code.
	bad := filepath.Join(s.sb.Root, "bad.json")
	writeFile(t, bad, `{"version":1,"rules":[{"id":"warn-post","action":"warn","event":"PostToolUse",
	"match":{"tool":["Bash"]},"message":"m",
	"examples":[{"name":"h","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}},
	            {"name":"m","expect":"miss","input":{"tool_name":"Read","tool_input":{"file_path":"/x"}}}]}]}`)
	s.mustExit(0, "", "", "lint", "--client", "claude-code", bad)
	r := s.mustExit(1, "", "", "lint", "--client", "hermes", bad)
	if !strings.Contains(r.stdout, "action-unsupported") {
		t.Errorf("want action-unsupported, got:\n%s", r.stdout)
	}
	// Unreadable input is an I/O error (5); bad flags are usage errors (2).
	s.mustExit(5, "", "", "lint", filepath.Join(s.sb.Root, "missing.json"))
	s.mustExit(2, "", "", "lint", "--no-such-flag")
	s.mustExit(2, "", "", "lint", "--client", "nobody", bad)
	s.mustExit(2, "", "", "lint", "--layer", "galaxy", bad)
}

func TestLintOrgLayerCap(t *testing.T) {
	s := newSandbox(t)
	var rules []string
	for i := 0; i < 13; i++ {
		rules = append(rules, `{"id":"cap-rule-`+string(rune('a'+i))+`","action":"log","event":"PreToolUse","match":{"tool":["Bash"]},"message":"m",
		"examples":[{"name":"h","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}},
		            {"name":"m","expect":"miss","input":{"tool_name":"Read","tool_input":{"file_path":"/x"}}}]}`)
	}
	f := filepath.Join(s.sb.Root, "org13.json")
	writeFile(t, f, `{"version":1,"rules":[`+strings.Join(rules, ",")+`]}`)
	s.mustExit(0, "", "", "lint", f)
	r := s.mustExit(1, "", "", "lint", "--layer", "org", f)
	if !strings.Contains(r.stdout, "too-many-rules") {
		t.Errorf("want too-many-rules for an org file with 13 rules:\n%s", r.stdout)
	}
}

func TestTestRulesAllContentFiles(t *testing.T) {
	s := newSandbox(t)
	r := s.mustExit(0, "", "", append([]string{"test-rules"}, contentTargets()...)...)
	if !strings.Contains(r.stdout, "0 失败") {
		t.Errorf("test-rules summary: %s", r.stdout)
	}
	// Match the whole count ("test-rules: 0 通过"), not a suffix of e.g. "300 通过".
	if strings.Contains(r.stdout, "test-rules: 0 通过") {
		t.Errorf("no example ran: %s", r.stdout)
	}
	// The regression file alone, as architecture §8.3 lists it separately.
	s.mustExit(0, "", "", "test-rules", filepath.Join(repoRoot, "testdata", "bash-regress.json"))
}

func TestTestRulesFailureAndUsage(t *testing.T) {
	s := newSandbox(t)
	f := filepath.Join(s.sb.Root, "wrong.json")
	writeFile(t, f, `{"version":1,"rules":[{"id":"wrong-expect","action":"log","event":"PreToolUse",
	"match":{"tool":["Bash"],"bash":{"cmd":"sleep"}},"message":"m",
	"examples":[{"name":"claims a hit that is none","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}},
	            {"name":"m","expect":"miss","input":{"tool_name":"Read","tool_input":{"file_path":"/x"}}}]}]}`)
	r := s.mustExit(1, "", "", "test-rules", f)
	if !strings.Contains(r.stdout, "FAIL") {
		t.Errorf("want a FAIL line:\n%s", r.stdout)
	}
	s.mustExit(2, "", "", "test-rules", "--bogus")
	notJSON := filepath.Join(s.sb.Root, "garbage.json")
	writeFile(t, notJSON, "this is not json")
	s.mustExit(2, "", "", "test-rules", notJSON)
	s.mustExit(2, "", "", "test-rules", filepath.Join(s.sb.Root, "absent.json"))
}

// TestCommitFormEndToEnd is the U8 integration acceptance: the "cmd" column
// of commitform.json goes through bashparse, the git node feeds
// evidence.ParseCommitForm, and the result equals "want" (and the parsed
// invocation equals the table's "git" for sub/cwd/args).
func TestCommitFormEndToEnd(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "internal", "evidence", "testdata", "commitform.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rows []struct {
			Cmd  string               `json:"cmd"`
			Git  *model.GitInvocation `json:"git"`
			Want model.CommitForm     `json:"want"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) < 14 {
		t.Fatalf("commitform.json has %d rows, want at least 14", len(f.Rows))
	}
	for _, row := range f.Rows {
		t.Run(row.Cmd, func(t *testing.T) {
			cl, err := bashparse.Parser{}.ParseBash(row.Cmd)
			if err != nil {
				t.Fatalf("bashparse: %v", err)
			}
			var git *model.GitInvocation
			for i := range cl.Nodes {
				if g := cl.Nodes[i].Git; g != nil && g.Sub == "commit" {
					git = g
					break
				}
			}
			if git == nil {
				t.Fatalf("no git commit node in %+v", cl.Nodes)
			}
			if git.Sub != row.Git.Sub || git.Cwd != row.Git.Cwd || !reflect.DeepEqual(nonNilWords(git.Args), nonNilWords(row.Git.Args)) {
				t.Errorf("parsed git = %+v\n  table git = %+v", git, row.Git)
			}
			if got := evidence.ParseCommitForm(git, "/repo"); !reflect.DeepEqual(got, row.Want) {
				t.Errorf("ParseCommitForm = %+v\nwant %+v", got, row.Want)
			}
		})
	}
}

func nonNilWords(w []model.Word) []model.Word {
	if w == nil {
		return []model.Word{}
	}
	return w
}
