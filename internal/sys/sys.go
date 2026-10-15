package sys

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Clock abstracts wall time.
type Clock interface {
	Now() time.Time
}

// RealClock uses time.Now.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a settable clock for tests. Safe for concurrent use.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a FakeClock at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Set sets the clock.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// IDGen produces identifiers (job ids, backup dir suffixes, temp names).
type IDGen interface {
	// NewID returns a short random lowercase hex id of n bytes (2n chars).
	NewID(n int) string
}

// RandomID uses crypto/rand.
type RandomID struct{}

// NewID implements IDGen.
func (RandomID) NewID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; keep a deterministic fallback.
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
}

// SeqID is a deterministic IDGen for tests: "000001", "000002", ... padded/truncated to 2n chars.
type SeqID struct{ n atomic.Int64 }

// NewID implements IDGen.
func (s *SeqID) NewID(n int) string {
	v := s.n.Add(1)
	str := fmt.Sprintf("%0*x", 2*n, v)
	return str[len(str)-2*n:]
}

// Env abstracts the process environment.
type Env interface {
	Getenv(key string) (string, bool)
	// Environ returns KEY=VALUE pairs (sorted for fakes).
	Environ() []string
	// HomeDir returns the user's home directory (HOME / USERPROFILE).
	HomeDir() (string, error)
	// GOOS returns the target OS ("darwin", "linux", "windows"); fakes may override.
	GOOS() string
}

// OSEnv reads the real environment.
type OSEnv struct{}

// Getenv implements Env.
func (OSEnv) Getenv(k string) (string, bool) { return os.LookupEnv(k) }

// Environ implements Env.
func (OSEnv) Environ() []string { return os.Environ() }

// HomeDir implements Env.
func (OSEnv) HomeDir() (string, error) { return os.UserHomeDir() }

// GOOS implements Env.
func (OSEnv) GOOS() string { return goos }

// MapEnv is a fake Env. Home is taken from HOME (or USERPROFILE when OS=="windows").
type MapEnv struct {
	Vars map[string]string
	OS   string // "" ⇒ runtime GOOS
}

// Getenv implements Env.
func (m MapEnv) Getenv(k string) (string, bool) { v, ok := m.Vars[k]; return v, ok }

// Environ implements Env.
func (m MapEnv) Environ() []string {
	out := make([]string, 0, len(m.Vars))
	for k, v := range m.Vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// HomeDir implements Env.
func (m MapEnv) HomeDir() (string, error) {
	key := "HOME"
	if m.GOOS() == "windows" {
		key = "USERPROFILE"
	}
	if v, ok := m.Vars[key]; ok && v != "" {
		return v, nil
	}
	return "", fmt.Errorf("sys: %s not set in MapEnv", key)
}

// GOOS implements Env.
func (m MapEnv) GOOS() string {
	if m.OS != "" {
		return m.OS
	}
	return goos
}
