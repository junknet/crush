package scheduler

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/provider"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestTaskTreeHelpers(t *testing.T) {
	t.Parallel()

	parent := &TaskNode{ID: "root", SessionID: "root", Intent: InitTaskIntent("root", []string{"a.go"}, "", 0, 0, "")}
	child := &TaskNode{ID: "child", SessionID: "child", Intent: InitTaskIntent("child", []string{"b.go"}, "", 0, 0, "root")}

	AttachChild(parent, child)
	AttachChild(parent, child)

	require.True(t, IsGroupNode(parent))
	require.False(t, IsGroupNode(child))
	require.Len(t, parent.Children, 1)
	require.Same(t, parent, child.Parent)
	require.True(t, InitOwnership([]string{"a.go"}).Overlaps(InitOwnership([]string{"a.go", "b.go"})))
	require.False(t, InitOwnership([]string{"a.go"}).Overlaps(InitOwnership([]string{"c.go"})))
}

func TestSchedulerDispatchEmitsEvents(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runtime := agentruntime.NewSession("/tmp/project", nil)
	sched := NewAgentScheduler(runtime)
	root := sched.EnsureRoot("session-1", "root goal", []string{"root.go"}, ProfileBuildAgent)
	left := sched.SpawnChild(root, "child-1", "left", ProfileWorkerAgent, []string{"left.go"}, "done")
	right := sched.SpawnChild(root, "child-2", "right", ProfileToolsAgent, []string{"right.go"}, "done")

	require.NotNil(t, left)
	require.NotNil(t, right)

	worker := fakeWorker(func(node *TaskNode, intent provider.RequestIntent) (string, error) {
		require.Equal(t, provider.PurposeEdit, intent.Purpose)
		return node.ID + ":ok", nil
	})

	require.NoError(t, sched.Dispatch(ctx, root, worker))
	require.Equal(t, "child-1:ok", left.LastOutput)
	require.Equal(t, "child-2:ok", right.LastOutput)

	traces := runtime.TraceEntries()
	require.GreaterOrEqual(t, len(traces), 3)
	require.Equal(t, "session-1", traces[0].ConversationSessionID)
	require.Equal(t, agentruntime.TraceKindTaskPlanned, traces[0].Kind)
	require.Equal(t, agentruntime.TraceKindTaskPlanned, traces[1].Kind)
	require.Equal(t, agentruntime.TraceKindTaskPlanned, traces[2].Kind)
}

func TestBuildDefaultWorkflowCreatesOrderedEditDAG(t *testing.T) {
	t.Parallel()

	sched := NewAgentScheduler(agentruntime.NewSession("/tmp/project", nil))
	root := sched.EnsureRoot("session-1", "implement a feature", []string{"main.go"}, ProfileBuildAgent)

	require.NotNil(t, root)
	sched.BuildDefaultWorkflow(root)

	require.Len(t, root.Children, 3)
	plan := root.Children[0]
	execute := root.Children[1]
	verify := root.Children[2]

	require.Equal(t, TaskResearch, plan.Kind)
	require.Equal(t, TaskEdit, execute.Kind)
	require.Equal(t, TaskVerify, verify.Kind)
	require.Len(t, execute.Deps, 1)
	require.Len(t, verify.Deps, 1)
	require.Same(t, plan, execute.Deps[0])
	require.Same(t, execute, verify.Deps[0])
}

func TestEnsureRootReplacesLatestRootPerSession(t *testing.T) {
	t.Parallel()

	sched := NewAgentScheduler(agentruntime.NewSession("/tmp/project", nil))
	first := sched.EnsureRoot("session-1", "root goal", []string{"root.go"}, ProfileBuildAgent)
	second := sched.EnsureRoot("session-1", "root goal 2", []string{"next.go"}, ProfileWorkerAgent)

	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotEqual(t, first.ID, second.ID)
	require.NotSame(t, first, second)

	latest, ok := sched.Root("session-1")
	require.True(t, ok)
	require.Same(t, second, latest)
	require.Equal(t, "root goal 2", latest.Intent.Goal)
	require.Equal(t, []string{"next.go"}, latest.Intent.Scope)
}

type fakeWorker func(node *TaskNode, intent provider.RequestIntent) (string, error)

func (f fakeWorker) RunTask(ctx context.Context, node *TaskNode, intent provider.RequestIntent) (string, error) {
	return f(node, intent)
}
