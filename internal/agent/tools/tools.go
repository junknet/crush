package tools

import (
	"bytes"
	"context"
	"html/template"
	"os/exec"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/iodriver"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
)

type (
	sessionIDContextKey    string
	messageIDContextKey    string
	supportsImagesKey      string
	modelNameKey           string
	traceRuntimeContextKey string
	taskNodeIDContextKey   string
	taskParentIDContextKey string
	taskProfileContextKey  string
	providerIDContextKey   string
	providerTypeContextKey string
	modelIDContextKey      string
	backendContextKey      string
)

const (
	// SessionIDContextKey is the key for the session ID in the context.
	SessionIDContextKey sessionIDContextKey = "session_id"
	// MessageIDContextKey is the key for the message ID in the context.
	MessageIDContextKey messageIDContextKey = "message_id"
	// SupportsImagesContextKey is the key for the model's image support capability.
	SupportsImagesContextKey supportsImagesKey = "supports_images"
	// ModelNameContextKey is the key for the model name in the context.
	ModelNameContextKey modelNameKey = "model_name"
	// TraceRuntimeContextKey is the key for appending runtime trace entries.
	TraceRuntimeContextKey traceRuntimeContextKey = "trace_runtime"
	// TaskNodeIDContextKey is the key for the current scheduler node ID.
	TaskNodeIDContextKey taskNodeIDContextKey = "task_node_id"
	// TaskParentIDContextKey is the key for the current scheduler parent ID.
	TaskParentIDContextKey taskParentIDContextKey = "task_parent_id"
	// TaskProfileContextKey is the key for the current scheduler profile.
	TaskProfileContextKey taskProfileContextKey = "task_profile"
	// ProviderIDContextKey is the key for the configured provider ID.
	ProviderIDContextKey providerIDContextKey = "provider_id"
	// ProviderTypeContextKey is the key for the configured provider protocol.
	ProviderTypeContextKey providerTypeContextKey = "provider_type"
	// ModelIDContextKey is the key for the configured model ID.
	ModelIDContextKey modelIDContextKey = "model_id"
	// BackendContextKey is the key for the active IO backend (local or remote).
	// When absent, file/exec helpers fall back to direct local os.* behavior.
	BackendContextKey backendContextKey = "io_backend"
)

// getContextValue is a generic helper that retrieves a typed value from context.
// If the value is not found or has the wrong type, it returns the default value.
func getContextValue[T any](ctx context.Context, key any, defaultValue T) T {
	value := ctx.Value(key)
	if value == nil {
		return defaultValue
	}
	if typedValue, ok := value.(T); ok {
		return typedValue
	}
	return defaultValue
}

// GetSessionFromContext retrieves the session ID from the context.
func GetSessionFromContext(ctx context.Context) string {
	return getContextValue(ctx, SessionIDContextKey, "")
}

// GetMessageFromContext retrieves the message ID from the context.
func GetMessageFromContext(ctx context.Context) string {
	return getContextValue(ctx, MessageIDContextKey, "")
}

// GetSupportsImagesFromContext retrieves whether the model supports images from the context.
func GetSupportsImagesFromContext(ctx context.Context) bool {
	return getContextValue(ctx, SupportsImagesContextKey, false)
}

// GetModelNameFromContext retrieves the model name from the context.
func GetModelNameFromContext(ctx context.Context) string {
	return getContextValue(ctx, ModelNameContextKey, "")
}

// WithBackendRegistry attaches the shared session→backend registry to the
// context. The registry REFERENCE is stable for the turn, but its CONTENTS are
// live: this is what lets remote_attach take effect on the very next tool call
// within the same turn. Injecting a resolved backend instead would snapshot
// "not attached" at turn start and silently run later tools locally even after
// an in-turn attach.
func WithBackendRegistry(ctx context.Context, registry *csync.Map[string, iodriver.Backend]) context.Context {
	return context.WithValue(ctx, BackendContextKey, registry)
}

// GetBackendFromContext resolves the active IO backend for the current session
// live from the registry, or nil when none is attached (the local-default
// path). It reads the registry per call (not a turn snapshot) so an attach/
// detach earlier in the same turn is honored by subsequent file/exec tools.
func GetBackendFromContext(ctx context.Context) iodriver.Backend {
	registry := getContextValue[*csync.Map[string, iodriver.Backend]](ctx, BackendContextKey, nil)
	if registry == nil {
		return nil
	}
	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return nil
	}
	backend, ok := registry.Get(sessionID)
	if !ok {
		return nil
	}
	return backend
}

// WithTraceContext attaches runtime trace metadata to tool calls.
func WithTraceContext(ctx context.Context, runtime *agentruntime.RuntimeSession, nodeID, parentID, profile, providerID, providerType, modelID string) context.Context {
	if runtime != nil {
		ctx = context.WithValue(ctx, TraceRuntimeContextKey, runtime)
	}
	ctx = context.WithValue(ctx, TaskNodeIDContextKey, nodeID)
	ctx = context.WithValue(ctx, TaskParentIDContextKey, parentID)
	ctx = context.WithValue(ctx, TaskProfileContextKey, profile)
	ctx = context.WithValue(ctx, ProviderIDContextKey, providerID)
	ctx = context.WithValue(ctx, ProviderTypeContextKey, providerType)
	ctx = context.WithValue(ctx, ModelIDContextKey, modelID)
	return ctx
}

// AppendTraceFromContext appends a trace entry using runtime metadata from the
// context. Missing values are left empty so tests can call tools directly.
func AppendTraceFromContext(ctx context.Context, entry agentruntime.TaskTrace) agentruntime.TaskTrace {
	runtime := getContextValue[*agentruntime.RuntimeSession](ctx, TraceRuntimeContextKey, nil)
	if runtime == nil {
		return entry
	}
	if entry.SessionID == "" {
		entry.SessionID = GetSessionFromContext(ctx)
	}
	if entry.NodeID == "" {
		entry.NodeID = getContextValue(ctx, TaskNodeIDContextKey, "")
	}
	if entry.ParentID == "" {
		entry.ParentID = getContextValue(ctx, TaskParentIDContextKey, "")
	}
	if entry.Profile == "" {
		entry.Profile = getContextValue(ctx, TaskProfileContextKey, "")
	}
	if entry.ProviderID == "" {
		entry.ProviderID = getContextValue(ctx, ProviderIDContextKey, "")
	}
	if entry.ProviderType == "" {
		entry.ProviderType = getContextValue(ctx, ProviderTypeContextKey, "")
	}
	if entry.ModelID == "" {
		entry.ModelID = getContextValue(ctx, ModelIDContextKey, "")
	}
	return runtime.AppendTrace(entry)
}

// NewPermissionDeniedResponse returns a tool response indicating the user
// denied permission, with StopTurn set so the agent loop does not retry.
func NewPermissionDeniedResponse() fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse("User denied permission")
	resp.StopTurn = true
	return resp
}

// ghAvailable indicates whether the `gh` CLI is available on PATH.
var ghAvailable = func() bool {
	if testing.Testing() {
		return false
	}
	_, err := exec.LookPath("gh")
	return err == nil
}()

// toolDescriptionData is the common data structure for tool description templates.
type toolDescriptionData struct {
	GhAvailable bool
}

// renderToolDescription renders a tool description template with the given data.
func renderToolDescription(tmpl *template.Template) string {
	data := toolDescriptionData{
		GhAvailable: ghAvailable,
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		panic("failed to execute tool description template: " + err.Error())
	}
	return out.String()
}

// renderTemplate renders a Go template with the given data.
func renderTemplate(tmpl *template.Template, data any) string {
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		panic("failed to execute tool description template: " + err.Error())
	}
	return out.String()
}
