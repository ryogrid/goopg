package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateTypeDomainRecordsNamespaceOID guards deferral-ledger row 1355
// (slice A): the four user-type registries (enum / composite / range / domain)
// now carry NamespaceOID (pg_type typnamespace), populated at DDL from the
// CREATE statement's schema using execCreateAggregate's
// schema-with-public-fallback resolution pattern. Slice B (regtype rendering,
// format_type_extended's typeform->typnamespace read) consumes it later.
func TestCreateTypeDomainRecordsNamespaceOID(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	// A non-public schema for the qualified CREATEs.
	im.RegisterSchema("myschema")
	wantNS := im.SchemaOID("myschema")
	if wantNS == 0 || wantNS == catalog.PublicNamespaceOID {
		t.Fatalf("SchemaOID(myschema) = %d, want non-zero distinct from public %d", wantNS, catalog.PublicNamespaceOID)
	}

	// Schema-qualified creations must record that schema's OID.
	if err := runDDL(t, ctx, `CREATE TYPE myschema.mood AS ENUM ('sad', 'ok', 'happy')`); err != nil {
		t.Fatalf("CREATE TYPE ... AS ENUM: %v", err)
	}
	et, found := im.LookupEnum("mood")
	if !found {
		t.Fatal("enum not registered after CREATE TYPE ... AS ENUM")
	}
	if et.NamespaceOID != wantNS {
		t.Errorf("enum NamespaceOID = %d, want %d", et.NamespaceOID, wantNS)
	}

	if err := runDDL(t, ctx, `CREATE TYPE myschema.comp AS (a int, b text)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS (composite): %v", err)
	}
	if ct := im.LookupCompositeType("comp"); ct == nil {
		t.Fatal("composite not registered after CREATE TYPE ... AS (composite)")
	} else if ct.NamespaceOID != wantNS {
		t.Errorf("composite NamespaceOID = %d, want %d", ct.NamespaceOID, wantNS)
	}

	if err := runDDL(t, ctx, `CREATE TYPE myschema.span AS RANGE (subtype = int4)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS RANGE: %v", err)
	}
	rt, found := im.LookupRangeType("span")
	if !found {
		t.Fatal("range not registered after CREATE TYPE ... AS RANGE")
	}
	if rt.NamespaceOID != wantNS {
		t.Errorf("range NamespaceOID = %d, want %d", rt.NamespaceOID, wantNS)
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN myschema.positive_int AS int CHECK (VALUE > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("positive_int")
	if !found {
		t.Fatal("domain not registered after CREATE DOMAIN")
	}
	if d.NamespaceOID != wantNS {
		t.Errorf("domain NamespaceOID = %d, want %d", d.NamespaceOID, wantNS)
	}

	// Backward-compat: an unqualified CREATE still records PublicNamespaceOID
	// (the schema-with-public-fallback default).
	if err := runDDL(t, ctx, `CREATE TYPE pubmood AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE (public default): %v", err)
	}
	pet, found := im.LookupEnum("pubmood")
	if !found {
		t.Fatal("public-default enum not registered")
	}
	if pet.NamespaceOID != catalog.PublicNamespaceOID {
		t.Errorf("public-default enum NamespaceOID = %d, want %d", pet.NamespaceOID, catalog.PublicNamespaceOID)
	}
}
