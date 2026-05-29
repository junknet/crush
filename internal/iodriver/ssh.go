package iodriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/iodriver/embedbin"
)

// sshConnectTimeout mirrors the SSH tools' batch-mode connect timeout so a dead
// host fails fast instead of hanging the agent.
const sshConnectTimeout = "ConnectTimeout=30"

// sshControlOpts returns the OpenSSH options that reuse a single multiplexed
// connection (ControlMaster) across the deploy probe, the scp, and the long
// lived serve channel — the same scheme the legacy ssh tools use, so an attach
// pays the handshake cost once.
func sshControlOpts(dataDir string) []string {
	socketDir := filepath.Join(dataDir, "ssh_sockets")
	_ = os.MkdirAll(socketDir, 0o700)
	controlPath := filepath.Join(socketDir, "%r@%h:%p")
	return []string{
		// Compression: the daemon binary is large and the JSON RPC channel is
		// highly compressible, so -C cuts first-attach scp time and per-op
		// latency over a WAN. Accepted by both ssh and scp.
		"-C",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlPersist=600",
		"-o", sshConnectTimeout,
	}
}

// sshGOARCH maps `uname -m` output to Go's GOARCH vocabulary.
func sshGOARCH(machine string) string {
	switch strings.TrimSpace(machine) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.TrimSpace(machine)
	}
}

// detectRemote returns the remote GOOS/GOARCH and home dir in one round-trip.
func detectRemote(ctx context.Context, dataDir, host string) (goos, goarch, home string, err error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", "", "", fmt.Errorf("ssh not found in PATH")
	}
	args := append(sshControlOpts(dataDir), host, "printf '%s\\n%s\\n%s\\n' \"$(uname -s)\" \"$(uname -m)\" \"$HOME\"")
	out, err := exec.CommandContext(ctx, sshPath, args...).Output()
	if err != nil {
		return "", "", "", fmt.Errorf("probe remote %s: %w", host, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 3 {
		return "", "", "", fmt.Errorf("probe remote %s: unexpected output %q", host, string(out))
	}
	return strings.ToLower(strings.TrimSpace(lines[0])), sshGOARCH(lines[1]), strings.TrimSpace(lines[2]), nil
}

// hashBytes returns a short content hash used to name the deployed daemon so a
// stale remote copy is never reused after the daemon changes.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// deployDaemon ensures the minimal crush-remote daemon (matching this client's
// content hash) exists on the remote and returns its remote path. It ships the
// ~2.4MB embedded daemon for the remote's GOOS/GOARCH — NOT the ~100MB full
// crush binary — written to a local temp and scp'd, cached on the remote by
// content hash so subsequent attaches skip the copy.
func deployDaemon(ctx context.Context, dataDir, host string) (remotePath, remoteHome string, err error) {
	goos, goarch, home, err := detectRemote(ctx, dataDir, host)
	if err != nil {
		return "", "", err
	}
	daemon, err := embedbin.Daemon(goos, goarch)
	if err != nil {
		return "", "", err
	}
	hash := hashBytes(daemon)
	// Relative-to-home remote path so scp/ssh resolve it against $HOME.
	relPath := ".cache/crush/crush-remote-" + hash
	remotePath = home + "/" + relPath

	sshPath, _ := exec.LookPath("ssh")
	scpPath, err := exec.LookPath("scp")
	if err != nil {
		return "", "", fmt.Errorf("scp not found in PATH")
	}

	// Skip the copy when a binary with this exact hash is already present.
	checkArgs := append(sshControlOpts(dataDir), host, "test -x "+relPath)
	if err := exec.CommandContext(ctx, sshPath, checkArgs...).Run(); err == nil {
		return remotePath, home, nil
	}

	// Stage the embedded daemon to a local temp file for scp.
	localTmp, err := os.CreateTemp("", "crush-remote-*")
	if err != nil {
		return "", "", fmt.Errorf("stage daemon: %w", err)
	}
	localPath := localTmp.Name()
	defer os.Remove(localPath)
	if _, err := localTmp.Write(daemon); err != nil {
		localTmp.Close()
		return "", "", fmt.Errorf("stage daemon: %w", err)
	}
	localTmp.Close()

	mkdirArgs := append(sshControlOpts(dataDir), host, "mkdir -p .cache/crush")
	if out, err := exec.CommandContext(ctx, sshPath, mkdirArgs...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("remote mkdir: %w: %s", err, out)
	}
	// scp to a temp name then atomically rename, so a concurrent attach never
	// execs a half-copied binary.
	tmpRel := relPath + ".tmp"
	scpArgs := append(sshControlOpts(dataDir), localPath, host+":"+tmpRel)
	if out, err := exec.CommandContext(ctx, scpPath, scpArgs...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("scp daemon to %s: %w: %s", host, err, out)
	}
	finalizeArgs := append(sshControlOpts(dataDir), host, "chmod +x "+tmpRel+" && mv -f "+tmpRel+" "+relPath)
	if out, err := exec.CommandContext(ctx, sshPath, finalizeArgs...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("finalize daemon on %s: %w: %s", host, err, out)
	}
	return remotePath, home, nil
}

// sshTransport is the io.Closer for an SSH-backed RemoteBackend: closing it
// shuts stdin (signalling the daemon's Serve loop to exit on EOF) and reaps the
// ssh process.
type sshTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func (t *sshTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}

// DialSSH deploys the daemon to host (if needed) and opens a RemoteBackend over
// `ssh host <daemon>`, using that process's stdio as the RPC channel. The
// crush-remote daemon serves immediately on start (no subcommand). remoteRoot,
// if empty, defaults to the remote home dir.
func DialSSH(ctx context.Context, dataDir, host, remoteRoot string) (*RemoteBackend, error) {
	remotePath, home, err := deployDaemon(ctx, dataDir, host)
	if err != nil {
		return nil, err
	}
	if remoteRoot == "" {
		remoteRoot = home
	}
	sshPath, _ := exec.LookPath("ssh")
	args := append(sshControlOpts(dataDir), host, remotePath)
	cmd := exec.Command(sshPath, args...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("daemon stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("daemon stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start daemon on %s: %w", host, err)
	}
	transport := &sshTransport{cmd: cmd, stdin: stdin}
	return NewRemoteBackend(stdout, stdin, transport, host, remoteRoot), nil
}
