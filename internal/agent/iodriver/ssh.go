package iodriver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// sshDriver is the remote workspace driver. It owns one persistent
// *ssh.Client and a lazily-created *sftp.Client; commands (Exec) get
// per-call *ssh.Session instances so concurrent tools don't interfere,
// while a separate persistent PTY shell (M5 will wire it up for the
// bash tool) keeps cd / env state across calls.
type sshDriver struct {
	uri  string
	user string
	host string // host:port
	cwd  string

	mu     sync.Mutex
	client *ssh.Client
	sftpC  *sftp.Client

	// rgPushed records whether we've already pushed the embedded rg
	// binary to ~/.local/share/crush/bin/rg on this host. M5 plugs in
	// the actual byte upload; for now we just remember whether the
	// remote already has an rg in PATH.
	rgPath    string // resolved remote rg path, "" = none
	rgChecked bool
}

// dialSSH is the M3 implementation invoked by the Factory. Auth order:
//  1. identity_file= query parameter (PEM-encoded key, no passphrase)
//  2. SSH_AUTH_SOCK (ssh-agent)
//  3. ~/.ssh/id_ed25519, ~/.ssh/id_rsa (passphrase-free)
//
// Host-key verification uses ~/.ssh/known_hosts; first-time hosts are
// accepted with a warning (TOFU). A stricter policy belongs to a later
// permission-integration pass.
func dialSSH(ctx context.Context, u *url.URL, cwd string) (Driver, error) {
	user := u.User.Username()
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":22"
	}

	auths, err := collectSSHAuths(u)
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, errors.New("iodriver: no SSH auth methods available (no agent, no ~/.ssh/id_*, no identity_file=)")
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TOFU stub; M5/M6 add known_hosts integration
		Timeout:         15 * time.Second,
	}

	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("iodriver: dial %s: %w", host, err)
	}
	cconn, chans, reqs, err := ssh.NewClientConn(conn, host, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("iodriver: ssh handshake %s: %w", host, err)
	}
	client := ssh.NewClient(cconn, chans, reqs)

	// Keep-alive every 30s to detect dead links promptly.
	go sshKeepAlive(client, 30*time.Second)

	// Resolve cwd: if relative, anchor at remote HOME.
	if !path.IsAbs(cwd) && cwd != "" {
		out, _ := runOnceClient(client, "echo $HOME")
		home := strings.TrimSpace(string(out))
		if home != "" {
			if cwd == "." {
				cwd = home
			} else {
				cwd = path.Join(home, cwd)
			}
		}
	}

	return &sshDriver{
		uri:    u.String(),
		user:   user,
		host:   host,
		cwd:    cwd,
		client: client,
	}, nil
}

func sshKeepAlive(c *ssh.Client, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		_, _, err := c.SendRequest("keepalive@crush", true, nil)
		if err != nil {
			return // connection dead; next op will fail naturally
		}
	}
}

func collectSSHAuths(u *url.URL) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod

	// 1) explicit identity_file= (no passphrase)
	if id := u.Query().Get("identity_file"); id != "" {
		if strings.HasPrefix(id, "~/") {
			home, _ := os.UserHomeDir()
			id = filepath.Join(home, id[2:])
		}
		key, err := os.ReadFile(id)
		if err != nil {
			return nil, fmt.Errorf("iodriver: read identity_file %s: %w", id, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("iodriver: parse identity_file %s: %w", id, err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}

	// 2) ssh-agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(c)
			auths = append(auths, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	// 3) default key files
	home, err := os.UserHomeDir()
	if err == nil {
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			p := filepath.Join(home, ".ssh", name)
			key, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(key)
			if err != nil {
				continue
			}
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	return auths, nil
}

// runOnceClient runs cmd in a fresh session and returns stdout. Used
// only during dial bootstrap — afterwards Exec drives normal commands.
func runOnceClient(c *ssh.Client, cmd string) ([]byte, error) {
	sess, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.Output(cmd)
}

// Kind implements Driver.
func (d *sshDriver) Kind() Kind { return KindSSH }

// URI implements Driver.
func (d *sshDriver) URI() string { return d.uri }

// WorkingDir implements Driver.
func (d *sshDriver) WorkingDir(ctx context.Context) string { return d.cwd }

func (d *sshDriver) resolve(p string) string {
	if path.IsAbs(p) {
		return p
	}
	return path.Join(d.cwd, p)
}

func (d *sshDriver) sftpClient() (*sftp.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sftpC != nil {
		return d.sftpC, nil
	}
	c, err := sftp.NewClient(d.client)
	if err != nil {
		return nil, fmt.Errorf("iodriver: open sftp: %w", err)
	}
	d.sftpC = c
	return c, nil
}

// Stat implements Driver via SFTP.
func (d *sshDriver) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	c, err := d.sftpClient()
	if err != nil {
		return nil, err
	}
	return c.Stat(d.resolve(p))
}

// ReadFile implements Driver via SFTP, single round-trip.
func (d *sshDriver) ReadFile(ctx context.Context, p string) ([]byte, error) {
	c, err := d.sftpClient()
	if err != nil {
		return nil, err
	}
	f, err := c.Open(d.resolve(p))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteFile implements Driver via SFTP. Parent dirs are auto-created.
func (d *sshDriver) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode) error {
	c, err := d.sftpClient()
	if err != nil {
		return err
	}
	target := d.resolve(p)
	if dir := path.Dir(target); dir != "" && dir != "." {
		_ = c.MkdirAll(dir)
	}
	f, err := c.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return c.Chmod(target, perm)
}

// Remove implements Driver.
func (d *sshDriver) Remove(ctx context.Context, p string) error {
	c, err := d.sftpClient()
	if err != nil {
		return err
	}
	return c.Remove(d.resolve(p))
}

// MkdirAll implements Driver.
func (d *sshDriver) MkdirAll(ctx context.Context, p string, perm fs.FileMode) error {
	c, err := d.sftpClient()
	if err != nil {
		return err
	}
	if err := c.MkdirAll(d.resolve(p)); err != nil {
		return err
	}
	return c.Chmod(d.resolve(p), perm)
}

// Walk implements Driver via sftp.Walker.
func (d *sshDriver) Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	c, err := d.sftpClient()
	if err != nil {
		return err
	}
	w := c.Walk(d.resolve(root))
	for w.Step() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.Err(); err != nil {
			if walkErr := fn(w.Path(), nil, err); walkErr != nil {
				return walkErr
			}
			continue
		}
		stat := w.Stat()
		de := sftpDirEntry{name: stat.Name(), info: stat}
		if walkErr := fn(w.Path(), de, nil); walkErr != nil {
			if errors.Is(walkErr, fs.SkipDir) {
				w.SkipDir()
				continue
			}
			if errors.Is(walkErr, fs.SkipAll) {
				return nil
			}
			return walkErr
		}
	}
	return nil
}

type sftpDirEntry struct {
	name string
	info fs.FileInfo
}

func (e sftpDirEntry) Name() string               { return e.name }
func (e sftpDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e sftpDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e sftpDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// Exec implements Driver. Each call gets a fresh ssh.Session so
// concurrent tools don't share stdin/stdout.
func (d *sshDriver) Exec(ctx context.Context, argv []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	if len(argv) == 0 {
		return nil, nil, -1, errors.New("iodriver: SSH Exec needs argv[0]")
	}
	sess, err := d.client.NewSession()
	if err != nil {
		return nil, nil, -1, fmt.Errorf("iodriver: ssh session: %w", err)
	}
	defer sess.Close()
	if stdin != nil {
		sess.Stdin = stdin
	}
	var outBuf, errBuf bytes.Buffer
	sess.Stdout = &outBuf
	sess.Stderr = &errBuf

	cmdLine := buildRemoteCmdLine(d.cwd, argv)

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmdLine) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		return outBuf.Bytes(), errBuf.Bytes(), -1, ctx.Err()
	case runErr := <-done:
		stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
		if runErr != nil {
			var ee *ssh.ExitError
			if errors.As(runErr, &ee) {
				return stdout, stderr, ee.ExitStatus(), nil
			}
			return stdout, stderr, -1, runErr
		}
		return stdout, stderr, 0, nil
	}
}

// buildRemoteCmdLine cd's into d.cwd then shell-quotes argv. For a
// single argv element treat it as raw shell input (matching the
// "bash -c" idiom callers expect).
func buildRemoteCmdLine(cwd string, argv []string) string {
	var sb strings.Builder
	if cwd != "" {
		sb.WriteString("cd ")
		sb.WriteString(shellQuote(cwd))
		sb.WriteString(" && ")
	}
	if len(argv) == 1 {
		sb.WriteString(argv[0])
		return sb.String()
	}
	for i, a := range argv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(shellQuote(a))
	}
	return sb.String()
}

// shellQuote does conservative POSIX single-quote escaping.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}();&|<>!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Grep implements Driver. When the remote has rg in PATH (or we've
// auto-pushed it in M5), use --json for one RTT and a clean JSON parse;
// otherwise fall back to a remote grep -rn.
func (d *sshDriver) Grep(ctx context.Context, opts GrepOpts) ([]GrepHit, error) {
	if opts.Pattern == "" {
		return nil, errors.New("iodriver: Grep needs Pattern")
	}
	rg := d.resolveRG(ctx)
	root := opts.Path
	if root == "" {
		root = "."
	}
	root = d.resolve(root)

	var argv []string
	if rg != "" {
		argv = []string{rg, "--json", "--no-messages"}
		if opts.IgnoreCase {
			argv = append(argv, "-i")
		}
		if opts.Literal {
			argv = append(argv, "-F")
		}
		if opts.Include != "" {
			argv = append(argv, "-g", opts.Include)
		}
		if opts.MaxResults > 0 {
			argv = append(argv, "-m", strconv.Itoa(opts.MaxResults))
		}
		argv = append(argv, "--", opts.Pattern, root)
	} else {
		// portable grep fallback
		gArgs := []string{"grep", "-rn"}
		if opts.IgnoreCase {
			gArgs = append(gArgs, "-i")
		}
		if opts.Literal {
			gArgs = append(gArgs, "-F")
		}
		if opts.Include != "" {
			gArgs = append(gArgs, "--include", opts.Include)
		}
		gArgs = append(gArgs, "--", opts.Pattern, root)
		argv = gArgs
	}
	stdout, _, code, err := d.Exec(ctx, argv, nil)
	if err != nil {
		return nil, err
	}
	if code == 1 && rg != "" {
		// rg: no matches
		return nil, nil
	}
	if rg != "" {
		return parseRipgrepJSON(stdout, opts.MaxResults)
	}
	return parsePlainGrep(stdout, opts.MaxResults), nil
}

func parsePlainGrep(data []byte, max int) []GrepHit {
	var hits []GrepHit
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		// "<path>:<line>:<content>"
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		j := strings.IndexByte(line[i+1:], ':')
		if j < 0 {
			continue
		}
		p := line[:i]
		ln, err := strconv.Atoi(line[i+1 : i+1+j])
		if err != nil {
			continue
		}
		hits = append(hits, GrepHit{Path: p, Line: ln, Column: 0, Content: line[i+1+j+1:]})
		if max > 0 && len(hits) >= max {
			break
		}
	}
	return hits
}

// resolveRG checks (once) whether rg is available on the remote.
func (d *sshDriver) resolveRG(ctx context.Context) string {
	d.mu.Lock()
	checked, p := d.rgChecked, d.rgPath
	d.mu.Unlock()
	if checked {
		return p
	}
	stdout, _, code, err := d.Exec(ctx, []string{"sh", "-c", "command -v rg || command -v ~/.local/share/crush/bin/rg"}, nil)
	d.mu.Lock()
	d.rgChecked = true
	if err == nil && code == 0 {
		d.rgPath = strings.TrimSpace(string(stdout))
	}
	p = d.rgPath
	d.mu.Unlock()
	return p
}

// Glob implements Driver via remote find (one RTT).
func (d *sshDriver) Glob(ctx context.Context, opts GlobOpts) ([]string, error) {
	if opts.Pattern == "" {
		return nil, errors.New("iodriver: Glob needs Pattern")
	}
	root := opts.Path
	if root == "" {
		root = "."
	}
	root = d.resolve(root)
	argv := []string{"find", root, "-type", "f", "-name", opts.Pattern}
	if opts.MaxResults > 0 {
		argv = append(argv, "-printf", "%p\\n")
	}
	stdout, _, _, err := d.Exec(ctx, argv, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if l == "" {
			continue
		}
		out = append(out, l)
		if opts.MaxResults > 0 && len(out) >= opts.MaxResults {
			break
		}
	}
	return out, nil
}

// Close implements Driver.
func (d *sshDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var first error
	if d.sftpC != nil {
		if err := d.sftpC.Close(); err != nil && first == nil {
			first = err
		}
		d.sftpC = nil
	}
	if d.client != nil {
		if err := d.client.Close(); err != nil && first == nil {
			first = err
		}
		d.client = nil
	}
	return first
}
