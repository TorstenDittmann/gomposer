package main

import (
	"fmt"
	"os"

	"github.com/torstendittmann/gomposer/internal/cli"
)

// These are overridden at release-build time via -ldflags. GoReleaser
// stamps in the tag, commit sha, and build date; unstamped local builds
// (`go install`, `go build`) fall back to "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.Execute(versionString()); err != nil {
		os.Exit(1)
	}
}

func versionString() string {
	if version == "dev" && commit == "none" {
		return "dev"
	}
	return fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
}
