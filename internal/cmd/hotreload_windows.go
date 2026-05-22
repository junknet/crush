//go:build windows

package cmd

// startHotReload is a no-op on Windows: syscall.Exec is unavailable and signal
// semantics differ. Users on Windows can still restart manually.
func startHotReload(_ func()) {}
