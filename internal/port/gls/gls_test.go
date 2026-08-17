package gls

import (
	"sync"
	"testing"
)

// TestBackendIDRoundTrip is the layout canary: on a supported runtime it
// must set and read back an id. If the pprof label-map layout mirror in
// gls_linkname.go drifts on a Go upgrade, this fails (rather than silently
// returning garbage on the hot path).
func TestBackendIDRoundTrip(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		SetBackendID(4242)
		id, ok := BackendID()
		if !ok {
			t.Errorf("BackendID reported unset after SetBackendID (glsUsable=%v); "+
				"pprof label-map layout may have changed on this Go version", usableForTest())
			return
		}
		if id != 4242 {
			t.Errorf("BackendID = %d, want 4242", id)
		}
	}()
	<-done
}

// TestBackendIDUnsetIsZeroFalse verifies an unstamped goroutine reports
// (0,false). This is the invariant the WAL writer relies on: the
// unregistered state.loop goroutine must fall back to stripe 0.
func TestBackendIDUnsetIsZeroFalse(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if id, ok := BackendID(); ok || id != 0 {
			t.Errorf("unstamped goroutine BackendID = (%d,%v), want (0,false)", id, ok)
		}
	}()
	<-done
}

// TestBackendIDInherited verifies pprof labels propagate to child
// goroutines spawned after SetBackendID (the property that lets a backend
// stamp itself once and have any helper goroutines it spawns inherit it).
func TestBackendIDInherited(t *testing.T) {
	var wg sync.WaitGroup
	parent := make(chan struct{})
	go func() {
		SetBackendID(77)
		var childID int32
		var childOK bool
		wg.Add(1)
		go func() {
			defer wg.Done()
			childID, childOK = BackendID()
		}()
		wg.Wait()
		if usableForTest() && (!childOK || childID != 77) {
			t.Errorf("child BackendID = (%d,%v), want (77,true)", childID, childOK)
		}
		close(parent)
	}()
	<-parent
}
