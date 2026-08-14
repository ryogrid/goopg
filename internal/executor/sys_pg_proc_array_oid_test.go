package executor

// sys_pg_proc_array_oid_test.go — M0119-0006 (deferral row 1364): non-char
// array-typed CREATE FUNCTION args/returns stay ArgTypeOIDs[i]==0 at capture
// (only the ambiguous `char` family is captured there) and resolve to the ARRAY
// OID via the fixed TypeNameToOID `[]` arms when buildPGProcRow emits
// proargtypes/prorettype. The live initdb pg_proc VIEW was already array-correct
// (initdb/pg_proc_view.go:typeNameToOIDStr), so this test MUST exercise the
// executor heap-row emitter (buildPGProcRow), not `SELECT … FROM pg_proc`.
//
// PG oracle (PG 18.3, immutable pg_type OIDs): _int4=1007, _date=1182 —
// verify: SELECT 'int4[]'::regtype::oid, 'date[]'::regtype::oid (1007, 1182).

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestBuildPGProcRowArrayOIDs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name       string
		ddl        string
		wantArgOID uint32
		wantRetOID uint32
	}{
		{"g_iarr", `CREATE FUNCTION g_iarr(int4[]) RETURNS int4[] LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDArrayInt4, catalog.OIDArrayInt4},
		{"g_darr", `CREATE FUNCTION g_darr(date[]) RETURNS date[] LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDArrayDate, catalog.OIDArrayDate},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.ddl); err != nil {
			t.Fatalf("create function %s: %v", tc.name, err)
		}
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		r := cands[0]
		// Non-char arrays are NOT captured at CREATE (charTypeOID returns 0 for
		// them) — the TypeNameToOID fallback below is what resolves the array OID.
		if len(r.ArgTypeOIDs) != 1 || r.ArgTypeOIDs[0] != 0 {
			t.Errorf("%s: ArgTypeOIDs = %v, want [0] (non-char array stays 0)", tc.name, r.ArgTypeOIDs)
		}
		if r.ReturnTypeOID != 0 {
			t.Errorf("%s: ReturnTypeOID = %d, want 0 (non-char array stays 0)", tc.name, r.ReturnTypeOID)
		}

		row := buildPGProcRow(ctx.Catalog, r)
		// Column 19 (index 18): prorettype — must be the ARRAY OID, not the
		// element OID (23/1082) and not the OIDText(25) fallback.
		if got := uint32(row[18].Int); got != tc.wantRetOID {
			t.Errorf("%s: buildPGProcRow prorettype = %d, want %d", tc.name, got, tc.wantRetOID)
		}
		// Column 20 (index 19): proargtypes oidvector — {ARRAY OID}.
		if got := row[19].BytesValue(); !bytes.Equal(got, pgProcOidVectorBytes([]uint32{tc.wantArgOID})) {
			t.Errorf("%s: buildPGProcRow proargtypes = %x, want { %d }", tc.name, got, tc.wantArgOID)
		}
	}
}
