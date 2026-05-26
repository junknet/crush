//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charlievieth/fastwalk"
)

func main() {
	base := filepath.Join(os.Getenv("HOME"), ".claude", "skills")
	fmt.Printf("Scanning base: %s\n", base)

	conf := fastwalk.Config{
		Follow:  true,
		ToSlash: fastwalk.DefaultToSlash(),
	}

	err := fastwalk.Walk(&conf, base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("Error at %s: %v\n", path, err)
			return nil
		}

		isDir := d.IsDir()
		info, _ := d.Info()
		isSymlink := info.Mode()&os.ModeSymlink != 0

		fmt.Printf("Path: %s | isDir: %t | isSymlink: %t | Mode: %s\n", path, isDir, isSymlink, info.Mode().String())
		return nil
	})

	if err != nil {
		fmt.Printf("Walk failed: %v\n", err)
	}
}
