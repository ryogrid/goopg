package xlog

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
// The 4-byte decode is the BINARY-mode image: regclasssend/regtypesend/
// regproceduresend/regprocsend/regrolesend/regcollationsend are ALL oidsend
// upstream (regproc.c) — pq_sendint32 over the OID. TEXT-mode pgoutput instead
// serializes a reg* value through its typoutput (OidOutputFunctionCall,
// proto.c:848), and regclassout/regprocout/... convert the OID to a NAME
// (regproc.c:940). The nil-renderer sub-test below pins the numeric fallback
// (the only correct text when no catalog/name resolution is available); the
// renderer sub-test pins the name rendering a walsender with a catalog emits.
func TestPgoDecodeRegFamilyMatchesPGNativeLayout(t *testing.T) {
	// Synthetic closure standing in for executor.RegOutRenderer — internal/wal
	// cannot import the executor.
	regOut := func(typeName string, oid uint32) string {
		// Mirror executor.RegOut's contract so the dangling/InvalidOid text is
		// the same the real walsender renderer produces.
		if oid == 0 {
			return "-"
		}
		names := map[uint32]string{
			1259: "pg_class", // pg_class
			2206: "regtype",  // regtype itself
			2202: "regprocedure",
			1289: "regproc",
			4096: "regrole", // regrole itself
			4191: "regcollation",
		}
		if n, ok := names[oid]; ok {
			return n
		}
		return strconv.FormatUint(uint64(oid), 10)
	}

	for _, tc := range []struct {
		name     string
		typeName string
		oid      uint32
		regOut   func(string, uint32) string
		want     string
	}{
		// Nil renderer (no catalog at hand): every member — including cid/oid,
		// which have no name form at all — decodes to the bare unsigned OID.
		{name: "regclass nil numeric", typeName: "regclass", oid: 1259, want: "1259"},
		{name: "regtype nil numeric", typeName: "regtype", oid: 2206, want: "2206"},
		{name: "regprocedure nil numeric", typeName: "regprocedure", oid: 2202, want: "2202"},
		{name: "regproc nil numeric", typeName: "regproc", oid: 1289, want: "1289"},
		{name: "regrole nil numeric", typeName: "regrole", oid: 4096, want: "4096"},
		{name: "regcollation nil numeric", typeName: "regcollation", oid: 4191, want: "4191"},
		{name: "cid numeric", typeName: "cid", oid: 5677, regOut: regOut, want: "5677"},
		{name: "oid numeric", typeName: "oid", oid: 1007, regOut: regOut, want: "1007"},
		// Renderer present: reg* values become NAMES; oid/cid stay numeric (no
		// name form — regOut is never consulted for them).
		{name: "regclass renders name", typeName: "regclass", oid: 1259, regOut: regOut, want: "pg_class"},
		{name: "regtype renders name", typeName: "regtype", oid: 2206, regOut: regOut, want: "regtype"},
		{name: "regprocedure renders name", typeName: "regprocedure", oid: 2202, regOut: regOut, want: "regprocedure"},
		{name: "regproc renders name", typeName: "regproc", oid: 1289, regOut: regOut, want: "regproc"},
		{name: "regrole renders name", typeName: "regrole", oid: 4096, regOut: regOut, want: "regrole"},
		{name: "regcollation renders name", typeName: "regcollation", oid: 4191, regOut: regOut, want: "regcollation"},
		// OID 0 (InvalidOid) and the full unsigned range are regOut's
		// unresolvable/dangling contract — the renderer's own fallbacks ("-" for
		// InvalidOid, matching regclassout; numeric for a dangling OID), exercised
		// even with regOut non-nil.
		{name: "regclass InvalidOid dash via renderer", typeName: "regclass", oid: 0, regOut: regOut, want: "-"},
		{name: "regtype full range numeric via renderer", typeName: "regtype", oid: 0xFFFFFFFF, regOut: regOut, want: "4294967295"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typ := catalog.Type{Name: tc.typeName}
			raw := []byte{
				byte(tc.oid),
				byte(tc.oid >> 8),
				byte(tc.oid >> 16),
				byte(tc.oid >> 24),
			}
			got, n, err := pgoDecodePhysicalValue(typ, raw, tc.regOut)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue(%s): %v", tc.typeName, err)
			}
			if n != 4 {
				t.Fatalf("%s: consumed %d bytes, want 4 (4-byte OID)", tc.typeName, n)
			}
			if string(got) != tc.want {
				t.Errorf("%s: decoded %q, want %q", tc.typeName, got, tc.want)
			}
		})
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
