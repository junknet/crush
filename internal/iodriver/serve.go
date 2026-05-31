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
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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
	case methodJobStart:
		return handleJobStart(req, resp)
	case methodJobOutput:
		return handleJobOutput(req, resp)
	case methodJobKill:
		return handleJobKill(req, resp)
	default:
		resp.ErrKind = errKindOther
		resp.ErrMsg = "iodriver: unknown method " + string(req.Method)
	}
	return resp
}

type remoteJob struct {
	id          string
	command     string
	description string
	cwd         string
	cmd         *exec.Cmd
	stdout      bytes.Buffer
	stderr      bytes.Buffer
	mu          sync.RWMutex
	done        chan struct{}
	exitCode    int
	errMsg      string
	completedAt atomic.Int64
}

var remoteJobs sync.Map
var remoteJobCounter atomic.Uint64

func handleJobStart(req rpcRequest, resp rpcResponse) rpcResponse {
	cmd := buildExecCommand(req)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	id := strconv.FormatUint(remoteJobCounter.Add(1), 16)
	job := &remoteJob{
		id:          id,
		command:     req.Command,
		description: req.Description,
		cwd:         req.Cwd,
		cmd:         cmd,
		done:        make(chan struct{}),
		exitCode:    -1,
	}
	cmd.Stdout = lockedWriter{job: job, stderr: false}
	cmd.Stderr = lockedWriter{job: job, stderr: true}

	if err := cmd.Start(); err != nil {
		resp.ErrKind = errKindOther
		resp.ErrMsg = err.Error()
		return resp
	}
	remoteJobs.Store(id, job)

	go func() {
		err := cmd.Wait()
		job.mu.Lock()
		switch e := err.(type) {
		case nil:
			job.exitCode = 0
		case *exec.ExitError:
			job.exitCode = e.ExitCode()
		default:
			job.exitCode = -1
			job.errMsg = err.Error()
		}
		job.completedAt.Store(time.Now().Unix())
		job.mu.Unlock()
		close(job.done)
	}()

	return fillJobResponse(resp, job)
}

func handleJobOutput(req rpcRequest, resp rpcResponse) rpcResponse {
	job, ok := getRemoteJob(req.JobID)
	if !ok {
		resp.ErrKind = errKindOther
		resp.ErrMsg = "background job not found: " + req.JobID
		return resp
	}
	return fillJobResponse(resp, job)
}

func handleJobKill(req rpcRequest, resp rpcResponse) rpcResponse {
	job, ok := getRemoteJob(req.JobID)
	if !ok {
		resp.ErrKind = errKindOther
		resp.ErrMsg = "background job not found: " + req.JobID
		return resp
	}
	if job.cmd.Process != nil {
		_ = syscall.Kill(-job.cmd.Process.Pid, syscall.SIGTERM)
		_ = job.cmd.Process.Kill()
	}
	return fillJobResponse(resp, job)
}

func getRemoteJob(id string) (*remoteJob, bool) {
	value, ok := remoteJobs.Load(id)
	if !ok {
		return nil, false
	}
	job, ok := value.(*remoteJob)
	return job, ok
}

func fillJobResponse(resp rpcResponse, job *remoteJob) rpcResponse {
	job.mu.RLock()
	defer job.mu.RUnlock()
	resp.JobID = job.id
	resp.Command = job.command
	resp.Description = job.description
	resp.Cwd = job.cwd
	resp.Stdout = append([]byte(nil), job.stdout.Bytes()...)
	resp.Stderr = append([]byte(nil), job.stderr.Bytes()...)
	resp.ExitCode = job.exitCode
	resp.Done = job.completedAt.Load() != 0
	if job.errMsg != "" {
		resp.ErrKind = errKindOther
		resp.ErrMsg = job.errMsg
	}
	return resp
}

type lockedWriter struct {
	job    *remoteJob
	stderr bool
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.job.mu.Lock()
	defer w.job.mu.Unlock()
	if w.stderr {
		return w.job.stderr.Write(p)
	}
	return w.job.stdout.Write(p)
}

// handleExec runs one command to completion in a fresh shell on the daemon
// host, capturing stdout and stderr separately and reporting the exit code.
func handleExec(req rpcRequest, resp rpcResponse) rpcResponse {
	cmd := buildExecCommand(req)
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

func buildExecCommand(req rpcRequest) *exec.Cmd {
	if len(req.Argv) > 0 {
		// Direct exec, no shell: structured tools (grep/find) pass argv so a
		// pattern with shell metacharacters is never reinterpreted.
		return exec.Command(req.Argv[0], req.Argv[1:]...)
	}
	return exec.Command("/bin/sh", "-c", req.Command)
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
