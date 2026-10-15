//go:build tools

// Package tools pins third-party dependencies agreed in docs/ballast-plan.md.
package tools

import (
	_ "github.com/bmatcuk/doublestar/v4"
	_ "mvdan.cc/sh/v3/syntax"
)
