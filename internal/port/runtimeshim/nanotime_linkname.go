//go:build go1.24 && !go1.27 && !noLinkname

package runtimeshim

import _ "unsafe" // required for //go:linkname

// nanotimeRuntime is linked to the Go runtime's internal monotonic
// clock source. It is a single-MOV read of the per-OS monotonic
// counter (~5 ns on Linux/amd64) vs ~50 ns for time.Now() which adds
// VDSO + wall-clock conversion overhead on top of the same monotonic
// read.
//
// The result is monotonically non-decreasing within a single process
// lifetime, expressed in nanoseconds, but is NOT a wall-clock value
// and is NOT comparable across processes or after suspend/resume.
//
//go:linkname nanotimeRuntime runtime.nanotime
func nanotimeRuntime() int64

// Nanotime returns a monotonic timestamp in nanoseconds suitable for
// hot-path duration measurement. See package docs for the supported
// Go-version window; on unsupported versions Nanotime falls back to
// time.Now().UnixNano() via the inverse-tagged file.
func Nanotime() int64 { return nanotimeRuntime() }
