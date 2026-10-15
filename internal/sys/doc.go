// Package sys provides the injectable environment seams of Ballast: Clock,
// IDGen, Env and FS. Every unit takes these instead of calling time.Now,
// math/rand, os.Getenv or os.* directly, so tests are deterministic and can
// never touch the real home directory.
//
// Owner: architect (contract, implemented). Real implementations are thin
// wrappers over the standard library; fakes live next to them.
//
// Rule: production code outside cmd/ballast and internal/cli MUST NOT call
// time.Now, os.Getenv, os.UserHomeDir or crypto/rand directly (enforced by
// review; internal/archtest checks the hot-path packages).
package sys
