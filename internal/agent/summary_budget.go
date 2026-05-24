package agent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/skills"
)

const (
	summaryMaxOutputTokensFloor    int64   = 4_096
	summaryMaxOutputTokensCap      int64   = 20_000
	summaryInputSafetyMarginTokens int64   = 8_192
	summaryRetryLimit              int     = 3
	summaryRetryDropFraction       float64 = 0.20
)

// summarizeOutputTokenBudget reserves a bounded output window for summary generation.
func summarizeOutputTokenBudget(model catwalk.Model) int64 {
	budget := model.DefaultMaxTokens
	if budget < summaryMaxOutputTokensFloor {
		budget = summaryMaxOutputTokensFloor
	}
	if budget > summaryMaxOutputTokensCap {
		budget = summaryMaxOutputTokensCap
	}
	return budget
}

// summarizeInputTokenBudget reserves output and a small safety margin from the
// model context window before we feed history into the summarizer.
func summarizeInputTokenBudget(model catwalk.Model) int64 {
	if model.ContextWindow <= 0 {
		return 0
	}
	budget := model.ContextWindow - summarizeOutputTokenBudget(model) - summaryInputSafetyMarginTokens
	if budget < 0 {
		return 0
	}
	return budget
}

// selectSummaryMessagesForBudget keeps the newest messages that fit within the
// supplied budget. The result is a suffix of the original slice, so the most
// recent context is preserved when the history is too long.
func selectSummaryMessagesForBudget(
	msgs []message.Message,
	supportsImages bool,
	budget int64,
) ([]message.Message, bool, int64) {
	if len(msgs) == 0 {
		return nil, false, 0
	}

	if budget <= 0 {
		estimated := estimateSummaryMessageTokens(msgs, supportsImages)
		return msgs, false, estimated
	}

	var used int64
	start := len(msgs) - 1
	for i := len(msgs) - 1; i >= 0; i-- {
		msgTokens := estimateSummaryRawMessageTokens(msgs[i], supportsImages)
		if i < len(msgs)-1 && used+msgTokens > budget {
			break
		}
		used += msgTokens
		start = i
	}

	selected := msgs[start:]
	if len(selected) == 0 {
		selected = msgs[len(msgs)-1:]
	}

	estimated := estimateSummaryMessageTokens(selected, supportsImages)
	return selected, start > 0, estimated
}

func estimateSummaryMessageTokens(msgs []message.Message, supportsImages bool) int64 {
	var total int64
	for _, msg := range msgs {
		total += estimateSummaryRawMessageTokens(msg, supportsImages)
	}
	return total
}

func estimateSummaryRawMessageTokens(msg message.Message, supportsImages bool) int64 {
	aiMsgs := msg.ToAIMessage()
	if !supportsImages {
		for i := range aiMsgs {
			if aiMsgs[i].Role == fantasy.MessageRoleUser {
				aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
			}
		}
	}
	return estimateFantasyMessagesTokens(aiMsgs)
}

func estimateFantasyMessagesTokens(msgs []fantasy.Message) int64 {
	var total int64
	for _, msg := range msgs {
		total += estimateFantasyMessageTokens(msg)
	}
	return total
}

func estimateFantasyMessageTokens(msg fantasy.Message) int64 {
	total := skills.ApproxTokenCount(string(msg.Role))
	for _, part := range msg.Content {
		total += estimateFantasyMessagePartTokens(part)
	}
	return int64(total)
}

func estimateFantasyMessagePartTokens(part fantasy.MessagePart) int {
	switch p := part.(type) {
	case fantasy.TextPart:
		return skills.ApproxTokenCount(p.Text)
	case fantasy.ReasoningPart:
		return skills.ApproxTokenCount(p.Text)
	case fantasy.FilePart:
		return skills.ApproxTokenCount(p.Filename) + skills.ApproxTokenCount(p.MediaType) + approximateBinaryTokenCount(p.Data)
	case fantasy.ToolCallPart:
		return skills.ApproxTokenCount(p.ToolCallID) + skills.ApproxTokenCount(p.ToolName) + skills.ApproxTokenCount(p.Input)
	case fantasy.ToolResultPart:
		return skills.ApproxTokenCount(p.ToolCallID) + estimateToolResultOutputTokens(p.Output)
	default:
		return skills.ApproxTokenCount(fmt.Sprintf("%#v", part))
	}
}

func estimateToolResultOutputTokens(output fantasy.ToolResultOutputContent) int {
	switch p := output.(type) {
	case fantasy.ToolResultOutputContentText:
		return skills.ApproxTokenCount(p.Text)
	case fantasy.ToolResultOutputContentError:
		if p.Error == nil {
			return 0
		}
		return skills.ApproxTokenCount(p.Error.Error())
	case fantasy.ToolResultOutputContentMedia:
		return skills.ApproxTokenCount(p.Text) + skills.ApproxTokenCount(p.MediaType) + approximateBinaryTokenCount([]byte(p.Data))
	default:
		return skills.ApproxTokenCount(fmt.Sprintf("%#v", output))
	}
}

func approximateBinaryTokenCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	estimate := len(data) / 4
	if estimate < 1 {
		return 1
	}
	return estimate
}

func isSummaryContextTooLargeError(err error) bool {
	var providerErr *fantasy.ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	if providerErr.IsContextTooLarge() {
		return true
	}

	message := strings.ToLower(providerErr.Message)
	if strings.Contains(message, "input token count exceeds") {
		return true
	}
	if strings.Contains(message, "prompt is too long") {
		return true
	}
	if strings.Contains(message, "maximum number of tokens") {
		return true
	}
	if providerErr.StatusCode == http.StatusBadRequest && strings.Contains(message, "token") {
		return true
	}
	return false
}

func summaryRetryDropCount(messageCount int) int {
	dropCount := int(float64(messageCount) * summaryRetryDropFraction)
	if dropCount < 1 {
		dropCount = 1
	}
	if dropCount >= messageCount {
		dropCount = messageCount - 1
	}
	if dropCount < 1 {
		dropCount = 1
	}
	return dropCount
}
