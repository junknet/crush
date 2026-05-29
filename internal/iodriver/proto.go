package iodriver

import (
	"io/fs"
	"time"
)

// Wire protocol for the remote IO daemon. Messages are JSON values streamed
// over a transport (SSH stdio, a local subprocess pipe, or an in-process pipe
// in tests). Requests and responses are correlated by ID; the client issues one
// request at a time per connection (strict request/response), which is
// sufficient for the file face and keeps the daemon simple.
//
// File bytes ride as the JSON-native base64 encoding of []byte, so there is no
// manual base64 and — critically — no text/newline normalization, preserving
// exact bytes for edit's whitespace-sensitive matching.

// rpcMethod names the file-face operations. Exec-face methods are added when
// the remote exec backend lands.
type rpcMethod string

const (
	methodStat      rpcMethod = "stat"
	methodReadFile  rpcMethod = "read_file"
	methodWriteFile rpcMethod = "write_file"
	methodMkdir     rpcMethod = "mkdir"
	methodRemove    rpcMethod = "remove"
	methodRename    rpcMethod = "rename"
	methodReadDir   rpcMethod = "read_dir"
	methodExec      rpcMethod = "exec"
)

// errKind lets the client reconstruct the sentinel a caller checks for, since
// error identity does not survive the wire. "not_exist" maps back to a wrapper
// of fs.ErrNotExist so os.IsNotExist keeps working across the remote boundary.
type errKind string

const (
	errKindNone     errKind = ""
	errKindNotExist errKind = "not_exist"
	errKindExist    errKind = "exist"
	errKindOther    errKind = "other"
)

type rpcRequest struct {
	ID     uint64    `json:"id"`
	Method rpcMethod `json:"method"`
	Path   string    `json:"path,omitempty"`
	// NewPath is the destination for rename.
	NewPath string `json:"new_path,omitempty"`
	// Data is the payload for write_file (JSON-native base64).
	Data []byte `json:"data,omitempty"`
	// Mode is the perm bits for write_file / mkdir.
	Mode uint32 `json:"mode,omitempty"`
	// Command/Cwd/Env carry an exec request (run-to-completion).
	Command string   `json:"command,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

type rpcResponse struct {
	ID      uint64       `json:"id"`
	ErrKind errKind      `json:"err_kind,omitempty"`
	ErrMsg  string       `json:"err_msg,omitempty"`
	Data    []byte       `json:"data,omitempty"`    // read_file payload
	Info    *wireInfo    `json:"info,omitempty"`    // stat result
	Entries []wireDirEnt `json:"entries,omitempty"` // read_dir result
	// exec results
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// wireInfo is the serializable projection of fs.FileInfo.
type wireInfo struct {
	Name    string      `json:"name"`
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"mode"`
	ModUnix int64       `json:"mod_unix_nano"`
	IsDir   bool        `json:"is_dir"`
}

// wireDirEnt is the serializable projection of fs.DirEntry.
type wireDirEnt struct {
	Name  string      `json:"name"`
	IsDir bool        `json:"is_dir"`
	Type  fs.FileMode `json:"type"`
}

// staticFileInfo is an fs.FileInfo reconstructed from wireInfo on the client.
type staticFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (i staticFileInfo) Name() string       { return i.name }
func (i staticFileInfo) Size() int64        { return i.size }
func (i staticFileInfo) Mode() fs.FileMode  { return i.mode }
func (i staticFileInfo) ModTime() time.Time { return i.modTime }
func (i staticFileInfo) IsDir() bool        { return i.isDir }
func (i staticFileInfo) Sys() any           { return nil }

func (w *wireInfo) toFileInfo() staticFileInfo {
	return staticFileInfo{
		name:    w.Name,
		size:    w.Size,
		mode:    w.Mode,
		modTime: time.Unix(0, w.ModUnix),
		isDir:   w.IsDir,
	}
}

func infoToWire(fi fs.FileInfo) *wireInfo {
	return &wireInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModUnix: fi.ModTime().UnixNano(),
		IsDir:   fi.IsDir(),
	}
}

// staticDirEntry is an fs.DirEntry reconstructed from wireDirEnt on the client.
type staticDirEntry struct {
	name  string
	isDir bool
	typ   fs.FileMode
}

func (e staticDirEntry) Name() string               { return e.name }
func (e staticDirEntry) IsDir() bool                { return e.isDir }
func (e staticDirEntry) Type() fs.FileMode          { return e.typ }
func (e staticDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
