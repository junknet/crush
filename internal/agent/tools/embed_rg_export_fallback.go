//go:build !(linux && amd64)

package tools

// EmbeddedRgBytes returns nil on platforms where no rg binary is
// embedded. iodriver treats a nil result as "no fast-path bootstrap
// possible" and uses the portable `grep -rn` fallback for remote
// search.
func EmbeddedRgBytes() []byte { return nil }
