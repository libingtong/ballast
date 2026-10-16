package tier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// loadOrgConfig implements LoadOrgConfig.
func loadOrgConfig(fsys sys.FS, p string) (OrgConfig, error) {
	b, err := fsys.ReadFile(p)
	if err != nil {
		return OrgConfig{}, err
	}
	return parseOrgConfig(b, orgDirOf(p))
}

// orgDirOf is the protected org directory (<managed>/ballast) that holds p.
func orgDirOf(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return path.Dir(p)
}

func parseOrgConfig(b []byte, orgDir string) (OrgConfig, error) {
	var o OrgConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&o); err != nil {
		return OrgConfig{}, fmt.Errorf("tier: org.json: %w", err)
	}
	// Exactly one JSON value.
	if dec.More() {
		return OrgConfig{}, errors.New("tier: org.json: trailing data")
	}
	if o.V != 1 {
		return OrgConfig{}, fmt.Errorf("tier: org.json: unsupported v %d", o.V)
	}
	switch o.Tier {
	case "", model.TierT0, model.TierT1:
	default:
		return OrgConfig{}, fmt.Errorf("tier: org.json: invalid tier %q", o.Tier)
	}
	switch o.RunnerSandbox {
	case "", model.SandboxRequire, model.SandboxPrefer, model.SandboxOff:
	default:
		return OrgConfig{}, fmt.Errorf("tier: org.json: invalid runner_sandbox %q", o.RunnerSandbox)
	}
	switch o.RunnerNetwork {
	case "", model.NetworkDeny, model.NetworkLoopback, model.NetworkAllow:
	default:
		return OrgConfig{}, fmt.Errorf("tier: org.json: invalid runner_network %q", o.RunnerNetwork)
	}
	switch o.WeakenedProject {
	case "", model.WeakenedProjectAudit, model.WeakenedProjectDeny:
	default:
		return OrgConfig{}, fmt.Errorf("tier: org.json: invalid weakened_project %q", o.WeakenedProject)
	}
	for _, e := range o.ProtectedExtra {
		if err := checkOrgPath("protected_extra", e); err != nil {
			return OrgConfig{}, err
		}
	}
	for _, e := range o.RunnerAllowWrite {
		if err := checkOrgPath("runner_allow_write", e); err != nil {
			return OrgConfig{}, err
		}
		if err := checkAllowWrite(e, orgDir); err != nil {
			return OrgConfig{}, err
		}
	}
	for _, e := range o.HomeRoot {
		if err := checkHomeRoot(e); err != nil {
			return OrgConfig{}, err
		}
	}
	return o, nil
}

func checkOrgPath(key, e string) error {
	if strings.HasPrefix(e, "/") || strings.HasPrefix(e, "~/") || filepath.IsAbs(e) {
		return nil
	}
	return fmt.Errorf("tier: org.json: %s entry %q must be absolute or start with ~/", key, e)
}

// homeAncestors are never acceptable as runner_allow_write (a writable home
// or one of its ancestors lets sandboxed code persist through shell rc
// files).
func isHomeAncestor(c string) bool {
	switch c {
	case "/", "/home", "/Users", "/root", "/var/root":
		return true
	}
	return false
}

func checkAllowWrite(e, orgDir string) error {
	bad := func(why string) error {
		return fmt.Errorf("tier: org.json: runner_allow_write entry %q %s", e, why)
	}
	if e == "~" || e == "~/" {
		return bad("is the home directory")
	}
	if strings.HasPrefix(e, "~/") {
		// Under the home directory; refuse only escapes.
		for _, seg := range strings.Split(e[2:], "/") {
			if seg == ".." {
				return bad("escapes the home directory")
			}
		}
		if path.Clean(e[2:]) == "." {
			return bad("is the home directory")
		}
		return nil
	}
	c := path.Clean(strings.ReplaceAll(e, `\`, "/"))
	if isHomeAncestor(c) {
		return bad("is a home directory or one of its ancestors")
	}
	for _, base := range []string{"/home/", "/Users/"} {
		if rest, ok := strings.CutPrefix(c, base); ok && rest != "" && !strings.Contains(rest, "/") {
			return bad("is a user's home directory")
		}
	}
	od := path.Clean(orgDir)
	if od != "." && (c == od || strings.HasPrefix(c, od+"/")) {
		return bad("lies under the protected org directory")
	}
	return nil
}

func checkHomeRoot(e string) error {
	bad := func(why string) error {
		return fmt.Errorf("tier: org.json: home_root entry %q %s", e, why)
	}
	if !strings.HasPrefix(e, "/") {
		return bad("is not an absolute path")
	}
	if path.Clean(e) != e {
		return bad("is not a clean path")
	}
	if e == "/" {
		return bad("must not be /")
	}
	if strings.ContainsAny(e, "~*?[]{}") {
		return bad("must not contain ~ or glob characters")
	}
	return nil
}

// effectiveWeakened is EffectiveWeakenedProject.
func effectiveWeakened(org OrgConfig) model.WeakenedProjectPolicy {
	if org.WeakenedProject == "" {
		return DefaultWeakenedProject
	}
	return org.WeakenedProject
}

// expandHome expands a leading "~/" against home; other entries are returned
// unchanged. An empty home leaves "~/" entries untouched.
func expandHome(p, home string) string {
	if home != "" && strings.HasPrefix(p, "~/") {
		return path.Join(home, p[2:])
	}
	return p
}
