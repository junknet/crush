package tools

import "sync"

var remoteMonitorJobs sync.Map

func markRemoteMonitor(shellID string) {
	remoteMonitorJobs.Store(shellID, struct{}{})
}

func unmarkRemoteMonitor(shellID string) {
	// Intentionally keep the marker for the life of this Crush process. Once a
	// remote job has an explicit monitor, the generic done watcher must stay
	// suppressed even after the monitor fires, matching local BackgroundShell
	// behavior.
}

func hasRemoteMonitor(shellID string) bool {
	_, ok := remoteMonitorJobs.Load(shellID)
	return ok
}
