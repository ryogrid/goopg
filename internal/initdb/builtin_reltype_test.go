package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuiltinRelTypesRegistered pins M0149-0001h1a34 slice 1: initdb installs
// PG 18.3's system relation → rowtype map, so the virtual pg_class renders a
// catalog's reltype (pg_class 83, pg_type 71, pg_namespace 10047) and 0 for
// an index.
func TestBuiltinRelTypesRegistered(t *testing.T) {
	for rel, want := range map[uint32]uint32{1259: 83, 1247: 71, 1249: 75, 2615: 10047, 2613: 10025, 2662: 0, 16384: 0} {
		if got := catalog.BuiltinRelType(rel); got != want {
			t.Errorf("BuiltinRelType(%d) = %d, want %d", rel, got, want)
		}
	}
}
