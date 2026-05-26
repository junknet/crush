//go:build !(linux && amd64)

package tools

// EnsureEmbeddedToolsExist is a stub fallback for non-linux-amd64 platforms.
func EnsureEmbeddedToolsExist() string {
	return ""
}
