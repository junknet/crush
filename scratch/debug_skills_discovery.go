//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/crush/internal/skills"
)

func main() {
	// We bypass config.Load here because crush.yaml contains an unsupported "auditor" model type.
	// We directly call DiscoverWithStates on the ~/.claude/skills directory.
	userPaths := []string{os.ExpandEnv("$HOME/.claude/skills")}
	fmt.Printf("Directly scanning: %v\n", userPaths)

	discovered, states := skills.DiscoverWithStates(userPaths)

	fmt.Printf("Discovered %d skills:\n", len(discovered))
	for i, s := range discovered {
		fmt.Printf("  [%d] Name: %s, Path: %s, SkillFilePath: %s\n", i, s.Name, s.Path, s.SkillFilePath)
	}

	fmt.Printf("Discovery states (%d total):\n", len(states))
	for i, st := range states {
		var stateStr string
		if st.State == skills.StateNormal {
			stateStr = "Normal"
		} else {
			stateStr = fmt.Sprintf("Error (%v)", st.Err)
		}
		fmt.Printf("  [%d] Name: %s, State: %s, Path: %s\n", i, st.Name, stateStr, st.Path)
	}
}
