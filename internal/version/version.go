package version

// Version is the semantic version; "0.0.0-dev" for local builds.
var Version = "0.0.0-dev"

// Commit is the VCS revision; "" for local builds.
var Commit = ""

// String returns "ballast <Version> (<Commit>)".
func String() string {
	if Commit == "" {
		return "ballast " + Version
	}
	return "ballast " + Version + " (" + Commit + ")"
}
