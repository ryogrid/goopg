package wal

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pgoDecodePhysicalValue is the SECOND decoder of goopg's heap layout (the
// executor's decodePhysicalPGValueMctx is the first), and the two must agree —
// .ralph/PROMPT.md hard-won rule #2. When the reg* family and cid moved from
// varlena text to PG's native 4-byte OID (M0119-0006, the "54th slice"), an
// unrouted regclass/regtype/regprocedure/cid here would have read those bytes
// through the varlena fall-through: the first raw byte taken as a length
// header, so a replicated identifier would reach the subscriber as garbage of
// an arbitrary length rather than failing.
//
// The expected text is the bare UNSIGNED OID (regclasssend/regtypesend/
// regproceduresend/regprocsend/cidsend are all oidsend — pq_sendint32 over the
// OID), which is what the executor's decode arm produces as a KindInt.
func TestPgoDecodeRegFamilyMatchesPGNativeLayout(t *testing.T) {
	for _, tc := range []struct {
		typeName string
		oid      uint32
	}{
		{"regclass", 1259},        // pg_class
		{"regtype", 2206},         // regtype itself
		{"regprocedure", 2202},    // regprocedure itself
		{"regproc", 1289},         // pg_type.typoutput's value domain
		{"regrole", 4096},         // regrole itself
		{"regcollation", 4191},    // regcollation itself
		{"cid", 5677},             // a command id
		{"oid", 1007},             // the base type
		{"regclass", 0},           // InvalidOid
		{"regtype", 0xFFFFFFFF},   // full unsigned range
	} {
		typ := catalog.Type{Name: tc.typeName}
		raw := []byte{
			byte(tc.oid),
			byte(tc.oid >> 8),
			byte(tc.oid >> 16),
			byte(tc.oid >> 24),
		}
		got, n, err := pgoDecodePhysicalValue(typ, raw)
		if err != nil {
			t.Fatalf("pgoDecodePhysicalValue(%s): %v", tc.typeName, err)
		}
		if n != 4 {
			t.Fatalf("%s: consumed %d bytes, want 4 (4-byte OID)", tc.typeName, n)
		}
		want := strconv.FormatUint(uint64(tc.oid), 10)
		if string(got) != want {
			t.Errorf("%s: decoded %q, want %q", tc.typeName, got, want)
		}
	}
}

// TestPgoPhysicalAlignRegFamily pins typalign 'i' (4-byte). An 8-byte answer
// here would decode the identifier correctly and shift every following column
// of the replicated row, so it cannot be caught by the value test above.
func TestPgoPhysicalAlignRegFamily(t *testing.T) {
	for _, name := range []string{"regclass", "regtype", "regprocedure", "regproc", "regrole", "regcollation", "cid"} {
		typ := catalog.Type{Name: name}
		for _, off := range []int{1, 3, 5, 17} {
			if got := pgoPhysicalAlign(off, typ); got != ((off + 3) &^ 3) {
				t.Errorf("pgoPhysicalAlign(%d, %s) = %d, want %d (typalign 'i')", off, name, got, (off+3)&^3)
			}
		}
	}
}
