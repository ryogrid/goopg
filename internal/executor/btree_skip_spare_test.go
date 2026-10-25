package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
)

// TestSkipEnumKeepsSpareCursors pins M0122-0012 slice 77: a skip scan's
// enumeration re-aims the same two cursors for every descent, and neither a
// rescan's reset (operators_index.go) nor clearing the rest of its state drops
// them — PG's one BTScanOpaque per scan, not one cursor per primitive scan.
func TestSkipEnumKeepsSpareCursors(t *testing.T) {
	ws, es := new(nbtree.ScanCursor), new(nbtree.ScanCursor)
	e := btreeSkipEnum{walkSpare: ws, enumSpare: es, walkCur: ws, gProbe: []byte{1}, seek: []byte{2}, seekExcl: true}
	for _, done := range []bool{true, false} {
		e.reset(done)
		if e.walkSpare != ws || e.enumSpare != es {
			t.Fatalf("reset(%v) dropped a spare cursor", done)
		}
		if e.done != done || e.walkCur != nil || e.gProbe != nil || e.seek != nil || e.seekExcl {
			t.Fatalf("reset(%v) left enumeration state behind: %+v", done, e)
		}
	}
}
