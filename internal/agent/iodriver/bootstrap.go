package iodriver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sync"
)

// rgBytes holds the embedded ripgrep binary bytes. Tools package
// populates this via RegisterRgBytes during init() so the iodriver
// package itself does not need to depend on the embed sub-tree.
var (
	rgBytesMu sync.RWMutex
	rgBytes   []byte
)

// RegisterRgBytes is called by `internal/agent/tools` once at process
// startup to hand over the platform-specific embedded `rg` binary
// (currently linux-amd64 only). Iodriver uses these bytes to bootstrap
// remote hosts: the first ripgrep call against a host that has no `rg`
// in PATH triggers an SFTP push to ~/.local/share/crush/bin/rg so all
// subsequent searches become single-RTT.
func RegisterRgBytes(b []byte) {
	rgBytesMu.Lock()
	defer rgBytesMu.Unlock()
	rgBytes = b
}

// embeddedRgBytes returns the registered bytes (nil if none).
func embeddedRgBytes() []byte {
	rgBytesMu.RLock()
	defer rgBytesMu.RUnlock()
	return rgBytes
}

// remoteRgPath is the standard install location for the auto-pushed rg.
const remoteRgPath = ".local/share/crush/bin/rg"

// bootstrapRemoteRg, called from sshDriver.resolveRG when the remote
// has no `rg` in PATH, attempts to SFTP-push the embedded binary so
// the next Grep call can use the fast --json path. Returns the
// absolute remote path of the installed rg, or "" if bootstrapping
// failed (in which case sshDriver.Grep transparently falls back to
// `grep -rn`).
//
// Idempotent: skips upload if a file already exists at the target.
// Best-effort: any error is logged via the returned err and the
// caller treats it as "rg unavailable".
func (d *sshDriver) bootstrapRemoteRg(ctx context.Context) (string, error) {
	bytes := embeddedRgBytes()
	if len(bytes) == 0 {
		return "", errors.New("iodriver: no embedded rg available (build excluded the bytes)")
	}
	c, err := d.sftpClient()
	if err != nil {
		return "", err
	}
	// Resolve $HOME on the remote so we install under the right user.
	stdout, _, code, err := d.Exec(ctx, []string{"sh", "-c", "echo $HOME"}, nil)
	if err != nil || code != 0 {
		return "", fmt.Errorf("iodriver: probe $HOME: %v exit=%d", err, code)
	}
	home := trimTrailingNL(string(stdout))
	if home == "" {
		return "", errors.New("iodriver: $HOME empty on remote")
	}
	target := path.Join(home, remoteRgPath)

	if _, err := c.Stat(target); err == nil {
		return target, nil // already installed
	}
	if err := c.MkdirAll(path.Dir(target)); err != nil {
		return "", fmt.Errorf("iodriver: mkdir remote bin dir: %w", err)
	}
	f, err := c.OpenFile(target, openTruncWrite)
	if err != nil {
		return "", fmt.Errorf("iodriver: open remote rg for write: %w", err)
	}
	if _, err := f.Write(bytes); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("iodriver: write remote rg: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := c.Chmod(target, fs.FileMode(0o755)); err != nil {
		return "", fmt.Errorf("iodriver: chmod remote rg: %w", err)
	}
	return target, nil
}

func trimTrailingNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// openTruncWrite mirrors the os package flag bits used by sftp.OpenFile.
const openTruncWrite = 0o102 // O_WRONLY|O_CREATE|O_TRUNC
