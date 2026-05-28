package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// syntheticPNG generates a solid-color PNG of the given dimensions for testing.
func syntheticPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestCompressImageForLLM_passThrough_nonImage(t *testing.T) {
	input := []byte("hello world")
	out, outMime := compressImageForLLM(input, "text/plain")
	require.Equal(t, input, out)
	require.Equal(t, "text/plain", outMime)
}

func TestCompressImageForLLM_smallImageUnchangedDimensions(t *testing.T) {
	// 100x100 is well under maxImageDimension=1024; dimensions must not grow.
	raw := syntheticPNG(t, 100, 100)
	compressed, outMime := compressImageForLLM(raw, "image/png")
	require.Equal(t, "image/jpeg", outMime)

	img, _, err := image.Decode(bytes.NewReader(compressed))
	require.NoError(t, err)
	require.LessOrEqual(t, img.Bounds().Dx(), 100)
	require.LessOrEqual(t, img.Bounds().Dy(), 100)
}

func TestCompressImageForLLM_largeImageScalesDown(t *testing.T) {
	// 3000x2000 must be scaled so longer edge == maxImageDimension.
	raw := syntheticPNG(t, 3000, 2000)
	compressed, outMime := compressImageForLLM(raw, "image/png")
	require.Equal(t, "image/jpeg", outMime)

	img, _, err := image.Decode(bytes.NewReader(compressed))
	require.NoError(t, err)
	b := img.Bounds()
	// Longer edge must equal maxImageDimension (±1 for integer rounding).
	require.LessOrEqual(t, max(b.Dx(), b.Dy()), maxImageDimension+1)
	// Shorter edge must have scaled proportionally: 2000/3000*1024 ≈ 682.
	require.LessOrEqual(t, min(b.Dx(), b.Dy()), 690,
		"shorter edge should scale proportionally with the longer edge")
}

func TestCompressImageForLLM_tallImageScalesDown(t *testing.T) {
	raw := syntheticPNG(t, 500, 4000)
	compressed, outMime := compressImageForLLM(raw, "image/png")
	require.Equal(t, "image/jpeg", outMime)

	img, _, err := image.Decode(bytes.NewReader(compressed))
	require.NoError(t, err)
	b := img.Bounds()
	require.LessOrEqual(t, max(b.Dx(), b.Dy()), maxImageDimension+1,
		"longer edge (height) should be capped at maxImageDimension")
}

func TestCompressImageForLLM_invalidBytes_fallback(t *testing.T) {
	garbage := []byte("not an image at all")
	out, outMime := compressImageForLLM(garbage, "image/png")
	// Must fall back to original on decode failure.
	require.Equal(t, garbage, out)
	require.Equal(t, "image/png", outMime)
}

func TestCompressImageForLLM_outputIsJPEG(t *testing.T) {
	raw := syntheticPNG(t, 200, 200)
	compressed, outMime := compressImageForLLM(raw, "image/png")
	require.Equal(t, "image/jpeg", outMime)
	// JPEG files start with FF D8 FF.
	require.True(t, strings.HasPrefix(string(compressed), "\xff\xd8\xff"),
		"output must be a valid JPEG")
}
