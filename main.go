package main

import "github.com/ge/subscription-monitor/cmd"

// version/commit are injected at build time via -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "none"
)

func main() {
	cmd.SetVersion(version, commit)
	cmd.Execute()
}
