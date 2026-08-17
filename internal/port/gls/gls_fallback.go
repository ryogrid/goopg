//go:build !go1.24 || go1.27 || noLinkname

package gls

// BackendID always reports "unset" on runtimes outside the supported
// linkname window (or when built with -tags noLinkname). Callers fall
// back to WAL stripe 0 — always valid, just without per-backend striping.
// Promote the supported window in gls_linkname.go after verifying the
// runtime/pprof label-map layout on the new Go release.
func BackendID() (int32, bool) { return 0, false }

// usableForTest reports whether the linkname read is active on this
// runtime; always false in the fallback build.
func usableForTest() bool { return false }
