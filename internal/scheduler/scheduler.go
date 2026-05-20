package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/provider"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"golang.org/x/sync/errgroup"
)

// Worker executes leaf tasks.
type Worker interface {
	RunTask(ctx context.Context, node *TaskNode, intent provider.RequestIntent) (string, error)
}

// WorkerFunc adapts a function to the Worker interface.
type WorkerFunc func(ctx context.Context, node *TaskNode, intent provider.RequestIntent) (string, error)

// RunTask executes the function as a worker.
func (f WorkerFunc) RunTask(ctx context.Context, node *TaskNode, intent provider.RequestIntent) (string, error) {
	return f(ctx, node, intent)
}

// AgentScheduler owns the task DAG and publishes task lifecycle events.
type AgentScheduler struct {
	runtime *agentruntime.RuntimeSession

	mu    sync.RWMutex
	nodes map[string]*TaskNode
	roots map[string]*TaskNode
	seq   uint64
}

// NewAgentScheduler creates a scheduler for a runtime session.
func NewAgentScheduler(runtime *agentruntime.RuntimeSession) *AgentScheduler {
	return &AgentScheduler{
		runtime: runtime,
		nodes:   make(map[string]*TaskNode),
		roots:   make(map[string]*TaskNode),
	}
}

// Runtime returns the runtime session attached to the scheduler.
func (s *AgentScheduler) Runtime() *agentruntime.RuntimeSession {
	if s == nil {
		return nil
	}
	return s.runtime
}

// RegisterNode stores a node in the scheduler index.
func (s *AgentScheduler) RegisterNode(node *TaskNode) {
	if s == nil || node == nil || node.ID == "" {
		return
	}
	s.mu.Lock()
	s.nodes[node.ID] = node
	s.mu.Unlock()
	s.publishPlanned(node)
}

// Node returns a node by ID.
func (s *AgentScheduler) Node(id string) (*TaskNode, bool) {
	if s == nil || id == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[id]
	return node, ok
}

// Root returns the latest root node registered for a session.
func (s *AgentScheduler) Root(sessionID string) (*TaskNode, bool) {
	if s == nil || sessionID == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.roots[sessionID]
	return node, ok
}

// EnsureRoot creates or returns the root node for a session.
func (s *AgentScheduler) EnsureRoot(sessionID, goal string, scope []string, profile WorkerProfile) *TaskNode {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nodeID := fmt.Sprintf("%s-%d", sessionID, atomic.AddUint64(&s.seq, 1))
	node := &TaskNode{
		ID:                    nodeID,
		SessionID:             sessionID,
		ConversationSessionID: sessionID,
		Kind:                  TaskEdit,
		Mode:                  TaskWrite,
		Profile:               profile,
		Ownership:             InitOwnership(scope),
		Intent:                InitTaskIntent(goal, scope, "", 0, 0, ""),
	}
	s.roots[sessionID] = node
	s.nodes[node.ID] = node
	s.publishPlanned(node)
	return node
}

// BuildDefaultWorkflow expands a root task into a small ordered DAG.
func (s *AgentScheduler) BuildDefaultWorkflow(root *TaskNode) {
	if s == nil || root == nil || len(root.Children) > 0 {
		return
	}

	switch classifyTaskKind(root.Intent.Goal) {
	case TaskSummarize:
		root.Kind = TaskSummarize
		root.Mode = TaskReadOnly
		s.spawnSingleLeaf(root, TaskSummarize, ProfileToolsAgent, root.Intent.Goal, root.Intent.Scope, root.Intent.SuccessCriteria)
	case TaskResearch, TaskExplore, TaskProbe:
		root.Kind = TaskResearch
		root.Mode = TaskReadOnly
		s.spawnSingleLeaf(root, TaskResearch, ProfileToolsAgent, root.Intent.Goal, root.Intent.Scope, root.Intent.SuccessCriteria)
	default:
		root.Kind = TaskEdit
		root.Mode = TaskWrite
		s.buildEditWorkflow(root)
	}
}

func (s *AgentScheduler) buildEditWorkflow(root *TaskNode) {
	plan := s.SpawnChild(
		root,
		"",
		"Plan the implementation for: "+root.Intent.Goal,
		ProfileBuildAgent,
		root.Intent.Scope,
		"Return a concise implementation plan.",
	)
	if plan == nil {
		return
	}
	plan.Kind = TaskResearch
	plan.Mode = TaskReadOnly
	plan.Intent.BudgetTokens = max(root.Intent.BudgetTokens/4, 1024)

	execute := s.SpawnChild(
		root,
		"",
		root.Intent.Goal,
		ProfileWorkerAgent,
		root.Intent.Scope,
		"Implement the requested change.",
	)
	if execute == nil {
		return
	}
	execute.Kind = TaskEdit
	execute.Mode = TaskWrite
	execute.Deps = []*TaskNode{plan}
	execute.Intent.BudgetTokens = root.Intent.BudgetTokens

	verify := s.SpawnChild(
		root,
		"",
		"Verify the result of: "+root.Intent.Goal,
		ProfileToolsAgent,
		root.Intent.Scope,
		"Confirm the implementation behaves as requested.",
	)
	if verify == nil {
		return
	}
	verify.Kind = TaskVerify
	verify.Mode = TaskReadOnly
	verify.Deps = []*TaskNode{execute}
	verify.Intent.BudgetTokens = max(root.Intent.BudgetTokens/4, 1024)
}

func (s *AgentScheduler) spawnSingleLeaf(parent *TaskNode, kind TaskKind, profile WorkerProfile, goal string, scope []string, successCriteria string) *TaskNode {
	child := s.SpawnChild(parent, "", goal, profile, scope, successCriteria)
	if child == nil {
		return nil
	}
	child.Kind = kind
	switch kind {
	case TaskSummarize, TaskResearch, TaskExplore, TaskProbe:
		child.Mode = TaskReadOnly
	case TaskVerify:
		child.Mode = TaskReadOnly
	default:
		child.Mode = TaskWrite
	}
	return child
}

func classifyTaskKind(goal string) TaskKind {
	lower := strings.ToLower(goal)
	switch {
	case containsAny(lower, "summarize", "summary", "extract", "compress"):
		return TaskSummarize
	case containsAny(lower, "review", "inspect", "analyze", "analyse", "diagnose", "compare", "where", "why", "what"):
		return TaskResearch
	default:
		return TaskEdit
	}
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// SpawnChild creates a child task under a parent node.
func (s *AgentScheduler) SpawnChild(parent *TaskNode, nodeID string, goal string, profile WorkerProfile, scope []string, successCriteria string) *TaskNode {
	if s == nil || parent == nil {
		return nil
	}
	if nodeID == "" {
		nodeID = fmt.Sprintf("%s-%d", parent.ID, len(parent.Children)+1)
	}
	mode := TaskWrite
	if profile == ProfileToolsAgent {
		mode = TaskReadOnly
	}
	child := &TaskNode{
		ID:                    nodeID,
		SessionID:             nodeID,
		ConversationSessionID: parent.ConversationSessionID,
		Kind:                  TaskEdit,
		Mode:                  mode,
		Profile:               profile,
		Ownership:             InitOwnership(scope),
		Intent:                InitTaskIntent(goal, scope, successCriteria, 0, 0, parent.ID),
	}
	AttachChild(parent, child)
	s.RegisterNode(child)
	return child
}

// Dispatch executes the DAG depth-first. Group nodes dispatch their children;
// leaf nodes are delegated to the supplied worker.
func (s *AgentScheduler) Dispatch(ctx context.Context, root *TaskNode, worker Worker) error {
	if s == nil {
		return errors.New("scheduler is nil")
	}
	if root == nil {
		return errors.New("root task is nil")
	}
	if worker == nil {
		return errors.New("worker is nil")
	}
	return s.dispatchNode(ctx, root, worker)
}

func (s *AgentScheduler) dispatchNode(ctx context.Context, node *TaskNode, worker Worker) error {
	if node == nil {
		return nil
	}
	s.publishStarted(node)

	if IsGroupNode(node) {
		if err := s.dispatchChildren(ctx, node, worker); err != nil {
			s.publishFailed(node, err)
			return err
		}
		s.publishFinished(node, true, "", node.LastOutput)
		return nil
	}

	intent := node.ToRequestIntent()
	var err error
	for attempt := 0; attempt <= max(node.MaxRetries, 0); attempt++ {
		node.RetryCount = attempt
		node.LastOutput, err = worker.RunTask(ctx, node, intent)
		if err == nil {
			s.publishFinished(node, true, "", node.LastOutput)
			return nil
		}
		if attempt < max(node.MaxRetries, 0) {
			s.publishProgress(node, fmt.Sprintf("retrying after error: %v", err))
			continue
		}
	}
	s.publishFailed(node, err)
	return err
}

func (s *AgentScheduler) dispatchChildren(ctx context.Context, parent *TaskNode, worker Worker) error {
	children := append([]*TaskNode(nil), parent.Children...)
	if len(children) == 0 {
		return nil
	}
	if canRunInParallel(children) {
		group, groupCtx := errgroup.WithContext(ctx)
		for _, child := range children {
			child := child
			group.Go(func() error {
				return s.dispatchNode(groupCtx, child, worker)
			})
		}
		return group.Wait()
	}
	for _, child := range children {
		if err := s.dispatchNode(ctx, child, worker); err != nil {
			return err
		}
	}
	return nil
}

func canRunInParallel(nodes []*TaskNode) bool {
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[i] == nil || nodes[j] == nil {
				continue
			}
			if len(nodes[i].Deps) > 0 || len(nodes[j].Deps) > 0 {
				return false
			}
			if nodes[i].Ownership.Overlaps(nodes[j].Ownership) {
				return false
			}
		}
	}
	return true
}

func (s *AgentScheduler) publishStarted(node *TaskNode) {
	if node != nil && node.StartedAt.IsZero() {
		node.StartedAt = time.Now()
	}
	s.publishTraceEvent(EventTaskStarted, node, "running", false, "", "")
}

func (s *AgentScheduler) publishProgress(node *TaskNode, status string) {
	s.publishTraceEvent(EventTaskProgress, node, status, false, "", "")
}

func (s *AgentScheduler) publishFinished(node *TaskNode, success bool, errText, output string) {
	finishTaskNode(node)
	s.publishTraceEvent(EventTaskFinished, node, "done", success, errText, output)
}

func (s *AgentScheduler) publishFailed(node *TaskNode, err error) {
	if err == nil {
		return
	}
	finishTaskNode(node)
	s.publishTraceEvent(EventTaskFailed, node, "failed", false, err.Error(), "")
}

func (s *AgentScheduler) publishPlanned(node *TaskNode) {
	s.publishTraceEvent(EventTaskPlanned, node, "planned", true, "", "")
}

func (s *AgentScheduler) publishTraceEvent(kind EventKind, node *TaskNode, status string, success bool, errText, output string) {
	ev, trace := s.recordEvent(kind, node, status, success, errText, output)
	PublishEvent(ev)
	if s.runtime != nil {
		s.runtime.Emit(ev.AsMessage())
	}
	_ = trace
}

func (s *AgentScheduler) recordEvent(kind EventKind, node *TaskNode, status string, success bool, errText, output string) (Event, agentruntime.TaskTrace) {
	trace := agentruntime.TaskTrace{
		StartedAt:             nodeStartedAt(node),
		FinishedAt:            nodeFinishedAt(node),
		DurationMs:            nodeDurationMs(node),
		ConversationSessionID: nodeConversationSessionID(node),
		SessionID:             nodeSessionID(node),
		NodeID:                nodeID(node),
		ParentID:              nodeParentID(node),
		Depth:                 nodeDepth(node),
		Profile:               string(nodeProfile(node)),
		ProviderID:            nodeProviderID(node),
		ProviderType:          nodeProviderType(node),
		ModelID:               nodeModelID(node),
		RequestID:             nodeRequestID(node),
		Kind:                  agentruntime.TraceKind(kind),
		Status:                status,
		Success:               success,
		Goal:                  nodeGoal(node),
		Scope:                 nodeScope(node),
		Error:                 errText,
		Output:                output,
		InputBytes:            nodeInputBytes(node),
		OutputBytes:           nodeOutputBytes(node),
		InputTokens:           nodeInputTokens(node),
		OutputTokens:          nodeOutputTokens(node),
		TotalTokens:           nodeTotalTokens(node),
		ReasoningTokens:       nodeReasoningTokens(node),
		CacheCreationTokens:   nodeCacheCreationTokens(node),
		CacheReadTokens:       nodeCacheReadTokens(node),
		EstimatedCostUSD:      nodeEstimatedCostUSD(node),
	}
	trace = s.appendTrace(trace)
	event := NewEventFromNode(kind, node, trace, status, success, errText, output)
	return event, trace
}

func finishTaskNode(node *TaskNode) {
	if node == nil {
		return
	}
	if node.StartedAt.IsZero() {
		node.StartedAt = time.Now()
	}
	node.FinishedAt = time.Now()
	node.DurationMs = node.FinishedAt.Sub(node.StartedAt).Milliseconds()
}

func (s *AgentScheduler) appendTrace(trace agentruntime.TaskTrace) agentruntime.TaskTrace {
	if s == nil || s.runtime == nil {
		return trace
	}
	return s.runtime.AppendTrace(trace)
}

func nodeConversationSessionID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.ConversationSessionID
}

func nodeSessionID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.SessionID
}

func nodeID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.ID
}

func nodeParentID(node *TaskNode) string {
	if node == nil || node.Parent == nil {
		return ""
	}
	return node.Parent.ID
}

func nodeDepth(node *TaskNode) int {
	depth := 0
	for current := node; current != nil && current.Parent != nil; current = current.Parent {
		depth++
	}
	return depth
}

func nodeGoal(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.Intent.Goal
}

func nodeScope(node *TaskNode) []string {
	if node == nil {
		return nil
	}
	return append([]string(nil), node.Intent.Scope...)
}

func nodeProfile(node *TaskNode) WorkerProfile {
	if node == nil {
		return ""
	}
	return node.Profile
}

func nodeProviderID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.ProviderID
}

func nodeProviderType(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.ProviderType
}

func nodeModelID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.ModelID
}

func nodeRequestID(node *TaskNode) string {
	if node == nil {
		return ""
	}
	return node.RequestID
}

func nodeStartedAt(node *TaskNode) time.Time {
	if node == nil {
		return time.Time{}
	}
	return node.StartedAt
}

func nodeFinishedAt(node *TaskNode) time.Time {
	if node == nil {
		return time.Time{}
	}
	return node.FinishedAt
}

func nodeDurationMs(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.DurationMs
}

func nodeInputBytes(node *TaskNode) int {
	if node == nil {
		return 0
	}
	return node.InputBytes
}

func nodeOutputBytes(node *TaskNode) int {
	if node == nil {
		return 0
	}
	return node.OutputBytes
}

func nodeInputTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.InputTokens
}

func nodeOutputTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.OutputTokens
}

func nodeTotalTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.TotalTokens
}

func nodeReasoningTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.ReasoningTokens
}

func nodeCacheCreationTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.CacheCreationTokens
}

func nodeCacheReadTokens(node *TaskNode) int64 {
	if node == nil {
		return 0
	}
	return node.CacheReadTokens
}

func nodeEstimatedCostUSD(node *TaskNode) float64 {
	if node == nil {
		return 0
	}
	return node.EstimatedCostUSD
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
