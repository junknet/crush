package scheduler

import (
	"time"

	"github.com/charmbracelet/crush/internal/provider"
)

// TaskKind classifies the kind of work a task performs.
type TaskKind string

const (
	TaskExplore   TaskKind = "explore"
	TaskEdit      TaskKind = "edit"
	TaskVerify    TaskKind = "verify"
	TaskSummarize TaskKind = "summarize"
	TaskResearch  TaskKind = "research"
	TaskProbe     TaskKind = "probe"
)

// TaskMode controls whether a task is read-only or can mutate the workspace.
type TaskMode string

const (
	TaskReadOnly   TaskMode = "read_only"
	TaskWrite      TaskMode = "write"
	TaskExternalIO TaskMode = "external_io"
)

// WorkerProfile selects which worker tier should execute the task.
type WorkerProfile string

const (
	ProfileBuildAgent  WorkerProfile = "build_agent"
	ProfileWorkerAgent WorkerProfile = "worker_agent"
	ProfileToolsAgent  WorkerProfile = "tools_agent"
)

// Ownership describes the path set a task is allowed to mutate.
type Ownership struct {
	Paths map[string]struct{}
}

// EvidenceRef is a compact reference to supporting evidence.
type EvidenceRef struct {
	ID      string
	Summary string
}

// TaskIntent is the task-layer payload that gets compacted as the scheduler
// walks down the DAG.
type TaskIntent struct {
	Goal            string
	Scope           []string
	Evidence        []EvidenceRef
	SuccessCriteria string
	BudgetMs        int
	BudgetTokens    int
	ParentID        string
}

// TaskNode is the scheduler node that represents one unit of work.
type TaskNode struct {
	ID                    string
	SessionID             string
	ConversationSessionID string
	Kind                  TaskKind
	Mode                  TaskMode
	Profile               WorkerProfile
	ProviderID            string
	ProviderType          string
	ModelID               string
	RequestID             string
	Ownership             Ownership
	Deps                  []*TaskNode
	Intent                TaskIntent
	Parent                *TaskNode
	Children              []*TaskNode
	MaxRetries            int
	RetryCount            int
	LastOutput            string
	ValidationCmd         string
	StartedAt             time.Time
	FinishedAt            time.Time
	DurationMs            int64
	InputBytes            int
	OutputBytes           int
	InputTokens           int64
	OutputTokens          int64
	TotalTokens           int64
	ReasoningTokens       int64
	CacheCreationTokens   int64
	CacheReadTokens       int64
	EstimatedCostUSD      float64
}

// InitOwnership creates an ownership set from a path list.
func InitOwnership(paths []string) Ownership {
	ownership := Ownership{Paths: make(map[string]struct{}, len(paths))}
	for _, path := range paths {
		if path == "" {
			continue
		}
		ownership.Paths[path] = struct{}{}
	}
	return ownership
}

// Overlaps returns true when two ownership sets share at least one path.
func (a Ownership) Overlaps(b Ownership) bool {
	if len(a.Paths) == 0 || len(b.Paths) == 0 {
		return false
	}
	for path := range a.Paths {
		if _, ok := b.Paths[path]; ok {
			return true
		}
	}
	return false
}

// InitTaskIntent constructs a task intent from a terse goal and scope.
func InitTaskIntent(goal string, scope []string, successCriteria string, budgetMs, budgetTokens int, parentID string) TaskIntent {
	return TaskIntent{
		Goal:            goal,
		Scope:           append([]string(nil), scope...),
		SuccessCriteria: successCriteria,
		BudgetMs:        budgetMs,
		BudgetTokens:    budgetTokens,
		ParentID:        parentID,
	}
}

// IsGroupNode returns true when the node delegates to child tasks.
func IsGroupNode(node *TaskNode) bool {
	return node != nil && len(node.Children) > 0
}

// AttachChild wires a parent/child relationship and keeps the parent's child
// list idempotent.
func AttachChild(parent, child *TaskNode) {
	if parent == nil || child == nil {
		return
	}
	child.Parent = parent
	if child.ConversationSessionID == "" {
		child.ConversationSessionID = parent.ConversationSessionID
	}
	for _, existing := range parent.Children {
		if existing == child {
			return
		}
	}
	parent.Children = append(parent.Children, child)
}

// ToRequestIntent converts a task into a provider-agnostic request intent.
func (t *TaskIntent) ToRequestIntent() provider.RequestIntent {
	if t == nil {
		return provider.RequestIntent{}
	}
	return provider.RequestIntent{
		Purpose:         provider.PurposeInspect,
		ThinkingBudget:  provider.BudgetFromTokens(t.BudgetTokens),
		AttentionPolicy: provider.AttentionPolicyInherit,
		MaxOutputTokens: t.BudgetTokens,
		ToolMode:        provider.ToolModeAuto,
		Tools:           append([]string(nil), t.Scope...),
	}
}

// ToRequestIntent converts a task node into a provider-facing request intent.
func (n *TaskNode) ToRequestIntent() provider.RequestIntent {
	if n == nil {
		return provider.RequestIntent{}
	}
	intent := n.Intent.ToRequestIntent()
	intent.Purpose = purposeForTaskKind(n.Kind)
	switch n.Mode {
	case TaskWrite:
		intent.AttentionPolicy = provider.AttentionPolicyEvictAfterTask
	case TaskReadOnly:
		intent.AttentionPolicy = provider.AttentionPolicyKeepSessionCompact
	case TaskExternalIO:
		intent.AttentionPolicy = provider.AttentionPolicyInherit
	}
	if len(n.Intent.Scope) > 0 {
		intent.Tools = append([]string(nil), n.Intent.Scope...)
	}
	return intent
}

func purposeForTaskKind(kind TaskKind) provider.RequestPurpose {
	switch kind {
	case TaskEdit:
		return provider.PurposeEdit
	case TaskVerify:
		return provider.PurposeVerify
	case TaskSummarize:
		return provider.PurposeSummarize
	case TaskResearch, TaskExplore, TaskProbe:
		return provider.PurposeInspect
	default:
		return provider.PurposeInspect
	}
}
