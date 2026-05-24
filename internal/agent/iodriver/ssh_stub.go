package iodriver

import (
	"context"
	"errors"
	"net/url"
)

// dialSSH is implemented in ssh.go once the SSH driver is built. The
// stub keeps the package building during incremental rollout — calling
// it returns ErrSSHNotAvailable.
//
// M3 will replace this with the real crypto/ssh + sftp implementation
// plus persistent PTY shell.
func dialSSH(ctx context.Context, u *url.URL, cwd string) (Driver, error) {
	return nil, ErrSSHNotAvailable
}

// ErrSSHNotAvailable is returned when an SSH workspace is requested
// before the SSH driver has been wired up (M3) or when the build
// excludes SSH support.
var ErrSSHNotAvailable = errors.New("iodriver: SSH workspace driver not available in this build (M3 pending)")
