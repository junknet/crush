package cmd

import (
	"github.com/charmbracelet/crush/internal/iodriver"
	"github.com/spf13/cobra"
)

// remoteServeCmd is the daemon side of the remote IO driver. It is launched on
// the target host (e.g. `ssh host crush __remote-serve`) and speaks the
// length-free JSON protocol over stdin/stdout: the client's RemoteBackend
// proxies file (and, later, exec) operations here, so the agent operates the
// remote machine as if it were local. Hidden because it is never invoked by a
// human directly.
var remoteServeCmd = &cobra.Command{
	Use:    "__remote-serve",
	Short:  "Run the remote IO daemon over stdin/stdout (internal)",
	Hidden: true,
	Args:   cobra.NoArgs,
	// Silence usage/errors: this command's stdout is the RPC channel and must
	// not be polluted by Cobra help text on error.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return iodriver.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
	},
}
