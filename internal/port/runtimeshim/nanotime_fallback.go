//go:build !go1.24 || go1.27 || noLinkname

package runtimeshim

import "time"

// Nanotime falls back to time.Now().UnixNano() when the linkname
// target is not stable on this Go version. Correct but slower; the
// goopg hot paths absorb the ~50 ns/call until a runtimeshim review
// promotes the supported window.
func Nanotime() int64 { return time.Now().UnixNano() }
