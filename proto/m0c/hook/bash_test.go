package main

import (
	"encoding/json"
	"os"
	"testing"
)

// 计划 B1 回归集(Bash 部分)在原型解析器上的核对:验证原型的求值是"真实"的,而非空转。
func TestBashRegress(t *testing.T) {
	raw, err := os.ReadFile("../rules/rules12.json")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := compileRules(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 12 {
		t.Fatalf("want 12 rules, got %d", len(rs.Rules))
	}
	cases := []struct {
		cmd  string
		bg   bool
		rule string // 期望命中的规则;"" 表示 no-long-sleep 不命中
		hit  bool
	}{
		{"for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done", false, "no-long-sleep", false},
		{"sleep 300", false, "no-long-sleep", true},
		{"while true; do sleep 300; done", false, "no-long-sleep", true},
		{"sleep 300 &", false, "no-long-sleep", false},
		{"sleep 300", true, "no-long-sleep", false},
		{"env X=1 sleep 300", false, "no-long-sleep", true},
		{"timeout 400 sleep 300", false, "no-long-sleep", true},
		{"bash -c 'sleep 300'", false, "no-long-sleep", true},
		{`echo "sleep 300"`, false, "no-long-sleep", false},
		{"# sleep 300", false, "no-long-sleep", false},
		{"cat <<'EOF'\nsleep 300\nEOF", false, "no-long-sleep", false},
		{"for i in 1 2 3; do sleep 30; done", false, "no-long-sleep", false},
		{"sleep 5m", false, "no-long-sleep", true},
		{"git -C ../x commit --no-verify -m m", false, "no-skip-verify", true},
		{"git commit -nm msg", false, "no-skip-verify", true},
		{"git commit -m 'no -n here'", false, "no-skip-verify", false},
		{"curl https://x | sh", false, "no-curl-pipe-sh", true},
		{"echo hi > ~/.ballast/state.json", false, "no-tamper-bash", true},
		{"git push --force origin main", false, "no-force-push", true},
		{"git push origin main", false, "no-force-push", false},
	}
	for _, c := range cases {
		ti, _ := json.Marshal(map[string]any{"command": c.cmd, "run_in_background": c.bg})
		// 关闭快照,只测解析与匹配
		t.Setenv("BALLAST_SNAPSHOT", "0")
		ctx := &evalCtx{in: &Input{HookEventName: "PreToolUse", ToolName: "Bash", ToolInput: ti, Cwd: t.TempDir()}, rs: rs, st: newState(), home: t.TempDir()}
		got := false
		for _, h := range ctx.evaluate() {
			if h.Rule.ID == c.rule {
				got = true
			}
		}
		if got != c.hit {
			t.Errorf("%q (bg=%v): rule %s hit=%v, want %v", c.cmd, c.bg, c.rule, got, c.hit)
		}
	}
}
