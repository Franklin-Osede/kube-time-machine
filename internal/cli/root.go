package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Options holds the values of the persistent flags shared by every
// subcommand. The pointer is passed into each subcommand constructor so
// they read the value after flag parsing, not at construction time.
type Options struct {
	StorageDir string
}

// NewRootCmd builds the ktm command tree without executing it. version is
// the linker-stamped value from cmd/ktm and backs `ktm --version`.
func NewRootCmd(version string) *cobra.Command {
	opts := &Options{}

	cmd := &cobra.Command{
		Use:     "ktm",
		Short:   "Git blame & time-travel for your Kubernetes cluster",
		Long:    "ktm reads the snapshot history recorded by ktm-agent: list snapshots, reconstruct the state at any point, diff two points in time, blame a resource's changes, and roll a resource back.",
		Version: version,
		// A runtime error (missing snapshot, conflict) prints just the error,
		// not the usage text, which only helps with a mistyped command.
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

// defaultStorageDir picks a sensible per-user default for local
// development: $HOME/.ktm/data. The agent's default in-cluster path
// (/var/lib/ktm) is intentionally different because the CLI runs on the
// engineer's laptop, not in a pod.
func defaultStorageDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ktm", "data")
	}
	return "."
}
