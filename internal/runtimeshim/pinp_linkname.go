//go:build go1.24 && !go1.27

package runtimeshim

import _ "unsafe" // required for //go:linkname

// runtime_procPin / runtime_procUnpin are the runtime-internal primitives
// used by sync.Pool to pin a goroutine to its current P with zero atomic
// operations. While pinned, the runtime increments m.locks, which disables
// preemption and goroutine migration; per-P sharded data structures can
// therefore be mutated without synchronisation for the duration of the
// pinned window.
//
// The caller contract is strict: between PinP and UnpinP the goroutine
// MUST NOT perform any operation that can block — no channel send/recv,
// no mutex acquire that may wait, no syscall, no GC-able allocation that
// might trigger an STW. Violating this contract risks deadlock with the
// runtime's preemption logic.
//
//go:linkname runtime_procPin runtime.procPin
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
func runtime_procUnpin()

// PinP pins the calling goroutine to its current P and returns the P's
// integer index in [0, GOMAXPROCS). The returned index is stable for the
// duration of the pinned window and is suitable for indexing per-P
// sharded data.
//
// Callers MUST call UnpinP before any potentially-blocking operation.
// The pin/unpin pair must be balanced on every code path; a typical
// usage embeds UnpinP immediately, without a defer (defers are
// supported by the runtime but add ~20 ns versus inline unpin, which
// negates the win for sub-100-ns hot paths).
func PinP() int { return runtime_procPin() }

// UnpinP releases the pin established by PinP. Calling UnpinP without a
// matching prior PinP is undefined behaviour.
func UnpinP() { runtime_procUnpin() }
