//go:build linux && amd64

package tools

// EmbeddedRgBytes returns the embedded ripgrep binary for this build,
// or nil when the build did not include one. Exposed so the iodriver
// package can SFTP-push it to remote SSH hosts that lack ripgrep
// (one-time bootstrap per host, then every future remote grep takes
// the single-round-trip --json path).
//
// The actual byte slice is owned by embed_rg.go (build-tagged
// linux/amd64); this thin accessor keeps iodriver decoupled from the
// embed directive so it can compile on every platform.
func EmbeddedRgBytes() []byte { return rgEmbedBytes }
