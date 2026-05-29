package iodriver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
)

// Serve runs the daemon side of the IO protocol: it decodes requests from r,
// executes them against the local filesystem (the daemon runs ON the target
// host, so "local" there means the remote machine), and encodes responses to w.
// It returns when r reaches EOF or a transport error occurs. This is what
// `crush __remote-serve` invokes over stdin/stdout.
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
	default:
		resp.ErrKind = errKindOther
		resp.ErrMsg = "iodriver: unknown method " + string(req.Method)
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
