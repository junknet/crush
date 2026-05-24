package iodriver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// LocalDriver runs every operation directly against the host filesystem
// and the local sh/v3 interpreter. It is the default driver every
// session starts with; SSHDriver is opt-in via `set_workspace`.
type LocalDriver struct {
	cwd    string
	rgPath string // resolved at construction, "" if rg not available
}

// NewLocalDriver returns a driver pinned to workingDir. workingDir may
// be relative; it is resolved against the process cwd at construction.
func NewLocalDriver(workingDir string) *LocalDriver {
	if workingDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workingDir = wd
		} else {
			workingDir = "."
		}
	}
	abs, err := filepath.Abs(workingDir)
	if err == nil {
		workingDir = abs
	}
	rg, _ := exec.LookPath("rg")
	return &LocalDriver{cwd: workingDir, rgPath: rg}
}

// Kind implements Driver.
func (l *LocalDriver) Kind() Kind { return KindLocal }

// URI implements Driver.
func (l *LocalDriver) URI() string { return "local:" + l.cwd }

// WorkingDir implements Driver.
func (l *LocalDriver) WorkingDir(ctx context.Context) string { return l.cwd }

// resolve makes path absolute under cwd if it isn't already.
func (l *LocalDriver) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(l.cwd, path)
}

// Stat implements Driver.
func (l *LocalDriver) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(l.resolve(path))
}

// ReadFile implements Driver.
func (l *LocalDriver) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return os.ReadFile(l.resolve(path))
}

// WriteFile implements Driver.
func (l *LocalDriver) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	target := l.resolve(path)
	if dir := filepath.Dir(target); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(target, data, perm)
}

// Remove implements Driver.
func (l *LocalDriver) Remove(ctx context.Context, path string) error {
	return os.Remove(l.resolve(path))
}

// MkdirAll implements Driver.
func (l *LocalDriver) MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(l.resolve(path), perm)
}

// Walk implements Driver via filepath.WalkDir.
func (l *LocalDriver) Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(l.resolve(root), func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fn(p, d, err)
	})
}

// Exec implements Driver. argv[0] is the program; argv[1:] are its
// arguments. Use bash -c for shell pipelines.
func (l *LocalDriver) Exec(ctx context.Context, argv []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	if len(argv) == 0 {
		return nil, nil, -1, errors.New("iodriver: Exec needs at least argv[0]")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = l.cwd
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
	exitCode = 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
			// Non-zero exit isn't itself an "err" — return code is the
			// signal. Surface real spawn failures (binary not found, ctx
			// cancelled) as err.
			return stdout, stderr, exitCode, nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

// Grep implements Driver. Uses ripgrep when available with JSON output
// (one round-trip parse), otherwise falls back to a tree walk.
func (l *LocalDriver) Grep(ctx context.Context, opts GrepOpts) ([]GrepHit, error) {
	if opts.Pattern == "" {
		return nil, errors.New("iodriver: Grep needs Pattern")
	}
	root := opts.Path
	if root == "" {
		root = "."
	}
	root = l.resolve(root)

	if l.rgPath != "" {
		return l.grepRipgrep(ctx, root, opts)
	}
	return l.grepWalk(ctx, root, opts)
}

func (l *LocalDriver) grepRipgrep(ctx context.Context, root string, opts GrepOpts) ([]GrepHit, error) {
	args := []string{"--json", "--no-messages"}
	if opts.IgnoreCase {
		args = append(args, "-i")
	}
	if opts.Literal {
		args = append(args, "-F")
	}
	if opts.Include != "" {
		args = append(args, "-g", opts.Include)
	}
	if opts.MaxResults > 0 {
		args = append(args, "-m", strconv.Itoa(opts.MaxResults))
	}
	args = append(args, "--", opts.Pattern, root)
	cmd := exec.CommandContext(ctx, l.rgPath, args...)
	cmd.Dir = l.cwd
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// rg exits 1 on no-matches; treat as empty
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseRipgrepJSON(outBuf.Bytes(), opts.MaxResults)
}

func parseRipgrepJSON(data []byte, max int) ([]GrepHit, error) {
	var hits []GrepHit
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var raw struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := dec.Decode(&raw); err != nil {
			return hits, err
		}
		if raw.Type != "match" {
			continue
		}
		var m struct {
			Path  struct{ Text string } `json:"path"`
			Lines struct{ Text string } `json:"lines"`
			LineN int                   `json:"line_number"`
			Submatches []struct {
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"submatches"`
		}
		if err := json.Unmarshal(raw.Data, &m); err != nil {
			continue
		}
		col := 0
		if len(m.Submatches) > 0 {
			col = m.Submatches[0].Start + 1
		}
		hits = append(hits, GrepHit{
			Path:    m.Path.Text,
			Line:    m.LineN,
			Column:  col,
			Content: strings.TrimRight(m.Lines.Text, "\n"),
		})
		if max > 0 && len(hits) >= max {
			break
		}
	}
	return hits, nil
}

func (l *LocalDriver) grepWalk(ctx context.Context, root string, opts GrepOpts) ([]GrepHit, error) {
	var hits []GrepHit
	needle := opts.Pattern
	if opts.IgnoreCase {
		needle = strings.ToLower(needle)
	}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() {
			return nil
		}
		if opts.Include != "" {
			match, _ := filepath.Match(opts.Include, filepath.Base(p))
			if !match {
				return nil
			}
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			haystack := line
			if opts.IgnoreCase {
				haystack = strings.ToLower(haystack)
			}
			idx := strings.Index(haystack, needle)
			if idx < 0 {
				continue
			}
			hits = append(hits, GrepHit{
				Path: p, Line: ln, Column: idx + 1, Content: line,
			})
			if opts.MaxResults > 0 && len(hits) >= opts.MaxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return hits, walkErr
	}
	return hits, nil
}

// Glob implements Driver. Local impl uses filepath.Match against a walk.
func (l *LocalDriver) Glob(ctx context.Context, opts GlobOpts) ([]string, error) {
	if opts.Pattern == "" {
		return nil, errors.New("iodriver: Glob needs Pattern")
	}
	root := opts.Path
	if root == "" {
		root = "."
	}
	root = l.resolve(root)
	var matches []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		ok, _ := filepath.Match(opts.Pattern, filepath.Base(p))
		if !ok {
			// also try matching the relative path so callers can use
			// "subdir/*.go" style patterns.
			ok2, _ := filepath.Match(opts.Pattern, rel)
			if !ok2 {
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		matches = append(matches, p)
		if opts.MaxResults > 0 && len(matches) >= opts.MaxResults {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return matches, fmt.Errorf("iodriver: glob walk: %w", walkErr)
	}
	return matches, nil
}

// Close implements Driver. LocalDriver holds no resources.
func (l *LocalDriver) Close() error { return nil }
