package initdb

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestPgClassRowSharedReltablespaceIsGlobalTablespaceOID pins the
// M0106-0010 Step 3cr fix: pg_class.reltablespace for shared catalogs
// (relisshared=true) must be 1664 (GLOBALTABLESPACE_OID) so that PG's
// RelationInitPhysicalAddr (relcache.c:1347-1354) resolves the file
// path to `global/<relfilenode>`. Local catalogs continue to store 0.
//
// Without this fix every shared catalog index that PG opened via a
// real RelationBuildDesc (e.g. pg_database_oid_index OID 2672) FATAL'd
// the standby with `could not open file "base/<MyDatabaseId>/2672"`
// because spcOid fell back to MyDatabaseTableSpace.
func TestPgClassRowSharedReltablespaceIsGlobalTablespaceOID(t *testing.T) {
	cols := pgClassColDefs()
	const reltablespaceColIdx = 8 // 0=oid 1=relname 2=relnamespace 3=reltype 4=reloftype 5=relowner 6=relam 7=relfilenode 8=reltablespace
	if cols[reltablespaceColIdx].Name != "reltablespace" {
		t.Fatalf("col index %d expected reltablespace, got %q", reltablespaceColIdx, cols[reltablespaceColIdx].Name)
	}

	tests := []struct {
		name     string
		isShared bool
		want     int64
	}{
		{"shared catalog", true, 1664},
		{"local catalog", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rel := nailedRel{
				OID:      catalog.RelationRelationId,
				RelName:  "pg_class",
				RelType:  catalog.RelationRelationId,
				RelKind:  'r',
				RelNatts: int16(len(cols)),
				IsShared: tc.isShared,
			}
			row := pgClassRow(rel)
			d := row[reltablespaceColIdx]
			if d.Int != tc.want {
				t.Fatalf("reltablespace = %d, want %d", d.Int, tc.want)
			}
		})
	}
}

// TestPgClassRowSharedReltablespaceInEncodedPayload encodes the
// pg_class row for a shared rel via the same path bootstrapPgClassTuples
// uses and asserts byte offset 92 of the on-disk Form_pg_class layout
// contains the 32-bit LE encoding of 1664. This guards against
// alignment/encoder regressions that wouldn't be caught by the Datum-
// level assertion above.
func TestPgClassRowSharedReltablespaceInEncodedPayload(t *testing.T) {
	cols := pgClassColDefs()
	rel := nailedRel{
		OID:      1262, // pg_database
		RelName:  "pg_database",
		RelType:  1248,
		RelKind:  'r',
		RelNatts: int16(len(cols)),
		IsShared: true,
	}
	row := pgClassRow(rel)
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// Form_pg_class.reltablespace is at offset 92 (after oid/relname/
	// relnamespace/reltype/reloftype/relowner/relam/relfilenode).
	if len(payload) < 96 {
		t.Fatalf("payload too short: %d bytes", len(payload))
	}
	got := binary.LittleEndian.Uint32(payload[92:96])
	if got != 1664 {
		t.Fatalf("reltablespace bytes [92:96] = %d, want 1664", got)
	}
}

// TestFlattenRelsPropagatesIsSharedToIndexes pins the M0106-0010 Step
// 3cr fix to flattenRels: each idxSpec produces an indexNailed entry,
// and the IsShared flag must be inherited from the parent heap list.
// Without this, shared-catalog indexes (e.g. pg_database_oid_index =
// 2672) had IsShared=false and were written into the per-database
// pg_class with relisshared=false + reltablespace=0, causing PG to
// look for `base/<MyDatabaseId>/2672` instead of `global/2672`.
func TestFlattenRelsPropagatesIsSharedToIndexes(t *testing.T) {
	// 2672 is the pg_database_oid_index (shared), declared in
	// nailedSharedRels' idxSpec list. Walk nailedSharedRels and verify
	// it landed with IsShared=true.
	const wantOID uint32 = 2672
	var got *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == wantOID {
			got = &nailedSharedRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pg_database_oid_index (OID %d) missing from nailedSharedRels", wantOID)
	}
	if !got.IsShared {
		t.Fatalf("pg_database_oid_index IsShared = false, want true (Step 3cr)")
	}
	if got.RelKind != 'i' {
		t.Fatalf("pg_database_oid_index RelKind = %q, want 'i'", got.RelKind)
	}

	// And every shared index in the flattened list must carry the flag.
	for i := range nailedSharedRels {
		r := nailedSharedRels[i]
		if r.RelKind != 'i' {
			continue
		}
		if !r.IsShared {
			t.Errorf("nailedSharedRels[%d] (OID %d, %s) RelKind='i' has IsShared=false", i, r.OID, r.RelName)
		}
	}
	// Conversely, local indexes must NOT be marked shared.
	for i := range nailedLocalRels {
		r := nailedLocalRels[i]
		if r.RelKind != 'i' {
			continue
		}
		if r.IsShared {
			t.Errorf("nailedLocalRels[%d] (OID %d, %s) RelKind='i' has IsShared=true (must be local)", i, r.OID, r.RelName)
		}
	}
}
