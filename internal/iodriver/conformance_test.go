package iodriver

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newPipedRemote wires a RemoteBackend to an in-process Serve loop over two
// pipes, so the protocol + client + server are exercised end-to-end without a
// subprocess or SSH. This is the same transport shape the real daemon uses
// (Serve over stdin/stdout), just with pipes instead of an OS process boundary.
func newPipedRemote(t *testing.T, root string) *RemoteBackend {
	t.Helper()
	cr, sw := io.Pipe() // server writes -> client reads
	sr, cw := io.Pipe() // client writes -> server reads
	go func() {
		_ = Serve(context.Background(), sr, sw)
	}()
	rb := NewRemoteBackend(cr, cw, nil, "test", root)
	t.Cleanup(func() {
		_ = cw.Close()
		_ = sw.Close()
	})
	return rb
}

// TestRemoteMatchesLocal asserts the RemoteBackend produces the same observable
// results as the LocalBackend for every file operation, against the same temp
// tree. This is the Stage 1 conformance gate: if remote == local here, edit/
// view/write behave identically whether attached or not.
func TestRemoteMatchesLocal(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		make func(t *testing.T) (FileSystem, string) // returns backend + its temp root
	}{
		{"local", func(t *testing.T) (FileSystem, string) {
			d := t.TempDir()
			return NewLocalBackend(d), d
		}},
		{"remote", func(t *testing.T) (FileSystem, string) {
			d := t.TempDir()
			return newPipedRemote(t, d), d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsb, root := tc.make(t)

			// Raw-byte fidelity: bytes with CRLF, trailing spaces, NUL, unicode
			// must round-trip exactly (edit's whitespace matching depends on it).
			raw := []byte("line1\r\n  indented  \t\nNUL\x00here\nüni\n")
			fpath := filepath.Join(root, "sub", "f.txt")
			if err := fsb.Mkdir(ctx, filepath.Dir(fpath), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := fsb.WriteFile(ctx, fpath, raw, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := fsb.ReadFile(ctx, fpath)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, raw) {
				t.Fatalf("byte mismatch:\n got %q\nwant %q", got, raw)
			}

			// Stat
			fi, err := fsb.Stat(ctx, fpath)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if fi.Size() != int64(len(raw)) {
				t.Fatalf("stat size = %d, want %d", fi.Size(), len(raw))
			}
			if fi.IsDir() {
				t.Fatalf("stat IsDir = true for a file")
			}

			// os.IsNotExist fidelity across the wire.
			_, err = fsb.Stat(ctx, filepath.Join(root, "nope"))
			if !os.IsNotExist(err) {
				t.Fatalf("missing-file stat: os.IsNotExist = false, err = %v", err)
			}
			_, err = fsb.ReadFile(ctx, filepath.Join(root, "nope"))
			if !os.IsNotExist(err) {
				t.Fatalf("missing-file read: os.IsNotExist = false, err = %v", err)
			}

			// ReadDir
			ents, err := fsb.ReadDir(ctx, filepath.Dir(fpath))
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			if len(ents) != 1 || ents[0].Name() != "f.txt" || ents[0].IsDir() {
				t.Fatalf("readdir = %+v, want single file f.txt", ents)
			}

			// Rename + Remove
			npath := filepath.Join(root, "sub", "g.txt")
			if err := fsb.Rename(ctx, fpath, npath); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if _, err := fsb.Stat(ctx, fpath); !os.IsNotExist(err) {
				t.Fatalf("post-rename old path should be gone, err = %v", err)
			}
			if err := fsb.Remove(ctx, npath); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := fsb.Stat(ctx, npath); !os.IsNotExist(err) {
				t.Fatalf("post-remove path should be gone, err = %v", err)
			}
		})
	}
}

// TestRemoteConcurrent ensures the request/response serialization holds under
// concurrent callers (tools may issue parallel file ops).
func TestRemoteConcurrent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	rb := newPipedRemote(t, root)

	const n = 20
	errc := make(chan error, n)
	for i := range n {
		go func(i int) {
			p := filepath.Join(root, "f"+string(rune('a'+i)))
			data := bytes.Repeat([]byte{byte(i)}, 100+i)
			if err := rb.WriteFile(ctx, p, data, 0o644); err != nil {
				errc <- err
				return
			}
			got, err := rb.ReadFile(ctx, p)
			if err != nil {
				errc <- err
				return
			}
			if !bytes.Equal(got, data) {
				errc <- io.ErrUnexpectedEOF
				return
			}
			errc <- nil
		}(i)
	}
	deadline := time.After(10 * time.Second)
	for range n {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("concurrent op: %v", err)
			}
		case <-deadline:
			t.Fatal("concurrent ops timed out")
		}
	}
}
