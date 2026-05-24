package cli

import (
	"fmt"
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

// NewRootCmd builds the top-level cobra command. It does not call
// cmd.Execute — that's the caller's job in cmd/ktm/main.go. The version
// string is plumbed in from cmd/ktm/main.go so the linker-stamped value
// (-X main.version=...) reaches cobra's built-in Version field, which
// powers `ktm --version` and `ktm version`.
func NewRootCmd(version string) *cobra.Command {
	opts := &Options{}

	cmd := &cobra.Command{
		Use:           "ktm",
		Short:         "Git blame & time-travel for your Kubernetes cluster",
		Long:          "ktm inspects the snapshot history produced by the ktm-agent: list snapshots, show their contents, and diff between any two points in time.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
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

// errf is a tiny helper to format command errors consistently — callers
// inside subcommands return errf("...", ...) so cobra prints "Error: ..."
// without leaking internal package names.
func errf(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}
