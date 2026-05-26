package styles

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// ForegroundGrad returns a slice of strings representing the input string
// rendered with a horizontal gradient foreground from color1 to color2. Each
// string in the returned slice corresponds to a grapheme cluster in the input
// string. If bold is true, the rendered strings will be bolded.
func ForegroundGrad(base lipgloss.Style, input string, bold bool, color1, color2 color.Color) []string {
	if input == "" {
		return []string{""}
	}
	if len(input) == 1 {
		style := base.Foreground(color1)
		if bold {
			style.Bold(true)
		}
		return []string{style.Render(input)}
	}
	var clusters []string
	gr := uniseg.NewGraphemes(input)
	for gr.Next() {
		clusters = append(clusters, string(gr.Runes()))
	}

	ramp := lipgloss.Blend1D(len(clusters), color1, color2)
	for i, c := range ramp {
		style := base.Foreground(c)
		if bold {
			style.Bold(true)
		}
		clusters[i] = style.Render(clusters[i])
	}
	return clusters
}

// ApplyForegroundGrad renders a given string with a horizontal gradient
// foreground.
func ApplyForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, false, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}

// ApplyBoldForegroundGrad renders a given string with a horizontal gradient
// foreground.
func ApplyBoldForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, true, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}

// ApplyScrollingForegroundGrad renders a given string with a horizontal gradient
// foreground shifted by the given offset, overlaying a traveling "comet" highlight.
func ApplyScrollingForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color, offset int) string {
	if input == "" {
		return ""
	}
	var clusters []string
	gr := uniseg.NewGraphemes(input)
	for gr.Next() {
		clusters = append(clusters, string(gr.Runes()))
	}
	n := len(clusters)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return base.Foreground(color1).Render(input)
	}

	ramp := lipgloss.Blend1D(n, color1, color2)
	var o strings.Builder

	// Highlight color (pure white for maximum contrast highlight)
	highlight := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	wavelength := 30
	tailLength := 8

	for i := 0; i < n; i++ {
		// 1. Scrolling gradient mapping (left-to-right flow)
		colorIdx := (i - offset) % n
		if colorIdx < 0 {
			colorIdx += n
		}
		c := ramp[colorIdx]

		// 2. Comet highlight overlay (left-to-right flow)
		dist := (i - offset) % wavelength
		if dist < 0 {
			dist += wavelength
		}

		var factor float64
		if dist < tailLength {
			factor = 1.0 - float64(dist)/float64(tailLength)
			factor = factor * factor // Ease-out curve
		}

		if factor > 0 {
			c = blendColor(c, highlight, factor)
		}

		style := base.Foreground(c)
		fmt.Fprint(&o, style.Render(clusters[i]))
	}
	return o.String()
}

func blendColor(c1, c2 color.Color, factor float64) color.Color {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()

	r := uint8((float64(r1)*(1-factor) + float64(r2)*factor) / 256)
	g := uint8((float64(g1)*(1-factor) + float64(g2)*factor) / 256)
	b := uint8((float64(b1)*(1-factor) + float64(b2)*factor) / 256)
	a := uint8((float64(a1)*(1-factor) + float64(a2)*factor) / 256)

	return color.RGBA{R: r, G: g, B: b, A: a}
}
