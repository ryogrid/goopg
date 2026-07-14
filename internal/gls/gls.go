// Package gls provides cheap goroutine-local storage of a backend's
// process number ("backend id"), used to pick a WAL insert-lock stripe
// without deriving a goroutine identity via runtime.Stack.
//
// The carrier is pprof goroutine labels: SetBackendID stamps the calling
// goroutine (via the supported runtime/pprof API) and BackendID reads it
// back. The write side is a standard-library call; only the read side
// needs a //go:linkname to runtime/pprof.runtime_getProfLabel (isolated
// to gls_linkname.go, mirroring the internal/runtimeshim pattern) because
// the standard library exposes no "read the current goroutine's labels"
// function.
//
// Motivation: the WAL append hot path previously called
// activity.LookupCurrentGoroutine() → runtime.Stack on every append,
// which was 57% of server CPU under pgbench simple-update
// (analysis/perf-optimize2, fix-01). BackendID is a pointer load plus a
// one-entry label scan, allocation-free.
//
// Safety: on Go versions outside the supported linkname window, and if a
// runtime self-check (see gls_linkname.go) detects that the pprof label
// layout does not match the mirror, BackendID returns (0, false) and
// callers fall back to stripe 0 — always valid, just less striping.
// goopg reserves goroutine pprof labels for this purpose (no other code
// sets goroutine labels).
package gls

import (
	"context"
	"runtime/pprof"
	"strconv"
)

// labelKey is the pprof label key under which the backend id is stored.
const labelKey = "goopg_backend_id"

// SetBackendID stamps the calling goroutine — and every goroutine it
// later spawns (pprof labels are inherited at `go` time) — with the given
// backend id. Call once per connection at backend startup; it is a
// cold-path call (one small allocation), never on the WAL hot path.
func SetBackendID(id int32) {
	pprof.SetGoroutineLabels(pprof.WithLabels(
		context.Background(),
		pprof.Labels(labelKey, strconv.Itoa(int(id))),
	))
}
