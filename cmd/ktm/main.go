// Command ktm is the local CLI for kube-time-machine. It reads the snapshots
// ktm-agent writes and lists, reconstructs, diffs, blames and rolls back
// from them. Command logic lives in internal/cli.
package main

import (
	"os"

	"github.com/Franklin-Osede/kube-time-machine/internal/cli"
)

// version is set at release time with -ldflags="-X main.version=...".
var version = "dev"

func main() {
	if err := cli.NewRootCmd(version).Execute(); err != nil {
		// Cobra has already printed the error.
		os.Exit(1)
	}
}
