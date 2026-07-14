//go:build go1.24 && !go1.27 && !noLinkname

package gls

import (
	"strconv"
	"unsafe" // required for //go:linkname and the label-map pointer cast
)

// runtime_getProfLabel returns an unsafe.Pointer to the calling
// goroutine's pprof label map (*runtime/pprof.labelMap), or nil when no
// labels are set. It is the read counterpart of
// runtime/pprof.SetGoroutineLabels; the standard library keeps it
// internal, so we link to it directly.
//
//go:linkname runtime_getProfLabel runtime/pprof.runtime_getProfLabel
func runtime_getProfLabel() unsafe.Pointer

// labelMapMirror mirrors the memory layout of runtime/pprof.labelMap on
// the supported Go window (1.24–1.26):
//
//	type labelMap struct { label.Set }          // pprof
//	type Set      struct { List []Label }        // internal/runtime/pprof/label
//	type Label    struct { Key, Value string }
//
// so labelMap is layout-identical to `struct{ list []labelMirror }`. The
// runtime self-check (glsUsable) validates this before any hot-path read
// trusts it.
type labelMirror struct {
	key   string
	value string
}

type labelMapMirror struct {
	list []labelMirror
}

// glsUsable is set once, at package init, by a recovered round-trip probe
// on a throwaway goroutine. If the linkname read or the layout mirror is
// wrong on this runtime, the probe fails (or is recovered) and BackendID
// permanently returns (0, false), so callers fall back to stripe 0.
var glsUsable = probeLayout()

func probeLayout() bool {
	done := make(chan bool, 1)
	go func() {
		ok := false
		// A wild pointer deref from a layout mismatch may panic; recover
		// so a bad runtime degrades to the stripe-0 fallback instead of
		// crashing the process.
		defer func() {
			_ = recover()
			done <- ok
		}()
		const probeID = 1234567
		SetBackendID(probeID)
		v, got := readLabelID()
		ok = got && v == probeID
	}()
	return <-done
}

// readLabelID reads the backend id from the calling goroutine's pprof
// labels via the mirrored layout. Kept separate from BackendID so the
// probe can exercise it before glsUsable is trusted.
func readLabelID() (int32, bool) {
	p := runtime_getProfLabel()
	if p == nil {
		return 0, false
	}
	lm := (*labelMapMirror)(p)
	for i := range lm.list {
		if lm.list[i].key == labelKey {
			n, err := strconv.Atoi(lm.list[i].value)
			if err != nil {
				return 0, false
			}
			return int32(n), true
		}
	}
	return 0, false
}

// BackendID returns the backend id stamped on the calling goroutine by
// SetBackendID, or (0, false) if none is set (or the runtime is
// unsupported). Allocation-free; safe to call on the WAL hot path.
func BackendID() (int32, bool) {
	if !glsUsable {
		return 0, false
	}
	return readLabelID()
}

// usableForTest reports whether the linkname read is active on this
// runtime; used only by tests to phrase assertions correctly.
func usableForTest() bool { return glsUsable }
