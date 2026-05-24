package iodriver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in       string
		fallback string
		wantKind Kind
		wantCwd  string
		wantErr  bool
	}{
		{"", "/home/u", KindLocal, "/home/u", false},
		{"local", "/home/u", KindLocal, "/home/u", false},
		{"local:/abs/path", "/home/u", KindLocal, "/abs/path", false},
		{"local:relative", "/home/u", KindLocal, "relative", false},
		{"ssh://root@host", "/", KindSSH, ".", false},
		{"ssh://root@host:2222/srv/app", "/", KindSSH, "/srv/app", false},
		{"ssh://host/no/user", "/", "", "", true},
		{"http://nope", "/", "", "", true},
	}
	for _, tc := range tests {
		k, _, cwd, err := ParseURI(tc.in, tc.fallback)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseURI(%q) want error, got kind=%q cwd=%q", tc.in, k, cwd)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseURI(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if k != tc.wantKind || cwd != tc.wantCwd {
			t.Errorf("ParseURI(%q) = (%q,%q), want (%q,%q)", tc.in, k, cwd, tc.wantKind, tc.wantCwd)
		}
	}
}

func TestLocalDriver_FileRoundTrip(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	d := NewLocalDriver(tmp)
	if d.Kind() != KindLocal {
		t.Fatalf("Kind want local got %s", d.Kind())
	}

	ctx := context.Background()
	rel := "sub/dir/hello.txt"
	content := []byte("hello driver\n")
	if err := d.WriteFile(ctx, rel, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := d.ReadFile(ctx, rel)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: %q vs %q", got, content)
	}

	// Stat resolves relative path under cwd.
	if _, err := d.Stat(ctx, rel); err != nil {
		t.Fatalf("Stat rel: %v", err)
	}
	// Absolute path also works.
	if _, err := d.Stat(ctx, filepath.Join(tmp, rel)); err != nil {
		t.Fatalf("Stat abs: %v", err)
	}

	// Remove + verify gone.
	if err := d.Remove(ctx, rel); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := d.Stat(ctx, rel); !os.IsNotExist(err) {
		t.Fatalf("Stat after remove should be NotExist, got %v", err)
	}
}

func TestLocalDriver_Exec(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	d := NewLocalDriver(tmp)
	ctx := context.Background()

	out, errOut, code, err := d.Exec(ctx, []string{"sh", "-c", "echo hi && pwd"}, nil)
	if err != nil {
		t.Fatalf("Exec err: %v stderr=%s", err, errOut)
	}
	if code != 0 {
		t.Fatalf("exit code want 0 got %d stderr=%s", code, errOut)
	}
	if !strings.Contains(string(out), "hi") {
		t.Fatalf("stdout missing 'hi': %q", out)
	}
	// pwd should reflect driver's cwd.
	if !strings.Contains(string(out), tmp) {
		t.Fatalf("pwd output %q does not contain cwd %q", out, tmp)
	}

	// Non-zero exit surfaces as code, not err.
	_, _, code, err = d.Exec(ctx, []string{"sh", "-c", "exit 7"}, nil)
	if err != nil {
		t.Fatalf("Exec exit 7 returned err=%v want nil", err)
	}
	if code != 7 {
		t.Fatalf("exit code want 7 got %d", code)
	}
}

func TestLocalDriver_GrepWalk(t *testing.T) {
	// Force walk path by zeroing rg.
	tmp := t.TempDir()
	d := NewLocalDriver(tmp)
	d.rgPath = "" // force fallback walk
	ctx := context.Background()

	for i, line := range []string{"alpha\n", "beta needle\n", "gamma\n"} {
		if err := d.WriteFile(ctx, filepath.Base(t.Name())+"-"+string(rune('a'+i))+".txt", []byte(line), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	hits, err := d.Grep(ctx, GrepOpts{Pattern: "needle"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d: %+v", len(hits), hits)
	}
	if hits[0].Line != 1 || !strings.Contains(hits[0].Content, "needle") {
		t.Fatalf("hit unexpected: %+v", hits[0])
	}
}

func TestContext_FromContextOrLocal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// No driver in ctx → falls back to LocalDriver pinned to fallback.
	d := FromContextOrLocal(ctx, "/tmp/x")
	if d == nil {
		t.Fatal("FromContextOrLocal nil")
	}
	if d.Kind() != KindLocal || d.WorkingDir(ctx) != "/tmp/x" {
		t.Fatalf("fallback driver wrong: kind=%s cwd=%s", d.Kind(), d.WorkingDir(ctx))
	}

	// Driver in ctx takes precedence.
	pinned := NewLocalDriver("/tmp/pinned")
	ctx2 := WithDriver(ctx, pinned)
	d2 := FromContextOrLocal(ctx2, "/tmp/x")
	if d2 != pinned {
		t.Fatalf("expected pinned driver, got %v", d2)
	}
}

func TestFactory_LocalAndSSHStub(t *testing.T) {
	t.Parallel()
	f := NewFactory("/tmp/fb")
	ctx := context.Background()

	d, err := f.Get(ctx, "")
	if err != nil || d.Kind() != KindLocal {
		t.Fatalf("local default: %v %v", err, d)
	}
	if _, err := f.Get(ctx, "ssh://root@somewhere"); err == nil {
		t.Fatal("ssh stub should error before M3")
	}
}
