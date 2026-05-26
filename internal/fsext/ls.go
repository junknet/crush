package fsext

import (
	"bytes"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// ListDirectory lists files and directories in the specified path using ripgrep.
func ListDirectory(initialPath string, ignorePatterns []string, depth, limit int) ([]string, bool, error) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return nil, false, fmt.Errorf("ripgrep (rg) not found in PATH")
	}

	args := []string{"--files", "--null", "--no-config"}
	if depth > 0 {
		args = append(args, "--max-depth", fmt.Sprintf("%d", depth))
	}
	for _, pattern := range ignorePatterns {
		if pattern != "" {
			args = append(args, "--glob", "!"+pattern)
		}
	}
	args = append(args, initialPath)

	var stdout bytes.Buffer
	cmd := exec.Command(rgPath, args...)
	cmd.Stdout = &stdout
	err = cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return nil, false, fmt.Errorf("ripgrep error: %w", err)
		}
	}

	outputBytes := stdout.Bytes()
	if len(outputBytes) == 0 {
		return nil, false, nil
	}

	fileList := strings.Split(string(outputBytes), "\x00")
	if len(fileList) > 0 && fileList[len(fileList)-1] == "" {
		fileList = fileList[:len(fileList)-1]
	}

	// Use a map to collect unique paths and include parent directories.
	pathSet := make(map[string]struct{})
	isDirMap := make(map[string]bool)
	for _, f := range fileList {
		pathSet[f] = struct{}{}

		// Add parent directories up to initialPath
		rel := f
		if strings.HasPrefix(f, initialPath) {
			rel = strings.TrimPrefix(f, initialPath)
			rel = strings.TrimPrefix(rel, "/")
			rel = strings.TrimPrefix(rel, "\\")
		}

		parts := strings.Split(rel, "/")
		if len(parts) > 1 {
			for i := 1; i < len(parts); i++ {
				dir := strings.Join(parts[:i], "/")
				fullDir := initialPath
				if dir != "" {
					if strings.HasSuffix(fullDir, "/") || strings.HasSuffix(fullDir, "\\") {
						fullDir += dir
					} else {
						fullDir += "/" + dir
					}
				}
				if _, ok := pathSet[fullDir]; !ok {
					pathSet[fullDir] = struct{}{}
					isDirMap[fullDir] = true
				}
			}
		}
	}

	var results []string
	for p := range pathSet {
		results = append(results, p)
	}

	slices.Sort(results)

	truncated := false
	if limit > 0 && len(results) > limit {
		results = results[:limit]
		truncated = true
	}

	// Add trailing slash to directories to match original behavior.
	for i, p := range results {
		if isDirMap[p] {
			if !strings.HasSuffix(p, "/") && !strings.HasSuffix(p, "\\") {
				results[i] = p + "/"
			}
		}
	}

	return results, truncated, nil
}
