package initdb

import "testing"

// TestNailedLocalRelsContainsPgAggregate guards M0106-0010 step 3w: PG
// standby's `InitPostgres` opens `pg_aggregate` (OID 2600) and FATALs
// with `could not open relation with OID 2600` if no pg_class row exists.
// The fix adds an entry to `nailedLocalRels`; this test rejects silent
// removal that would re-introduce the FATAL.
func TestNailedLocalRelsContainsPgAggregate(t *testing.T) {
	const aggregateOID = 2600

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == aggregateOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_aggregate) — step 3w regression", aggregateOID)
	}
	if got.RelName != "pg_aggregate" {
		t.Fatalf("OID %d: RelName=%q want %q", aggregateOID, got.RelName, "pg_aggregate")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", aggregateOID, got.RelKind)
	}
	// PG18's `Anum_pg_aggregate_aggminitval` = 22, the highest column.
	if got.RelNatts != 22 {
		t.Fatalf("OID %d: RelNatts=%d want 22 (PG18 pg_aggregate column count)", aggregateOID, got.RelNatts)
	}
	if len(got.Attrs) != 22 {
		t.Fatalf("OID %d: len(Attrs)=%d want 22", aggregateOID, len(got.Attrs))
	}
	// Spot-check the first column matches pg_aggregate_d.h
	// (Anum_pg_aggregate_aggfnoid = 1, regproc).
	if got.Attrs[0].Name != "aggfnoid" || got.Attrs[0].TypeOID != 24 || got.Attrs[0].Num != 1 {
		t.Fatalf("OID %d: Attrs[0]=%+v want {aggfnoid regproc 1}", aggregateOID, got.Attrs[0])
	}
}
