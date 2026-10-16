//go:build darwin || linux

package sys

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

func euid() int { return os.Geteuid() }

func lookupHome(uid int) (string, error) {
	if uid < 0 {
		return "", ErrNoPasswdEntry
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		var unknown user.UnknownUserIdError
		if errors.As(err, &unknown) {
			return "", ErrNoPasswdEntry
		}
		return "", fmt.Errorf("sys: passwd lookup for uid %d: %w", uid, err)
	}
	// A record without a usable home field is "no entry": the caller must not
	// be handed a relative path to build trust paths from.
	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return "", ErrNoPasswdEntry
	}
	return filepath.Clean(u.HomeDir), nil
}
