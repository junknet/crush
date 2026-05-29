// Command crush-remote is the minimal daemon for crush's remote IO driver.
//
// It is deliberately tiny: it imports only internal/iodriver (which depends on
// the standard library alone), so it compiles to a few MB instead of the ~100MB
// full crush binary. crush cross-compiles it for the target host, scps it once
// (cached by content hash), and launches it as `crush-remote` over an SSH stdio
// channel. The client's RemoteBackend then proxies file and exec operations
// here so the agent operates the remote host as if it were local.
//
// Protocol/version compatibility is negotiated at the RPC layer (Initialize),
// not by shipping the whole binary — which is why a separate, minimal daemon is
// correct despite the "one binary" instinct.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/crush/internal/iodriver"
)

func main() {
	if err := iodriver.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "crush-remote:", err)
		os.Exit(1)
	}
}
