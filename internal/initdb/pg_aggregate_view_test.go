package initdb

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAggBuiltinFuncNameInvalidOidRendersDash mirrors regprocout's
// InvalidOid handling: a moving-aggregate column absent for a plain
// (non-window) aggregate is 0 in the BKI data and must render "-", not "0".
func TestAggBuiltinFuncNameInvalidOidRendersDash(t *testing.T) {
	if got := aggBuiltinFuncName(0); got != "-" {
		t.Errorf("aggBuiltinFuncName(0) = %q, want \"-\"", got)
	}
}

// TestAggBuiltinFuncNameResolvesKnownOID spot-checks a handful of
// aggtransfn/aggfinalfn/aggcombinefn OIDs from avg(int8) (aggfnoid 2100)
// against their known pg_proc.dat names.
func TestAggBuiltinFuncNameResolvesKnownOID(t *testing.T) {
	cases := []struct {
		oid  uint32
		want string
	}{
		{2746, "int8_avg_accum"},
		{3389, "numeric_poly_avg"},
		{2785, "int8_avg_combine"},
		{177, "int4pl"},
	}
	for _, tc := range cases {
		if got := aggBuiltinFuncName(tc.oid); got != tc.want {
			t.Errorf("aggBuiltinFuncName(%d) = %q, want %q", tc.oid, got, tc.want)
		}
	}
}

// TestPgAggregateBKIRegprocColumnsAllResolveToNames guards against the
// aggBuiltinFuncName defensive numeric-fallback ever firing for a real BKI
// row: every non-zero transfn/finalfn/... OID referenced by the 161-row
// pg_aggregate.dat seed must resolve through pgProcAllEntries (both are
// generated from the same PG18 catalog .dat files). A bare numeric string
// here would mean regprocout-style output regressed to raw OIDs.
func TestPgAggregateBKIRegprocColumnsAllResolveToNames(t *testing.T) {
	for _, e := range pgAggregateInitialEntries() {
		for _, col := range []struct {
			name string
			oid  uint32
		}{
			{"aggtransfn", e.AggTransFn},
			{"aggfinalfn", e.AggFinalFn},
			{"aggcombinefn", e.AggCombineFn},
			{"aggserialfn", e.AggSerialFn},
			{"aggdeserialfn", e.AggDeserialFn},
			{"aggmtransfn", e.AggMTransFn},
			{"aggminvtransfn", e.AggMInvTransFn},
			{"aggmfinalfn", e.AggMFinalFn},
		} {
			if col.oid == 0 {
				continue
			}
			got := aggBuiltinFuncName(col.oid)
			if _, err := strconv.ParseUint(got, 10, 32); err == nil {
				t.Errorf("aggfnoid=%d %s: aggBuiltinFuncName(%d) = %q (unresolved, fell back to numeric)", e.AggFnOID, col.name, col.oid, got)
			}
		}
	}
}

// TestRegisterPgAggregateViewRendersBuiltinFuncNames is the end-to-end
// proof: pg_catalog.pg_aggregate's VirtualRows output for a built-in (BKI)
// aggregate row now carries function NAMES in the aggtransfn/aggfinalfn/
// aggcombinefn columns, matching real PostgreSQL's regprocout rendering on
// a direct `SELECT aggtransfn, aggfinalfn, aggcombinefn FROM pg_aggregate`
// -- not raw OID numbers. DU-002 slice 405 resume point (b).
func TestRegisterPgAggregateViewRendersBuiltinFuncNames(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgAggregateView(cat); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_aggregate"})
	if !ok {
		t.Fatal("pg_aggregate not registered")
	}
	rows := tbl.VirtualRows()
	// avg(int8): aggfnoid=2100, aggtransfn=2746 (int8_avg_accum),
	// aggfinalfn=3389 (numeric_poly_avg), aggcombinefn=2785 (int8_avg_combine).
	var found []string
	for _, r := range rows {
		if r[0] == "2100" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("avg(int8) row (aggfnoid=2100) not found in pg_aggregate VirtualRows")
	}
	want := map[int]string{3: "int8_avg_accum", 4: "numeric_poly_avg", 5: "int8_avg_combine"}
	for idx, name := range want {
		if found[idx] != name {
			t.Errorf("aggfnoid=2100 column %d = %q, want %q", idx, found[idx], name)
		}
	}
}
