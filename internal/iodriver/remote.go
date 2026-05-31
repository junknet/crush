package iodriver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
)

// RemoteBackend is the client face of the IO protocol. It satisfies FileSystem
// (and, in a later stage, the exec face) by issuing RPCs over a transport to a
// daemon (crush-remote) running on the target host. The transport is
// any reader/writer pair — an SSH stdio channel, a local subprocess pipe, or an
// in-process pipe in tests — so the same client works across all of them.
//
// File operations are strict request/response; a single mutex serializes the
// in-flight request, which is correct and simple for the file face.
type RemoteBackend struct {
	mu     sync.Mutex
	enc    *json.Encoder
	dec    *json.Decoder
	closer io.Closer
	nextID uint64
	root   string
	host   string
}

// NewRemoteBackend builds a client over the given transport. host labels the
// backend (e.g. an SSH alias); root is the remote default working dir. closer,
// if non-nil, is closed by Close to tear the transport down.
func NewRemoteBackend(r io.Reader, w io.Writer, closer io.Closer, host, root string) *RemoteBackend {
	return &RemoteBackend{
		enc:    json.NewEncoder(w),
		dec:    json.NewDecoder(r),
		closer: closer,
		root:   root,
		host:   host,
	}
}

func (b *RemoteBackend) Kind() string { return "remote:" + b.host }
func (b *RemoteBackend) Root() string { return b.root }

func (b *RemoteBackend) Close() error {
	if b.closer != nil {
		return b.closer.Close()
	}
	return nil
}

// call performs one synchronous request/response round-trip.
func (b *RemoteBackend) call(req rpcRequest) (rpcResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	req.ID = b.nextID
	if err := b.enc.Encode(&req); err != nil {
		return rpcResponse{}, fmt.Errorf("iodriver remote %s: send %s: %w", b.host, req.Method, err)
	}
	var resp rpcResponse
	if err := b.dec.Decode(&resp); err != nil {
		return rpcResponse{}, fmt.Errorf("iodriver remote %s: recv %s: %w", b.host, req.Method, err)
	}
	if resp.ID != req.ID {
		return rpcResponse{}, fmt.Errorf("iodriver remote %s: response id mismatch: want %d got %d", b.host, req.ID, resp.ID)
	}
	return resp, nil
}

// rpcErr reconstructs an error from a response, mapping err_kind back to the
// fs sentinels. For not-exist/exist it returns a real *fs.PathError wrapping the
// sentinel: os.IsNotExist / os.IsExist only unwrap PathError/LinkError/
// SyscallError (they predate errors.Is and do NOT follow %w chains), so a plain
// fmt.Errorf("%w", fs.ErrNotExist) would fail os.IsNotExist. A PathError both
// satisfies those checks and preserves op/path context.
func rpcErr(method rpcMethod, path string, resp rpcResponse) error {
	if resp.ErrKind == errKindNone && resp.ErrMsg == "" {
		return nil
	}
	switch resp.ErrKind {
	case errKindNotExist:
		return &fs.PathError{Op: string(method), Path: path, Err: fs.ErrNotExist}
	case errKindExist:
		return &fs.PathError{Op: string(method), Path: path, Err: fs.ErrExist}
	default:
		return fmt.Errorf("%s %s: %s", method, path, resp.ErrMsg)
	}
}

func (b *RemoteBackend) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	resp, err := b.call(rpcRequest{Method: methodStat, Path: path})
	if err != nil {
		return nil, err
	}
	if e := rpcErr(methodStat, path, resp); e != nil {
		return nil, e
	}
	if resp.Info == nil {
		return nil, fmt.Errorf("stat: empty info for %s", path)
	}
	return resp.Info.toFileInfo(), nil
}

func (b *RemoteBackend) ReadFile(_ context.Context, path string) ([]byte, error) {
	resp, err := b.call(rpcRequest{Method: methodReadFile, Path: path})
	if err != nil {
		return nil, err
	}
	if e := rpcErr(methodReadFile, path, resp); e != nil {
		return nil, e
	}
	// Normalize nil→empty so callers comparing len behave identically to os.
	if resp.Data == nil {
		return []byte{}, nil
	}
	return resp.Data, nil
}

func (b *RemoteBackend) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	resp, err := b.call(rpcRequest{Method: methodWriteFile, Path: path, Data: data, Mode: uint32(perm)})
	if err != nil {
		return err
	}
	return rpcErr(methodWriteFile, path, resp)
}

func (b *RemoteBackend) Mkdir(_ context.Context, path string, perm fs.FileMode) error {
	resp, err := b.call(rpcRequest{Method: methodMkdir, Path: path, Mode: uint32(perm)})
	if err != nil {
		return err
	}
	return rpcErr(methodMkdir, path, resp)
}

func (b *RemoteBackend) Remove(_ context.Context, path string) error {
	resp, err := b.call(rpcRequest{Method: methodRemove, Path: path})
	if err != nil {
		return err
	}
	return rpcErr(methodRemove, path, resp)
}

func (b *RemoteBackend) Rename(_ context.Context, oldPath, newPath string) error {
	resp, err := b.call(rpcRequest{Method: methodRename, Path: oldPath, NewPath: newPath})
	if err != nil {
		return err
	}
	return rpcErr(methodRename, oldPath, resp)
}

func (b *RemoteBackend) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	resp, err := b.call(rpcRequest{Method: methodReadDir, Path: path})
	if err != nil {
		return nil, err
	}
	if e := rpcErr(methodReadDir, path, resp); e != nil {
		return nil, e
	}
	out := make([]fs.DirEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, staticDirEntry{name: e.Name, isDir: e.IsDir, typ: e.Type})
	}
	return out, nil
}

// Exec runs a command to completion on the remote host. RemoteBackend is the
// only Execer; the bash/rg tools type-assert for it and fall back to the local
// shell when absent.
func (b *RemoteBackend) Exec(_ context.Context, req ExecRequest) (ExecResult, error) {
	resp, err := b.call(rpcRequest{Method: methodExec, Command: req.Command, Argv: req.Argv, Cwd: req.Cwd, Env: req.Env})
	if err != nil {
		return ExecResult{}, err
	}
	if resp.ErrKind != errKindNone {
		return ExecResult{}, fmt.Errorf("exec: %s", resp.ErrMsg)
	}
	return ExecResult{Stdout: resp.Stdout, Stderr: resp.Stderr, ExitCode: resp.ExitCode}, nil
}

const remoteJobIDPrefix = "remote:"

func RemoteJobID(raw string) string {
	if strings.HasPrefix(raw, remoteJobIDPrefix) {
		return raw
	}
	return remoteJobIDPrefix + raw
}

func IsRemoteJobID(id string) bool {
	return strings.HasPrefix(id, remoteJobIDPrefix)
}

func remoteRawJobID(id string) string {
	return strings.TrimPrefix(id, remoteJobIDPrefix)
}

func (b *RemoteBackend) StartJob(_ context.Context, req JobRequest) (JobSnapshot, error) {
	resp, err := b.call(rpcRequest{
		Method:      methodJobStart,
		Command:     req.Command,
		Argv:        req.Argv,
		Cwd:         req.Cwd,
		Env:         req.Env,
		Description: req.Description,
		SessionID:   req.SessionID,
	})
	if err != nil {
		return JobSnapshot{}, err
	}
	if resp.ErrKind != errKindNone {
		return JobSnapshot{}, fmt.Errorf("job_start: %s", resp.ErrMsg)
	}
	return jobSnapshotFromResponse(resp), nil
}

func (b *RemoteBackend) JobOutput(_ context.Context, id string) (JobSnapshot, error) {
	resp, err := b.call(rpcRequest{Method: methodJobOutput, JobID: remoteRawJobID(id)})
	if err != nil {
		return JobSnapshot{}, err
	}
	if resp.ErrKind != errKindNone {
		return JobSnapshot{}, fmt.Errorf("job_output: %s", resp.ErrMsg)
	}
	return jobSnapshotFromResponse(resp), nil
}

func (b *RemoteBackend) KillJob(_ context.Context, id string) error {
	resp, err := b.call(rpcRequest{Method: methodJobKill, JobID: remoteRawJobID(id)})
	if err != nil {
		return err
	}
	if resp.ErrKind != errKindNone {
		return fmt.Errorf("job_kill: %s", resp.ErrMsg)
	}
	return nil
}

func jobSnapshotFromResponse(resp rpcResponse) JobSnapshot {
	return JobSnapshot{
		ID:          RemoteJobID(resp.JobID),
		Command:     resp.Command,
		Description: resp.Description,
		Cwd:         resp.Cwd,
		Stdout:      resp.Stdout,
		Stderr:      resp.Stderr,
		Done:        resp.Done,
		ExitCode:    resp.ExitCode,
	}
}
