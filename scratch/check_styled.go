//go:build ignore

package main

import (
	"fmt"
	"os"

	uv "github.com/charmbracelet/ultraviolet"
)

func main() {
	content, err := os.ReadFile("/tmp/landing_view.txt")
	if err != nil {
		fmt.Printf("failed to read file: %v\n", err)
		return
	}

	styled := uv.NewStyledString(string(content))

	w, h := 160, 45
	scr := uv.NewScreenBuffer(w, h)

	// Draw to the layout.main rectangle
	styled.Draw(scr, uv.Rect(2, 5, 158, 40))

	nonEmptyCount := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := scr.CellAt(x, y)
			if cell != nil && !cell.IsZero() && cell.Content != " " {
				nonEmptyCount++
				if nonEmptyCount < 50 {
					fmt.Printf("y=%d, x=%d: content=%q, width=%d, style=%v\n", y, x, cell.Content, cell.Width, cell.Style)
				}
			}
		}
	}
	fmt.Printf("Total non-empty cells in virtual screen: %d\n", nonEmptyCount)
}
