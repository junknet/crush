// Package agent — lossy image compression for LLM context efficiency.
//
// Android screenshots and large images inflame the context window and can
// trigger provider-side 400 errors on inline-image payloads. This file
// provides compressImageForLLM which:
//   - decodes any image/* MIME type the standard library supports
//   - scales down so the longer edge does not exceed maxImageDimension pixels
//   - re-encodes as JPEG at jpegQuality (lossy, but still visually readable)
//
// The result keeps fidelity sufficient for "can you read this UI/screenshot"
// tasks while staying well under the ~200KB target that avoids provider
// throttling on inlineData payloads.
//
// Only called from preparePrompt for the current-turn user image; historical
// images are stripped to a text placeholder so they do not accumulate context
// tokens across turns.
package agent

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"image/jpeg"
	"log/slog"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// maxImageDimension caps the longer edge after downscale.
	// 1024px is sufficient for most UI / screenshot reading tasks.
	maxImageDimension = 1024

	// jpegQuality trades file size for visual fidelity.
	// 75 gives ~50-150 KB for a typical screenshot, which is well under
	// provider inline-image limits while remaining readable.
	jpegQuality = 75
)

// compressImageForLLM decodes rawBytes (any image/* that stdlib supports),
// downscales to maxImageDimension on the long edge, and re-encodes as JPEG.
// Returns the compressed bytes and "image/jpeg" on success.
// On any decode/encode failure it returns the original bytes and mimeType
// unchanged so the caller can still attempt to send the original.
func compressImageForLLM(rawBytes []byte, mimeType string) (compressed []byte, outMimeType string) {
	if !strings.HasPrefix(mimeType, "image/") {
		return rawBytes, mimeType
	}

	img, _, err := image.Decode(bytes.NewReader(rawBytes))
	if err != nil {
		slog.Warn("compressImageForLLM: failed to decode image, using original",
			"mime_type", mimeType,
			"input_bytes", len(rawBytes),
			"error", err,
		)
		return rawBytes, mimeType
	}

	scaled := scaleDown(img, maxImageDimension)

	var buf bytes.Buffer
	if encErr := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: jpegQuality}); encErr != nil {
		slog.Warn("compressImageForLLM: JPEG encode failed, using original",
			"mime_type", mimeType,
			"input_bytes", len(rawBytes),
			"error", encErr,
		)
		return rawBytes, mimeType
	}

	slog.Debug("compressImageForLLM: image compressed",
		"input_bytes", len(rawBytes),
		"output_bytes", buf.Len(),
		"original_bounds", img.Bounds(),
		"scaled_bounds", scaled.Bounds(),
	)
	return buf.Bytes(), "image/jpeg"
}

// scaleDown returns img scaled so its longer edge is at most maxEdge pixels.
// Returns the original image unchanged if it already fits.
func scaleDown(img image.Image, maxEdge int) image.Image {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}

	var dstW, dstH int
	if w >= h {
		dstW = maxEdge
		dstH = (h * maxEdge) / w
		if dstH < 1 {
			dstH = 1
		}
	} else {
		dstH = maxEdge
		dstW = (w * maxEdge) / h
		if dstW < 1 {
			dstW = 1
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	return dst
}
