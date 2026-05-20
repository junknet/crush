package tools

import (
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
)

type commandOutcome string

const (
	commandOutcomeSucceeded   commandOutcome = "succeeded"
	commandOutcomeNoMatch     commandOutcome = "no_match"
	commandOutcomeFailed      commandOutcome = "failed"
	commandOutcomeInterrupted commandOutcome = "interrupted"
)

type commandSemantics struct {
	ExitCode int
	Outcome  commandOutcome
	Success  bool
}

func deriveCommandSemantics(command, stdout string, execErr error) commandSemantics {
	exitCode := shell.ExitCode(execErr)
	if execErr == nil {
		return commandSemantics{
			ExitCode: exitCode,
			Outcome:  commandOutcomeSucceeded,
			Success:  true,
		}
	}
	if shell.IsInterrupt(execErr) {
		return commandSemantics{
			ExitCode: exitCode,
			Outcome:  commandOutcomeInterrupted,
			Success:  false,
		}
	}
	if isNoMatchSearchExit(command, stdout, exitCode) {
		return commandSemantics{
			ExitCode: exitCode,
			Outcome:  commandOutcomeNoMatch,
			Success:  true,
		}
	}
	return commandSemantics{
		ExitCode: exitCode,
		Outcome:  commandOutcomeFailed,
		Success:  false,
	}
}

func isNoMatchSearchExit(command, stdout string, exitCode int) bool {
	if exitCode != 1 || strings.TrimSpace(stdout) != "" {
		return false
	}
	switch firstCommandWord(command) {
	case "grep", "rg", "ripgrep":
		return true
	default:
		return false
	}
}

func firstCommandWord(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "'\"")
}
