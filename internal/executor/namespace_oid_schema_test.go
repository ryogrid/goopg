package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestNamespaceOIDForSchemaResolvesUserSchema pins the write side of the
// M0110-0003 user-schema table durability fix: namespaceOIDForSchema must stamp
// a user table's pg_class.relnamespace with the schema's real OID (so the
// restart-recovery reverse map can recover the schema name), while system
// schemas keep their fixed OIDs and an unregistered schema falls back to
// pg_catalog (pre-M0110-0003 behaviour).
func TestNamespaceOIDForSchemaResolvesUserSchema(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterSchema("s1")
	s1OID := cat.SchemaOID("s1")
	if s1OID == 0 {
		t.Fatal("RegisterSchema(s1) yielded OID 0")
	}

	cases := []struct {
		name   string
		schema string
		want   uint32
	}{
		{"empty -> public", "", catalog.PublicNamespaceOID},
		{"public", "public", catalog.PublicNamespaceOID},
		{"pg_catalog", "pg_catalog", catalog.PGCatalogNamespaceOID},
		{"registered user schema", "s1", s1OID},
		{"unregistered -> pg_catalog fallback", "nope", catalog.PGCatalogNamespaceOID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceOIDForSchema(cat, tc.schema); got != tc.want {
				t.Errorf("namespaceOIDForSchema(%q) = %d, want %d", tc.schema, got, tc.want)
			}
		})
	}

	// A nil catalog must not panic; system schemas still resolve and a user
	// schema falls back to pg_catalog.
	if got := namespaceOIDForSchema(nil, "public"); got != catalog.PublicNamespaceOID {
		t.Errorf("namespaceOIDForSchema(nil, public) = %d, want %d", got, catalog.PublicNamespaceOID)
	}
	if got := namespaceOIDForSchema(nil, "s1"); got != catalog.PGCatalogNamespaceOID {
		t.Errorf("namespaceOIDForSchema(nil, s1) = %d, want %d (fallback)", got, catalog.PGCatalogNamespaceOID)
	}
}
