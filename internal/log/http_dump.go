package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HTTPDumpDirEnv is the environment variable that, when set to a writable
// directory, makes every outbound provider HTTP call mirror its request +
// response body into "<dir>/<providerID>.jsonl". This is independent of the
// --debug slog plumbing so crush-dev can capture raw traffic for all
// providers (not just one) regardless of log level.
const HTTPDumpDirEnv = "CRUSH_HTTP_DUMP_DIR"

const (
	providerHTTPIdleConnTimeout     = 45 * time.Second
	providerHTTPMaxIdleConns        = 128
	providerHTTPMaxIdleConnsPerHost = 16
)

var (
	providerHTTPTransportOnce sync.Once
	providerHTTPTransport     *http.Transport
)

// DumpHTTPClient returns an *http.Client that mirrors every request +
// response pair into the file at path (line-delimited JSON).
//
// Sensitive headers (Authorization, api-key, token, secret) are
// redacted on the way to disk.
func DumpHTTPClient(path string) *http.Client {
	return &http.Client{
		Transport: &dumpTransport{
			path: path,
			next: http.DefaultTransport,
		},
	}
}

// DumpRoundTripper wraps next so every request/response pair is mirrored to the
// file at path, then delegates to next. Use this to layer dumping over an
// existing custom transport (e.g. the IPC unix-socket transport) rather than
// the default one. Infinite event-stream responses are recorded by headers
// only — their bodies are passed through untouched so streaming is not broken.
func DumpRoundTripper(next http.RoundTripper, path string) http.RoundTripper {
	return &dumpTransport{path: path, next: next}
}

// NewProviderHTTPClient builds the http.Client used for a provider's outbound
// traffic, composing two optional layers over a shared provider transport:
//
//   - file dump  — enabled when CRUSH_HTTP_DUMP_DIR is set; writes raw req/resp
//     bodies to "<dir>/<providerID>.jsonl". Independent of debug.
//   - slog logger — enabled when debug is true; emits HTTP Request/Response at
//     Debug level via [HTTPRoundTripLogger].
//
// The dump layer wraps the slog layer so a single call produces both a disk
// record and a log line. The base transport is always shared across provider
// clients so SDK instances reuse TCP/TLS connections instead of each request
// constructing an isolated pool.
func NewProviderHTTPClient(providerID string, debug bool) *http.Client {
	var rt http.RoundTripper = providerHTTPRoundTripper()
	if debug {
		rt = &HTTPRoundTripLogger{Transport: rt}
	}
	if dir := os.Getenv(HTTPDumpDirEnv); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			path := filepath.Join(dir, providerID+".jsonl")
			rt = &dumpTransport{path: path, next: rt}
		}
	}
	return &http.Client{Transport: rt}
}

// CloseProviderIdleConnections drops idle keepalive sockets from the shared
// provider pool. Active SSE streams are not closed. Call this after network
// failures where a local proxy/TUN stack may have left stale pooled sockets.
func CloseProviderIdleConnections() {
	providerHTTPRoundTripper().CloseIdleConnections()
}

func providerHTTPRoundTripper() *http.Transport {
	providerHTTPTransportOnce.Do(func() {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.IdleConnTimeout = providerHTTPIdleConnTimeout
		base.MaxIdleConns = providerHTTPMaxIdleConns
		base.MaxIdleConnsPerHost = providerHTTPMaxIdleConnsPerHost
		providerHTTPTransport = base
	})
	return providerHTTPTransport
}

type dumpTransport struct {
	path string
	next http.RoundTripper
	mu   sync.Mutex
}

type dumpEntry struct {
	Time         string              `json:"time"`
	TraceID      string              `json:"trace_id,omitempty"`
	SessionID    string              `json:"session_id,omitempty"`
	Method       string              `json:"method"`
	URL          string              `json:"url"`
	ReqHeaders   map[string][]string `json:"req_headers"`
	ReqBody      string              `json:"req_body"`
	Status       int                 `json:"status,omitempty"`
	RespHeaders  map[string][]string `json:"resp_headers,omitempty"`
	RespBody     string              `json:"resp_body,omitempty"`
	DurationMs   int64               `json:"duration_ms"`
	TransportErr string              `json:"transport_err,omitempty"`
}

func (d *dumpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := dumpEntry{
		Time:       time.Now().UTC().Format(time.RFC3339Nano),
		TraceID:    TraceIDFromContext(req.Context()),
		SessionID:  SessionIDFromContext(req.Context()),
		Method:     req.Method,
		URL:        req.URL.String(),
		ReqHeaders: formatHeaders(req.Header),
	}

	if req.Body != nil {
		var savedReq io.ReadCloser
		var err error
		savedReq, req.Body, err = drainBody(req.Body)
		if err == nil && savedReq != nil {
			b, _ := io.ReadAll(savedReq)
			entry.ReqBody = string(b)
		}
	}

	start := time.Now()
	resp, err := d.next.RoundTrip(req)
	entry.DurationMs = time.Since(start).Milliseconds()

	if err != nil {
		entry.TransportErr = err.Error()
		d.write(entry)
		return resp, err
	}

	entry.Status = resp.StatusCode
	entry.RespHeaders = formatHeaders(resp.Header)

	// Event-stream responses (e.g. provider streamGenerateContent, the IPC
	// /events subscription) must not be drained up front: a fully-blocking
	// io.ReadAll would hang on an unbounded stream and break live delivery.
	// Instead tee the body incrementally into a capped buffer and write the
	// dump entry when the consumer closes it. Finite provider streams are
	// captured in full (up to the cap); infinite streams stay bounded and
	// flush whatever was seen on disconnect.
	if isEventStreamResponse(resp) {
		if resp.Body != nil {
			resp.Body = &streamCaptureBody{
				rc:  resp.Body,
				cap: maxStreamCaptureBytes,
				onClose: func(captured []byte, truncated bool) {
					entry.RespBody = string(captured)
					if truncated {
						entry.RespBody += "\n[stream truncated at capture cap]"
					}
					d.write(entry)
				},
			}
		} else {
			d.write(entry)
		}
		return resp, nil
	}

	if resp.Body != nil {
		var savedResp io.ReadCloser
		savedResp, resp.Body, _ = drainBody(resp.Body)
		// drainBody returns nil savedResp on ReadFrom/Close error (e.g.
		// another transport already drained the body before we got here —
		// see Anthropic CCH cch.go:101 → dumpTransport composition).
		// io.ReadAll(nil) panics, which would take down the whole TUI.
		if savedResp != nil {
			b, _ := io.ReadAll(savedResp)
			entry.RespBody = string(b)
		}
	}
	d.write(entry)
	return resp, nil
}

// maxStreamCaptureBytes bounds how much of a streaming response body is mirrored
// to disk, so an unbounded event stream cannot exhaust memory.
const maxStreamCaptureBytes = 4 << 20 // 4 MiB

// streamCaptureBody mirrors bytes read by the consumer into a capped buffer,
// preserving live streaming (it passes every Read straight through). The dump
// entry is flushed exactly once, on whichever happens first: the stream
// reaching io.EOF (finite provider responses), a read error, or Close (e.g. an
// unbounded /events subscription ending on client disconnect). Flushing on EOF
// rather than only on Close is what makes capture survive one-shot runs where
// the consumer reads to completion but never explicitly closes the body.
type streamCaptureBody struct {
	rc        io.ReadCloser
	buf       bytes.Buffer
	cap       int
	truncated bool
	flushed   bool
	onClose   func(captured []byte, truncated bool)
}

func (s *streamCaptureBody) Read(p []byte) (int, error) {
	n, err := s.rc.Read(p)
	if n > 0 && s.buf.Len() < s.cap {
		if rem := s.cap - s.buf.Len(); n <= rem {
			s.buf.Write(p[:n])
		} else {
			s.buf.Write(p[:rem])
			s.truncated = true
		}
	}
	if err != nil {
		s.flush()
	}
	return n, err
}

func (s *streamCaptureBody) Close() error {
	s.flush()
	return s.rc.Close()
}

func (s *streamCaptureBody) flush() {
	if s.flushed {
		return
	}
	s.flushed = true
	if s.onClose != nil {
		s.onClose(s.buf.Bytes(), s.truncated)
	}
}

func (d *dumpTransport) write(e dumpEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	// Pretty-print the body for readability when it's JSON. Best-effort.
	var buf bytes.Buffer
	if e.ReqBody != "" {
		if json.Indent(&buf, bytes.TrimSpace([]byte(e.ReqBody)), "", "  ") == nil {
			e.ReqBody = buf.String()
		}
	}
	buf.Reset()
	if e.RespBody != "" {
		if json.Indent(&buf, bytes.TrimSpace([]byte(e.RespBody)), "", "  ") == nil {
			e.RespBody = buf.String()
		}
	}
	enc, _ := json.Marshal(e)
	fmt.Fprintln(f, string(enc))
}
