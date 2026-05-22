package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// maxDumpBodyBytes caps how much of a non-streaming response body is buffered
// for the IPC dump, so a large payload can't blow up server memory.
const maxDumpBodyBytes = 1 << 20 // 1 MiB

// ipcDumpWriter appends inbound IPC request/response pairs to a JSONL file. It
// is installed only when CRUSH_HTTP_DUMP_DIR is set, capturing the traffic
// between remote clients (e.g. the mobile app) and this server — the path the
// client-side dump cannot see because those clients are not the Go RPC client.
type ipcDumpWriter struct {
	path string
	mu   sync.Mutex
}

type ipcDumpEntry struct {
	Time       string              `json:"time"`
	RemoteAddr string              `json:"remote_addr"`
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	ReqHeaders map[string][]string `json:"req_headers"`
	ReqBody    string              `json:"req_body,omitempty"`
	Status     int                 `json:"status"`
	RespBody   string              `json:"resp_body,omitempty"`
	DurationMs int64               `json:"duration_ms"`
	Streamed   bool                `json:"streamed,omitempty"`
}

func (d *ipcDumpWriter) write(e ipcDumpEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var buf bytes.Buffer
	if e.ReqBody != "" && json.Indent(&buf, bytes.TrimSpace([]byte(e.ReqBody)), "", "  ") == nil {
		e.ReqBody = buf.String()
	}
	buf.Reset()
	if e.RespBody != "" && json.Indent(&buf, bytes.TrimSpace([]byte(e.RespBody)), "", "  ") == nil {
		e.RespBody = buf.String()
	}
	enc, _ := json.Marshal(e)
	fmt.Fprintln(f, string(enc))
}

func redactDumpHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "authorization") || strings.Contains(lk, "api-key") ||
			strings.Contains(lk, "token") || strings.Contains(lk, "secret") {
			out[k] = []string{"[REDACTED]"}
		} else {
			out[k] = v
		}
	}
	return out
}

// dumpResponseRecorder wraps an http.ResponseWriter to capture the status code
// and (bounded) response body. It implements Unwrap so http.ResponseController
// can still reach the underlying Flusher, and Flush so streaming endpoints keep
// working — once flushed it stops buffering so unbounded SSE bodies are safe.
type dumpResponseRecorder struct {
	http.ResponseWriter
	status      int
	buf         bytes.Buffer
	wroteHeader bool
	streaming   bool
}

func (r *dumpResponseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	if strings.Contains(r.Header().Get("Content-Type"), "text/event-stream") {
		r.streaming = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *dumpResponseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if !r.streaming && r.buf.Len() < maxDumpBodyBytes {
		r.buf.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

// Flush marks the response as streaming (stop buffering) and delegates to the
// underlying writer's flusher via the response controller.
func (r *dumpResponseRecorder) Flush() {
	r.streaming = true
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap exposes the underlying writer so http.ResponseController can locate
// optional interfaces (Flusher, Hijacker) the real writer implements.
func (r *dumpResponseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// ipcDumpHandler wraps next so every inbound request/response pair is mirrored
// to the dump file. Event-stream responses are recorded by metadata only.
func ipcDumpHandler(next http.Handler, path string) http.Handler {
	d := &ipcDumpWriter{path: path}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := ipcDumpEntry{
			Time:       time.Now().UTC().Format(time.RFC3339Nano),
			RemoteAddr: r.RemoteAddr,
			Method:     r.Method,
			URL:        r.URL.String(),
			ReqHeaders: redactDumpHeaders(r.Header),
		}
		if r.Body != nil {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxDumpBodyBytes))
			if err == nil {
				entry.ReqBody = string(b)
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), r.Body))
			}
		}

		rec := &dumpResponseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)

		entry.DurationMs = time.Since(start).Milliseconds()
		entry.Status = rec.status
		entry.Streamed = rec.streaming
		if rec.streaming {
			entry.RespBody = "[event-stream body not captured]"
		} else {
			entry.RespBody = rec.buf.String()
		}
		d.write(entry)
	})
}
