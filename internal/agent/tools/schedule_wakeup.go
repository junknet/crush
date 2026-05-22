package tools

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/pubsub"
)

const (
	ScheduleWakeupToolName = "schedule_wakeup"
	// MinWakeupSeconds / MaxWakeupSeconds clamp the delay so a wakeup is neither
	// a tight busy-loop nor effectively never.
	MinWakeupSeconds = 5
	MaxWakeupSeconds = 24 * 60 * 60
)

//go:embed schedule_wakeup.md
var scheduleWakeupDescription string

// WakeupRequest asks the coordinator to resume a session after a timer fires.
type WakeupRequest struct {
	SessionID string
	Reason    string
}

var wakeupBroker = pubsub.NewBroker[WakeupRequest]()

// SubscribeWakeups returns a channel of scheduled wake-up requests. The agent
// coordinator subscribes to drive timer-based re-wakeups.
func SubscribeWakeups(ctx context.Context) <-chan pubsub.Event[WakeupRequest] {
	return wakeupBroker.Subscribe(ctx)
}

type ScheduleWakeupParams struct {
	DelaySeconds int    `json:"delay_seconds" description:"Seconds from now to wake the agent (5..86400)"`
	Reason       string `json:"reason" description:"What to do or check when woken (concrete, e.g. 're-check CI run 123')"`
}

type ScheduleWakeupResponseMetadata struct {
	DelaySeconds int    `json:"delay_seconds"`
	Reason       string `json:"reason"`
}

func NewScheduleWakeupTool() fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ScheduleWakeupToolName,
		scheduleWakeupDescription,
		func(ctx context.Context, params ScheduleWakeupParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Reason == "" {
				return fantasy.NewTextErrorResponse("missing reason"), nil
			}
			delay := params.DelaySeconds
			if delay < MinWakeupSeconds {
				delay = MinWakeupSeconds
			}
			if delay > MaxWakeupSeconds {
				delay = MaxWakeupSeconds
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("schedule_wakeup requires an active session"), nil
			}

			reason := params.Reason
			time.AfterFunc(time.Duration(delay)*time.Second, func() {
				wakeupBroker.Publish(pubsub.CreatedEvent, WakeupRequest{
					SessionID: sessionID,
					Reason:    reason,
				})
			})

			metadata := ScheduleWakeupResponseMetadata{DelaySeconds: delay, Reason: reason}
			response := fmt.Sprintf(
				"Scheduled a wake-up in %ds. This turn will end now; you'll be automatically continued then. Reason: %s. Do not poll.",
				delay, reason)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}
