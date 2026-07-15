package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// conversionByName is a ListUserConversions-backed lookup helper — the
// catalog exposes no ConversionAttrsByName analog to CollationAttrsByName,
// so tests scan the (small) registry directly.
func conversionByName(im *catalog.InMemory, name string) (*catalog.UserConversion, bool) {
	for _, uc := range im.ListUserConversions() {
		if strings.EqualFold(uc.Name, name) {
			return uc, true
		}
	}
	return nil, false
}

// TestAlterConversionRenameOwner guards the M0122-0007 4e follow-up: ALTER
// CONVERSION RENAME TO / OWNER TO were entirely unhandled (no parser branch
// existed for the "conversion" keyword inside parseAlter at all), blocking
// the DU-002 round-trip probe on `ALTER CONVERSION public.aliasconv OWNER TO
// postgres;`. Mirrors TestAlterCollationRenameOwnerRefresh.
func TestAlterConversionRenameOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE CONVERSION myconv FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8`); err != nil {
		t.Fatalf("CREATE CONVERSION: %v", err)
	}

	// RENAME TO.
	if err := runDDL(t, ctx, `ALTER CONVERSION myconv RENAME TO renamedconv`); err != nil {
		t.Fatalf("ALTER CONVERSION RENAME TO: %v", err)
	}
	if _, found := conversionByName(im, "myconv"); found {
		t.Error("old conversion name still resolves after RENAME TO")
	}
	uc, found := conversionByName(im, "renamedconv")
	if !found {
		t.Fatal("renamed conversion not found via ListUserConversions")
	}
	if uc.Owner != 10 {
		t.Errorf("Owner before OWNER TO = %d, want 10 (bootstrap superuser default)", uc.Owner)
	}

	// OWNER TO a registered role — the exact form the DU-002 probe needs
	// (pg_dump's dumpConversion emits this via the generic archive-owner
	// mechanism).
	im.RegisterRole("newowner")
	wantOID, found := im.RoleOID("newowner")
	if !found {
		t.Fatal("RoleOID(\"newowner\") not found after RegisterRole")
	}
	if err := runDDL(t, ctx, `ALTER CONVERSION renamedconv OWNER TO newowner`); err != nil {
		t.Fatalf("ALTER CONVERSION OWNER TO: %v", err)
	}
	uc, found = conversionByName(im, "renamedconv")
	if !found || uc.Owner != wantOID {
		t.Errorf("Owner after OWNER TO = %+v, want owner OID %d", uc, wantOID)
	}

	// IF EXISTS on an unknown conversion is a no-op.
	if err := runDDL(t, ctx, `ALTER CONVERSION IF EXISTS nosuchconv RENAME TO x`); err != nil {
		t.Fatalf("ALTER CONVERSION IF EXISTS on unknown conversion should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown conversion raises 42704.
	err := runDDL(t, ctx, `ALTER CONVERSION nosuchconv RENAME TO x`)
	if err == nil {
		t.Fatal("ALTER CONVERSION on unknown conversion without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	if !strings.Contains(err.Error(), "nosuchconv") {
		t.Errorf("err = %v, want it to name the missing conversion", err)
	}
}

// TestAlterConversionSetSchema guards ALTER CONVERSION name SET SCHEMA
// newschema. Mirrors TestAlterCollationSetSchema.
func TestAlterConversionSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA otherschema`); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE CONVERSION setschemaconv FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8`); err != nil {
		t.Fatalf("CREATE CONVERSION: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER CONVERSION setschemaconv SET SCHEMA otherschema`); err != nil {
		t.Fatalf("ALTER CONVERSION SET SCHEMA: %v", err)
	}
	uc, found := conversionByName(im, "setschemaconv")
	if !found {
		t.Fatal("conversion not found via ListUserConversions after SET SCHEMA")
	}
	wantNsOID := im.SchemaOID("otherschema")
	if wantNsOID == 0 {
		t.Fatal("SchemaOID(\"otherschema\") = 0, want a real namespace OID")
	}
	if uc.NamespaceOID != wantNsOID {
		t.Errorf("NamespaceOID after SET SCHEMA = %d, want %d (otherschema)", uc.NamespaceOID, wantNsOID)
	}

	// IF EXISTS on an unknown conversion is a no-op.
	if err := runDDL(t, ctx, `ALTER CONVERSION IF EXISTS nosuchconv SET SCHEMA otherschema`); err != nil {
		t.Fatalf("ALTER CONVERSION IF EXISTS SET SCHEMA on unknown conversion should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown conversion raises 42704.
	err := runDDL(t, ctx, `ALTER CONVERSION nosuchconv SET SCHEMA otherschema`)
	if err == nil {
		t.Fatal("ALTER CONVERSION SET SCHEMA on unknown conversion without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
