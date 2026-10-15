package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// geteuid is os.Geteuid; tests replace it.
var geteuid = os.Geteuid

type orgHook struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type orgGroup struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []orgHook `json:"hooks"`
}

type orgFS struct {
	DenyWrite []string `json:"denyWrite"`
}

type orgSandbox struct {
	Enabled                  bool     `json:"enabled"`
	AllowUnsandboxedCommands bool     `json:"allowUnsandboxedCommands"`
	FailIfUnavailable        bool     `json:"failIfUnavailable"`
	ExcludedCommands         []string `json:"excludedCommands"`
	Filesystem               orgFS    `json:"filesystem"`
}

type orgPermissions struct {
	Deny []string `json:"deny"`
}

type orgMCPServer struct {
	ServerName string `json:"serverName"`
}

// orgSettings is the T1 managed-settings fragment; field order is the key
// order of the rendered JSON.
type orgSettings struct {
	AllowManagedHooksOnly           bool                  `json:"allowManagedHooksOnly"`
	Hooks                           map[string][]orgGroup `json:"hooks"`
	Sandbox                         orgSandbox            `json:"sandbox"`
	Permissions                     orgPermissions        `json:"permissions"`
	AllowManagedPermissionRulesOnly bool                  `json:"allowManagedPermissionRulesOnly"`
	AllowManagedMCPServersOnly      bool                  `json:"allowManagedMcpServersOnly,omitempty"`
	AllowedMCPServers               []orgMCPServer        `json:"allowedMcpServers,omitempty"`
}

// dirOf is filepath.Dir for either separator style.
func dirOf(p, goos string) string {
	seps := "/"
	if goos == "windows" {
		seps = `/\`
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == seps[0] || (len(seps) > 1 && p[i] == seps[1]) {
			if i == 0 {
				return p[:1]
			}
			return p[:i]
		}
	}
	return "."
}

func renderOrg(lay layout.Layout, fsys sys.FS, opt OrgRenderOptions) ([]byte, error) {
	goos := firstNonEmpty(opt.GOOS, lay.GOOS)
	if goos == "" {
		goos = "linux"
	}
	binary := opt.Binary
	if binary == "" {
		if goos == "windows" {
			binary = `C:\Program Files\Ballast\ballast.exe`
		} else {
			binary = "/usr/local/libexec/ballast/ballast"
		}
	}

	protected := []string{
		dirOf(lay.OrgRules, goos),
		"~/.ballast",
		"./.ballast",
		dirOf(binary, goos),
	}
	protected = append(protected, opt.Protected...)
	protected = dedupe(protected)

	var deny []string
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		for _, p := range protected {
			deny = append(deny, fmt.Sprintf("%s(%s/**)", tool, p))
		}
		deny = append(deny, fmt.Sprintf("%s(~/.claude/settings*.json)", tool))
	}
	denyWrite := append(append([]string{}, protected...), "~/.claude/settings*.json")

	s := orgSettings{
		AllowManagedHooksOnly: true,
		Hooks:                 map[string][]orgGroup{},
		Sandbox: orgSandbox{
			Enabled: true, AllowUnsandboxedCommands: false, FailIfUnavailable: true,
			ExcludedCommands: []string{},
			Filesystem:       orgFS{DenyWrite: denyWrite},
		},
		Permissions:                     orgPermissions{Deny: deny},
		AllowManagedPermissionRulesOnly: true,
	}
	for _, ne := range model.NativeEventTable[model.ClientClaude] {
		if !ne.Install {
			continue
		}
		g := orgGroup{Hooks: []orgHook{{Type: "command", Command: binary, Args: HookArgv(binary, model.ClientClaude, ne.Native)[1:]}}}
		if ne.Event.IsToolEvent() {
			g.Matcher = "*"
		}
		s.Hooks[ne.Native] = []orgGroup{g}
	}
	if len(opt.MCPAllow) > 0 {
		s.AllowManagedMCPServersOnly = true
		for _, n := range opt.MCPAllow {
			s.AllowedMCPServers = append(s.AllowedMCPServers, orgMCPServer{ServerName: n})
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&s); err != nil {
		return nil, err
	}
	out := buf.Bytes()

	if opt.Write {
		if !opt.IAmAdmin {
			return out, fmt.Errorf("%w: --write requires --i-am-admin", ErrOrgWriteDenied)
		}
		if geteuid() != 0 {
			return out, fmt.Errorf("%w: --write requires root", ErrOrgWriteDenied)
		}
		dst := filepath.Join(lay.ClaudeManagedSettingsD(), "ballast.json")
		if err := fsys.MkdirAll(lay.ClaudeManagedSettingsD(), 0o755); err != nil {
			return out, fmt.Errorf("create %s: %w", lay.ClaudeManagedSettingsD(), err)
		}
		if err := sys.WriteFileAtomic(fsys, sys.RandomID{}, dst, out, 0o644, true); err != nil {
			return out, fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
