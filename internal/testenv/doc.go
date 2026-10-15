// Package testenv isolates tests from the real user environment (docs/architecture.md §11).
//
// Every test that touches the file system through Layout MUST call Isolate
// (process-env based, for tests that spawn the ballast binary or call code
// using sys.OSEnv) or use Env() (sys.MapEnv based, preferred for unit tests).
// No test may read or write the real ~/.ballast, ~/.claude, ~/.codex,
// ~/.cursor or ~/.hermes. `go test ./...` must not access the network.
//
// Owner: architect (contract, implemented).
package testenv
