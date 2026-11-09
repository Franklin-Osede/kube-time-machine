package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Options holds the persistent flags shared by every subcommand. Subcommands
// keep the pointer so they read values after flag parsing.
type Options struct {
	StorageDir string
}

// NewRootCmd builds the ktm command tree; version backs `ktm --version`.
func NewRootCmd(version string) *cobra.Command {
	opts := &Options{}

	cmd := &cobra.Command{
		Use:     "ktm",
		Short:   "Git blame & time-travel for your Kubernetes cluster",
		Long:    "ktm reads the snapshot history recorded by ktm-agent: list snapshots, reconstruct the state at any point, diff two points in time, blame a resource's changes, and roll a resource back.",
		Version: version,
		// Usage text helps with a mistyped command, not a runtime error.
		SilenceUsage: true,
	}

	cmd.PersistentFlags().StringVar(&opts.StorageDir, "storage-dir", defaultStorageDir(),
		"directory where snapshots are persisted (must match the agent's --storage-dir)")

	cmd.AddCommand(newSnapshotCmd(opts))
	cmd.AddCommand(newDiffCmd(opts))
	cmd.AddCommand(newBlameCmd(opts))
	cmd.AddCommand(newRollbackCmd(opts))

	return cmd
}

// defaultStorageDir returns $HOME/.ktm/data. It differs from the agent's
// /var/lib/ktm because the CLI runs on a workstation, not in a pod.
func defaultStorageDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ktm", "data")
	}
	return "."
}
