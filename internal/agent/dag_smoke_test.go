package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/provider"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func TestDAGSmokeFlowUsesSchedulerRuntimeAndCoordinator(t *testing.T) {
	env := testEnv(t)

	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	coord := &coordinator{
		cfg:      cfg,
		sessions: env.sessions,
		runtime:  agentruntime.NewSession(env.workingDir, nil),
	}

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	taskSessionID := "dag-smoke-task"
	taskRootID := ""
	eventsCtx, cancelEvents := context.WithCancel(t.Context())
	defer cancelEvents()

	eventsCh := scheduler.SubscribeEvents(eventsCtx)
	observed := make([]scheduler.Event, 0, 12)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range eventsCh {
			if taskRootID != "" && (event.Payload.NodeID == taskRootID || strings.HasPrefix(event.Payload.NodeID, taskRootID+"-")) {
				observed = append(observed, event.Payload)
			}
		}
	}()

	taskRuntime := coord.newTaskRuntime(taskSessionID)
	require.NotNil(t, taskRuntime)

	taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
	taskRoot := coord.ensureChildTask(taskScheduler, parentSession.ID, taskSessionID, "implement a DAG smoke test", 4096)
	require.NotNil(t, taskRoot)
	taskRootID = taskRoot.ID
	taskScheduler.BuildDefaultWorkflow(taskRoot)

	calls := make([]struct {
		NodeID string
		Kind   scheduler.TaskKind
		Prompt string
	}, 0, 3)

	worker := scheduler.WorkerFunc(func(ctx context.Context, node *scheduler.TaskNode, intent provider.RequestIntent) (string, error) {
		prompt := coord.composeTaskPrompt(taskRuntime, node, "implement a DAG smoke test")
		calls = append(calls, struct {
			NodeID string
			Kind   scheduler.TaskKind
			Prompt string
		}{
			NodeID: node.ID,
			Kind:   node.Kind,
			Prompt: prompt,
		})

		var output string
		switch node.Kind {
		case scheduler.TaskResearch:
			output = "PLAN OUTPUT"
		case scheduler.TaskEdit:
			output = "EXECUTE OUTPUT"
		case scheduler.TaskVerify:
			output = "VERIFY OUTPUT"
		default:
			output = "UNEXPECTED OUTPUT"
		}

		coord.recordTaskOutcome(taskRuntime, node, prompt, output)
		return output, nil
	})

	err = taskScheduler.Dispatch(t.Context(), taskRoot, worker)
	require.NoError(t, err)

	cancelEvents()
	<-done

	require.Len(t, calls, 3)
	require.Equal(t, scheduler.TaskResearch, calls[0].Kind)
	require.Equal(t, "Plan the implementation for: implement a DAG smoke test", calls[0].Prompt)
	require.Equal(t, scheduler.TaskEdit, calls[1].Kind)
	require.Equal(t, "implement a DAG smoke test\n\nImplementation plan:\nPLAN OUTPUT", calls[1].Prompt)
	require.Equal(t, scheduler.TaskVerify, calls[2].Kind)
	require.Equal(t, "Verify the result of: implement a DAG smoke test\n\nImplementation output:\nEXECUTE OUTPUT", calls[2].Prompt)

	plan, ok := taskRuntime.Fact("task.plan")
	require.True(t, ok)
	require.Equal(t, "PLAN OUTPUT", plan)

	output, ok := taskRuntime.Fact("task.output")
	require.True(t, ok)
	require.Equal(t, "EXECUTE OUTPUT", output)

	verify, ok := taskRuntime.Fact("task.verify")
	require.True(t, ok)
	require.Equal(t, "VERIFY OUTPUT", verify)

	require.Equal(t, []scheduler.EventKind{
		scheduler.EventTaskPlanned,
		scheduler.EventTaskPlanned,
		scheduler.EventTaskPlanned,
		scheduler.EventTaskPlanned,
		scheduler.EventTaskStarted,
		scheduler.EventTaskStarted,
		scheduler.EventTaskFinished,
		scheduler.EventTaskStarted,
		scheduler.EventTaskFinished,
		scheduler.EventTaskStarted,
		scheduler.EventTaskFinished,
		scheduler.EventTaskFinished,
	}, eventKinds(observed))
	require.Equal(t, []string{
		taskRoot.ID,
		taskRoot.ID + "-1",
		taskRoot.ID + "-2",
		taskRoot.ID + "-3",
		taskRoot.ID,
		taskRoot.ID + "-1",
		taskRoot.ID + "-1",
		taskRoot.ID + "-2",
		taskRoot.ID + "-2",
		taskRoot.ID + "-3",
		taskRoot.ID + "-3",
		taskRoot.ID,
	}, eventNodeIDs(observed))
}

func eventKinds(events []scheduler.Event) []scheduler.EventKind {
	result := make([]scheduler.EventKind, len(events))
	for i, event := range events {
		result[i] = event.Kind
	}
	return result
}

func eventNodeIDs(events []scheduler.Event) []string {
	result := make([]string, len(events))
	for i, event := range events {
		result[i] = event.NodeID
	}
	return result
}
