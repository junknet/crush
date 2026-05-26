//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
)

func main() {
	fmt.Println("Starting hang verification with blocker...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cwd, _ := os.Getwd()
	blockFuncs := []shell.BlockFunc{
		shell.CommandsBlocker([]string{"view", "vim", "vi"}),
	}

	opts := shell.RunOptions{
		Command:    "view go.mod",
		Cwd:        cwd,
		Env:        os.Environ(),
		BlockFuncs: blockFuncs,
	}

	start := time.Now()
	err := shell.Run(ctx, opts)
	duration := time.Since(start)

	fmt.Printf("Shell run duration: %v\n", duration)
	if err != nil {
		fmt.Printf("Shell run exited with error: %v\n", err)
	} else {
		fmt.Println("Shell run exited successfully.")
	}

	if duration >= 1*time.Second {
		fmt.Println("FAIL: Shell run took too long!")
		os.Exit(1)
	} else {
		fmt.Println("SUCCESS: Shell run was blocked and exited immediately.")
	}
}
