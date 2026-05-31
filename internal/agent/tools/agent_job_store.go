package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PersistedAgentJob is the durable terminal record of a background sub-agent,
// written when the sub-agent completes. Background jobs live only in the
// in-memory BackgroundShellManager, so without this a resumed session (new
// process, empty manager) fails to retrieve the result of a worker dispatched
// before the restart. Persisting the terminal result lets JobOutput recover a
// completed-but-uncollected sub-agent across a restart.
type PersistedAgentJob struct {
	Status   string `json:"status"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func agentJobResultPath(dataDir, sessionID, jobID string) string {
	return filepath.Join(dataDir, "agent-jobs", sessionID, sanitizeCallID(jobID)+".json")
}

// PersistAgentJobResult writes a background sub-agent's terminal result so a
// later JobOutput — even one in a process that restarted — can recover it.
// Best-effort: a write failure is returned but never fatal to the caller.
func PersistAgentJobResult(dataDir, sessionID, jobID, status, output string, exitCode int) error {
	if dataDir == "" || sessionID == "" || jobID == "" {
		return nil
	}
	dir := filepath.Join(dataDir, "agent-jobs", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(PersistedAgentJob{Status: status, Output: output, ExitCode: exitCode})
	if err != nil {
		return err
	}
	return os.WriteFile(agentJobResultPath(dataDir, sessionID, jobID), b, 0o644)
}

func readPersistedAgentJob(dataDir, sessionID, jobID string) (PersistedAgentJob, bool) {
	if dataDir == "" || sessionID == "" || jobID == "" {
		return PersistedAgentJob{}, false
	}
	b, err := os.ReadFile(agentJobResultPath(dataDir, sessionID, jobID))
	if err != nil {
		return PersistedAgentJob{}, false
	}
	var j PersistedAgentJob
	if err := json.Unmarshal(b, &j); err != nil {
		return PersistedAgentJob{}, false
	}
	return j, true
}
