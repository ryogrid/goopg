package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
)

// TestInformationSchemaTableRowsMatchCapture asserts the embedded TSV captures
// parse to the exact row counts a fresh PG 18.3 produces, and spot-checks cells
// whose value is load-bearing (the initdb-computed DBMS VERSION, the
// post-UPDATE sql_sizing comment, and the NULL-vs-empty distinction in
// sql_implementation_info).
func TestInformationSchemaTableRowsMatchCapture(t *testing.T) {
	wantCounts := map[string]int{
		"sql_features":             755,
		"sql_sizing":               23,
		"sql_implementation_info":  12,
		"sql_parts":                11,
	}
	for _, tbl := range infoSchemaTables() {
		rows, err := infoSchemaTableRows(tbl)
		if err != nil {
			t.Fatalf("infoSchemaTableRows(%s): %v", tbl.relname, err)
		}
		if len(rows) != wantCounts[tbl.relname] {
			t.Fatalf("%s: got %d rows, want %d", tbl.relname, len(rows), wantCounts[tbl.relname])
		}
	}

	// sql_features row B011: feature_name "Embedded Ada", is_supported "NO",
	// is_verified_by NULL (upstream's COPY omits it).
	features, _ := infoSchemaTableRows(infoSchemaTables()[0])
	if got := features[0]; !stringDatumEq(got[0], "B011") || !stringDatumEq(got[1], "Embedded Ada") ||
		!stringDatumEq(got[4], "NO") || !got[5].IsNull() {
		t.Fatalf("sql_features row 0 = %#v, want B011/Embedded Ada/…/NO/NULL", got)
	}

	// sql_sizing: "MAXIMUM IDENTIFIER LENGTH" has sizing_id 10005, supported_value 63.
	sizing, _ := infoSchemaTableRows(infoSchemaTables()[3])
	found := false
	for _, r := range sizing {
		if stringDatumEq(r[1], "MAXIMUM IDENTIFIER LENGTH") {
			found = true
			if !intDatumEq(r[0], 10005) || !intDatumEq(r[2], 63) {
				t.Fatalf("sql_sizing MAXIMUM IDENTIFIER LENGTH row = %#v", r)
			}
		}
	}
	if !found {
		t.Fatal("sql_sizing: MAXIMUM IDENTIFIER LENGTH row not found")
	}

	// sql_implementation_info: DBMS VERSION is filled by initdb with the
	// PG-version infoversion string; DATA SOURCE NAME distinguishes an empty
	// character_value ("") from a NULL comments.
	impl, _ := infoSchemaTableRows(infoSchemaTables()[1])
	var versionRow, dsnRow executor.Row
	for _, r := range impl {
		switch {
		case stringDatumEq(r[1], "DBMS VERSION"):
			versionRow = r
		case stringDatumEq(r[1], "DATA SOURCE NAME"):
			dsnRow = r
		}
	}
	if versionRow == nil || !stringDatumEq(versionRow[3], "18.03.0000") {
		t.Fatalf("DBMS VERSION row = %#v, want character_value 18.03.0000", versionRow)
	}
	if dsnRow == nil || !stringDatumEq(dsnRow[3], "") || !dsnRow[4].IsNull() {
		t.Fatalf("DATA SOURCE NAME row = %#v, want character_value \"\" and comments NULL", dsnRow)
	}
}

// TestInformationSchemaDataTableRelsStayOutOfInitFile asserts the four data
// tables are wired into the pg_class/pg_attribute heaps and the toast pairs
// WITHOUT entering pg_internal.init (nailedSharedRels / nailedLocalRels), and
// that each carries a real composite RelType.
func TestInformationSchemaDataTableRelsStayOutOfInitFile(t *testing.T) {
	nailed := map[uint32]bool{}
	for _, r := range nailedSharedRels {
		nailed[r.OID] = true
	}
	for _, r := range nailedLocalRels {
		nailed[r.OID] = true
	}

	toastParents := map[uint32]uint32{} // parent OID → toast rel OID
	for _, p := range nailedToastPairs() {
		toastParents[p.Parent] = p.ToastRel
	}

	for _, tbl := range infoSchemaTables() {
		if nailed[tbl.oid] {
			t.Fatalf("%s (OID %d) must not be a nailed rel — it would enter pg_internal.init", tbl.relname, tbl.oid)
		}
		if tbl.reltype == 0 {
			t.Fatalf("%s: RelType must be the composite rowtype, not 0", tbl.relname)
		}
		if _, ok := toastParents[tbl.oid]; !ok {
			t.Fatalf("%s: missing TOAST pair (reltoastrelid must name %d)", tbl.relname, tbl.oid+3)
		}
	}
}

// TestInformationSchemaTableTypesHaveCanonicalRows asserts the composite and
// array pg_type rows resolve, the composite carries a valid typrelid, and the
// array/composite edges point at each other — the invariants a hosted PG's
// lookup_type_cache and get_array_type depend on.
func TestInformationSchemaTableTypesHaveCanonicalRows(t *testing.T) {
	for _, tbl := range infoSchemaTables() {
		arrayOID := tbl.oid + 1
		compositeOID := tbl.reltype // oid + 2

		comp, ok := pgTypeCanonical(compositeOID)
		if !ok || comp.Type != 'c' {
			t.Fatalf("%s: composite type %d missing or not 'c'", tbl.relname, compositeOID)
		}
		if pgTypeRelidOverlay[compositeOID] != tbl.oid {
			t.Fatalf("%s: composite typrelid = %d, want %d", tbl.relname, pgTypeRelidOverlay[compositeOID], tbl.oid)
		}

		arr, ok := pgTypeCanonical(arrayOID)
		if !ok || arr.Type != 'b' {
			t.Fatalf("%s: array type %d missing or not 'b'", tbl.relname, arrayOID)
		}
		elem, parray, _ := pgTypeElemArraySubscriptForOID(arrayOID)
		if elem != int64(compositeOID) || parray != 0 {
			t.Fatalf("%s: array typelem/typarray = %d/%d, want %d/0", tbl.relname, elem, parray, compositeOID)
		}
		celem, cparray, csub := pgTypeElemArraySubscriptForOID(compositeOID)
		if celem != 0 || cparray != int64(arrayOID) || csub != 0 {
			t.Fatalf("%s: composite typelem/typarray/typsubscript = %d/%d/%d, want 0/%d/0", tbl.relname, celem, cparray, csub, arrayOID)
		}
	}
}

func stringDatumEq(d executor.Datum, s string) bool {
	return !d.IsNull() && d.StringValue() == s
}

func intDatumEq(d executor.Datum, v int64) bool {
	return !d.IsNull() && d.Int == v
}
