//go:build linux && amd64

package tools

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed bin/rg-linux-amd64
var rgEmbedBytes []byte

var initOnce sync.Once

// EnsureEmbeddedToolsExist extracts the embedded ripgrep binary to ~/.local/share/crush/bin/rg
// and prepends that directory to the process PATH environment variable if not already present.
func EnsureEmbeddedToolsExist() string {
	var targetDir string
	initOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		targetDir = filepath.Join(home, ".local", "share", "crush", "bin")
		_ = os.MkdirAll(targetDir, 0o755)

		rgPath := filepath.Join(targetDir, "rg")
		if _, err := os.Stat(rgPath); os.IsNotExist(err) {
			_ = os.WriteFile(rgPath, rgEmbedBytes, 0o755)
		}

		currentPath := os.Getenv("PATH")
		paths := filepath.SplitList(currentPath)
		found := false
		for _, p := range paths {
			if p == targetDir {
				found = true
				break
			}
		}
		if !found {
			_ = os.Setenv("PATH", targetDir+string(filepath.ListSeparator)+currentPath)
		}
	})
	return targetDir
}
