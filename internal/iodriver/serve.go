package iodriver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

// Serve runs the daemon side of the IO protocol: it decodes requests from r,
// executes them against the local filesystem (the daemon runs ON the target
// host, so "local" there means the remote machine), and encodes responses to w.
// It returns when r reaches EOF or a transport error occurs. This is what
// the crush-remote daemon runs over stdin/stdout.
func Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		resp := handleRequest(ctx, req)
		if err := enc.Encode(&resp); err != nil {
			return err
		}
	}
}

func handleRequest(_ context.Context, req rpcRequest) rpcResponse {
	resp := rpcResponse{ID: req.ID}
	switch req.Method {
	case methodStat:
		fi, err := os.Stat(req.Path)
		if err != nil {
			return withErr(resp, err)
		}
		resp.Info = infoToWire(fi)
	case methodReadFile:
		data, err := os.ReadFile(req.Path)
		if err != nil {
			return withErr(resp, err)
		}
		resp.Data = data
	case methodWriteFile:
		if err := os.WriteFile(req.Path, req.Data, fs.FileMode(req.Mode)); err != nil {
			return withErr(resp, err)
		}
	case methodMkdir:
		if err := os.MkdirAll(req.Path, fs.FileMode(req.Mode)); err != nil {
			return withErr(resp, err)
		}
	case methodRemove:
		if err := os.Remove(req.Path); err != nil {
			return withErr(resp, err)
		}
	case methodRename:
		if err := os.Rename(req.Path, req.NewPath); err != nil {
			return withErr(resp, err)
		}
	case methodReadDir:
		ents, err := os.ReadDir(req.Path)
		if err != nil {
			return withErr(resp, err)
		}
		resp.Entries = make([]wireDirEnt, 0, len(ents))
		for _, e := range ents {
			resp.Entries = append(resp.Entries, wireDirEnt{
				Name:  e.Name(),
				IsDir: e.IsDir(),
				Type:  e.Type(),
			})
		}
	case methodExec:
		return handleExec(req, resp)
	default:
		resp.ErrKind = errKindOther
		resp.ErrMsg = "iodriver: unknown method " + string(req.Method)
	}
	return resp
}

// handleExec runs one command to completion in a fresh shell on the daemon
// host, capturing stdout and stderr separately and reporting the exit code.
func handleExec(req rpcRequest, resp rpcResponse) rpcResponse {
	cmd := exec.Command("/bin/sh", "-c", req.Command)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Own process group so a future cancel/signal can take down the whole
	// subtree rather than leaking children on the remote host.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := cmd.Run()
	resp.Stdout = stdout.Bytes()
	resp.Stderr = stderr.Bytes()
	switch e := err.(type) {
	case nil:
		resp.ExitCode = 0
	case *exec.ExitError:
		resp.ExitCode = e.ExitCode()
	default:
		// Command could not start (not found, bad cwd, ...).
		resp.ExitCode = -1
		resp.ErrKind = errKindOther
		resp.ErrMsg = err.Error()
	}
	return resp
}

// withErr classifies an os error so the client can reconstruct the sentinel a
// caller checks (os.IsNotExist / os.IsExist).
func withErr(resp rpcResponse, err error) rpcResponse {
	resp.ErrMsg = err.Error()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		resp.ErrKind = errKindNotExist
	case errors.Is(err, fs.ErrExist):
		resp.ErrKind = errKindExist
	default:
		resp.ErrKind = errKindOther
	}
	return resp
}
