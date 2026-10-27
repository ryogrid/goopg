package executor

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
)

// TestPGIndexIntWidthMatchesComparator pins M0122-0012 C37's descriptor
// stamp: nbtree.PGKeyAttr.IntWidth lets compareFixedPrefix compare an
// attribute inline as a signed integer, so it must be set exactly for the
// key types whose comparator is PGCompareInt2/4/8, with that comparator's
// width — never for oid (unsigned order) or any other type.
func TestPGIndexIntWidthMatchesComparator(t *testing.T) {
	ptr := func(f nbtree.PGAttrComparator) uintptr {
		if f == nil {
			return 0
		}
		return reflect.ValueOf(f).Pointer()
	}
	width := map[uintptr]uint8{
		ptr(nbtree.PGCompareInt2): 2,
		ptr(nbtree.PGCompareInt4): 4,
		ptr(nbtree.PGCompareInt8): 8,
	}
	oids := []uint32{
		catalog.OIDBool, catalog.OIDChar, catalog.OIDName, catalog.OIDInt2, catalog.OIDInt4, catalog.OIDDate,
		catalog.OIDInt8, catalog.OIDTime, catalog.OIDTimestamp, catalog.OIDTimestampTZ, catalog.OIDOID,
		catalog.OIDFloat4, catalog.OIDFloat8, catalog.OIDBytea, catalog.OIDText, catalog.OIDVarChar,
		catalog.OIDBpChar, catalog.OIDNumeric, catalog.OIDUUID, catalog.OIDTimeTZ,
	}
	stamped := 0
	for _, oid := range oids {
		want := width[ptr(pgIndexComparatorForOID(oid))]
		got := pgIndexIntWidthForOID(oid, userTypeAttrsForOID(oid).TypLen)
		if got != want {
			t.Errorf("oid %d: IntWidth %d, comparator width %d", oid, got, want)
		}
		if got != 0 {
			stamped++
		}
	}
	if stamped != 7 {
		t.Errorf("stamped %d types, want 7 (int2, int4, date, int8, time, timestamp, timestamptz)", stamped)
	}
}
