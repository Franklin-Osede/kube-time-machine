// Command ktm is the local CLI for kube-time-machine. It reads snapshots
// produced by the ktm-agent and supports listing them, showing the
// reconstructed state at any point, and diffing between two points.
//
// All command logic lives in internal/cli; this file only handles the
// process boundary (exit codes).
package main

import (
	"os"

	"github.com/Franklin-Osede/kube-time-machine/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		// Cobra has already printed the error message to stderr; we
		// just need a non-zero exit code so shells and CI report the
		// failure properly.
		os.Exit(1)
	}
}
