package log

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProviderHTTPClientUsesSharedPooledTransport(t *testing.T) {
	t.Setenv(HTTPDumpDirEnv, "")

	firstClient := NewProviderHTTPClient("anthropic", false)
	secondClient := NewProviderHTTPClient("openai", false)
	if firstClient == nil || secondClient == nil {
		t.Fatalf("provider HTTP clients must always be explicit")
	}

	firstTransport, ok := firstClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected direct transport, got %T", firstClient.Transport)
	}
	secondTransport, ok := secondClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected direct transport, got %T", secondClient.Transport)
	}
	if firstTransport != secondTransport {
		t.Fatalf("expected provider HTTP clients to share one transport")
	}
	if firstTransport.IdleConnTimeout != providerHTTPIdleConnTimeout {
		t.Fatalf("unexpected idle timeout: %s", firstTransport.IdleConnTimeout)
	}
	if firstTransport.MaxIdleConns != providerHTTPMaxIdleConns {
		t.Fatalf("unexpected max idle conns: %d", firstTransport.MaxIdleConns)
	}
	if firstTransport.MaxIdleConnsPerHost != providerHTTPMaxIdleConnsPerHost {
		t.Fatalf("unexpected max idle conns per host: %d", firstTransport.MaxIdleConnsPerHost)
	}
}

func TestNewProviderHTTPClientWrapsSharedTransport(t *testing.T) {
	t.Setenv(HTTPDumpDirEnv, t.TempDir())

	client := NewProviderHTTPClient("openai", true)
	dumpLayer, ok := client.Transport.(*dumpTransport)
	if !ok {
		t.Fatalf("expected dump transport, got %T", client.Transport)
	}
	logLayer, ok := dumpLayer.next.(*HTTPRoundTripLogger)
	if !ok {
		t.Fatalf("expected logger transport, got %T", dumpLayer.next)
	}
	if logLayer.Transport != providerHTTPRoundTripper() {
		t.Fatalf("expected wrappers to delegate to shared provider transport")
	}
}

func TestHTTPRoundTripLoggerDoesNotDrainEventStream(t *testing.T) {
	logger := &HTTPRoundTripLogger{Transport: eventStreamRoundTripper{}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := logger.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp == nil {
		t.Fatalf("RoundTrip returned nil response")
	}
}

type eventStreamRoundTripper struct{}

func (eventStreamRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       readFailBody{},
		Request:    req,
	}, nil
}

type readFailBody struct{}

func (readFailBody) Read(p []byte) (int, error) {
	return 0, errors.New("event stream body was drained")
}

func (readFailBody) Close() error { return nil }

// brokenCloser implements io.ReadCloser whose Close always returns an
// error. This forces drainBody down the (nil, b, err) return path that
// previously crashed http_dump.go on `io.ReadAll(nil)`.
type brokenCloser struct{ io.Reader }

func (brokenCloser) Close() error { return errors.New("simulated close failure") }

// fakeRoundTripper returns a canned response whose Body's Close fails so
// drainBody short-circuits. Pre-fix this caused the dumpTransport to
// panic inside the TUI's bubbletea goroutine, taking the whole process
// down. Post-fix the nil savedResp branch is skipped gracefully.
type fakeRoundTripper struct{}

func (fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       brokenCloser{Reader: strings.NewReader("ok")},
		Request:    req,
	}, nil
}

func TestDumpTransport_NilSavedRespDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	dt := &dumpTransport{
		path: filepath.Join(dir, "test.jsonl"),
		next: fakeRoundTripper{},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp == nil {
		t.Fatalf("RoundTrip returned nil response")
	}
	// Ensure the entry was written even when body capture was skipped.
	data, err := os.ReadFile(filepath.Join(dir, "test.jsonl"))
	if err != nil {
		t.Fatalf("read dump file: %v", err)
	}
	if !strings.Contains(string(data), `"status":200`) {
		t.Errorf("expected status 200 in dump, got: %s", data)
	}
}

// brokenRequestBody simulates a request body whose Close fails — same
// drainBody nil-return path on the request side.
type brokenRequestBody struct{ io.Reader }

func (brokenRequestBody) Close() error { return errors.New("close failed") }

func TestDumpTransport_NilSavedReqDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	dt := &dumpTransport{
		path: filepath.Join(dir, "test.jsonl"),
		next: rrCapture{
			respBody: "ok",
		},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.invalid/", brokenRequestBody{Reader: strings.NewReader(`{}`)})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
}

// rrCapture returns a vanilla canned response so the request-side test
// isolates only the request-body drain path.
type rrCapture struct{ respBody string }

func (r rrCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(r.respBody)),
		Request:    req,
	}, nil
}

// existing test below ---------------------------------------------------------

func TestHTTPRoundTripLogger(t *testing.T) {
	// Create a test server that returns a 500 error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Internal server error", "code": 500}`))
	}))
	defer server.Close()

	// Create HTTP client with logging
	client := NewHTTPClient()

	// Make a request
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		server.URL,
		strings.NewReader(`{"test": "data"}`),
	)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", resp.StatusCode)
	}
}

func TestRedactedHeaderFormat(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret-token")
	headers.Set("X-API-Key", "secret-api-key")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "test-agent")
	headers.Set("X-Auth-Token", "secret-auth-token")

	formatted := formatHeaders(headers)

	// Check that sensitive headers are redacted
	if formatted["Authorization"][0] != "[REDACTED]" {
		t.Error("Authorization header should be redacted")
	}
	if formatted["X-Api-Key"][0] != "[REDACTED]" {
		t.Error("X-Api-Key header should be redacted")
	}
	if formatted["X-Auth-Token"][0] != "[REDACTED]" {
		t.Error("X-Auth-Token header should be redacted")
	}

	// Check that non-sensitive headers are preserved
	if formatted["Content-Type"][0] != "application/json" {
		t.Error("Content-Type header should be preserved")
	}
	if formatted["User-Agent"][0] != "test-agent" {
		t.Error("User-Agent header should be preserved")
	}
}
