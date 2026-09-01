package executor

// review/260831-2 ES-8 — the regtype input half dropped a schema qualifier.
//
// regtypein parses the name and resolves it through LookupTypeName, which
// raises 3F000 for a schema that does not exist and then searches THAT schema
// only. goopg ignored the qualifier entirely, so on the PG 18.3 oracle vs goopg
// (values inserted into a `regtype` column, read back as `::oid`):
//
//	'pg_catalog.int4'    PG 23      goopg 23
//	'public.int4'        PG error   goopg 23
//	'nosuchschema.int4'  PG error   goopg 23
//	'nosuchschema.ct'    PG error   goopg <the OID of public.ct>
//
// The user-type half stays bare-name because goopg's catalog has no per-schema
// type namespace at all; what is pinned here is the part that is decidable.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestRegTypeInputHonoursSchemaQualifier(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TYPE ct AS ENUM ('a')`); err != nil {
		t.Fatalf("CREATE TYPE ct: %v", err)
	}
	ctOID, ok := userTypeOIDForName(ctx.Catalog, "ct")
	if !ok {
		t.Fatal("ct not registered")
	}

	for _, tc := range []struct {
		in      string
		wantOID uint32
		wantErr string // error code, "" when the input must resolve
	}{
		{in: "int4", wantOID: catalog.OIDInt4},
		{in: "pg_catalog.int4", wantOID: catalog.OIDInt4},
		{in: "public.int4", wantErr: "42704"},
		{in: "nosuchschema.int4", wantErr: "3F000"},
		{in: "ct", wantOID: ctOID},
		{in: "public.ct", wantOID: ctOID},
		{in: "nosuchschema.ct", wantErr: "3F000"},
		{in: "pg_catalog.ct", wantErr: "42704"},
	} {
		got, err := regIdentifierInput(NewStringDatum(tc.in), "regtype", ctx, 0)
		if tc.wantErr != "" {
			ee, isExec := err.(*ExecError)
			if !isExec {
				t.Errorf("%q: got (%v, %v), want error %s", tc.in, got, err, tc.wantErr)
				continue
			}
			if ee.Code != tc.wantErr {
				t.Errorf("%q: error code %s (%s), want %s", tc.in, ee.Code, ee.Message, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got.Kind != KindInt || uint32(got.Int) != tc.wantOID {
			t.Errorf("%q = %v, want OID %d", tc.in, got, tc.wantOID)
		}
	}
}
