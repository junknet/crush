// Package embedbin carries prebuilt, minimal crush-remote daemon binaries
// (one per supported remote GOOS/GOARCH) embedded into the crush binary, so a
// remote_attach can deploy the daemon without a Go toolchain on either side.
//
// The binaries are produced by `task daemon` (cross-compiling ./cmd/crush-remote
// with -s -w) and are gitignored; the build regenerates them before compiling
// crush. Each is ~2.4MB, versus the ~100MB full crush binary that the daemon
// path would otherwise have to ship.
package embedbin

import (
	"embed"
	"fmt"
)

//go:generate ../../../scripts/build_remote_daemon.sh

//go:embed crush-remote_linux_amd64 crush-remote_linux_arm64
var bins embed.FS

// Daemon returns the prebuilt crush-remote bytes for the given target, or an
// error naming the missing target when it is not embedded.
func Daemon(goos, goarch string) ([]byte, error) {
	name := fmt.Sprintf("crush-remote_%s_%s", goos, goarch)
	data, err := bins.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("no embedded crush-remote daemon for %s/%s (have linux/amd64, linux/arm64)", goos, goarch)
	}
	return data, nil
}
