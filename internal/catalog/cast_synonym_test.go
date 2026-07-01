package catalog

import "testing"

// TestDropCastResolvesTypeNameSynonyms guards the loop #48 deferral: real
// PG's pg_cast.castsource/casttarget are OIDs, so CREATE CAST (float4 AS
// text) and DROP CAST (real AS text) name the same cast ("real" and
// "float4" both resolve to OIDFloat4). Before this fix RegisterCast/
// DropCast/CastByTypes keyed the registry on the raw lowercased parsed
// spelling, so a DROP CAST using a different-but-equivalent type name
// synonym than the CREATE silently failed to find the cast.
func TestDropCastResolvesTypeNameSynonyms(t *testing.T) {
	c := NewInMemory()
	c.RegisterCast("float4", "text", "e", "f", 0)

	if cs := c.CastByTypes("real", "text"); cs == nil {
		t.Fatal("CastByTypes(\"real\", \"text\") = nil, want the float4->text cast (real is a float4 synonym)")
	}

	if !c.DropCast("real", "text") {
		t.Fatal("DropCast(\"real\", \"text\") = false, want true (real is a float4 synonym)")
	}
	if cs := c.CastByTypes("float4", "text"); cs != nil {
		t.Fatalf("cast still present after DropCast via synonym: %+v", cs)
	}
}

// TestDropCastResolvesMultiWordSynonyms exercises the multi-word synonym
// forms ("integer" / "double precision") alongside their single-word
// canonical spellings ("int4" / "float8").
func TestDropCastResolvesMultiWordSynonyms(t *testing.T) {
	c := NewInMemory()
	c.RegisterCast("int4", "double precision", "e", "b", 0)

	if cs := c.CastByTypes("integer", "float8"); cs == nil {
		t.Fatal("CastByTypes(\"integer\", \"float8\") = nil, want the int4->double precision cast")
	}
	if !c.DropCast("integer", "float8") {
		t.Fatal("DropCast(\"integer\", \"float8\") = false, want true")
	}
}

// TestRegisterCastIdempotentAcrossSynonyms verifies re-registering a cast
// under a synonym spelling refreshes the existing entry (same OID) rather
// than creating a duplicate row, matching RegisterCast's documented
// idempotent-on-(source,target) contract.
func TestRegisterCastIdempotentAcrossSynonyms(t *testing.T) {
	c := NewInMemory()
	first := c.RegisterCast("float4", "text", "e", "b", 0)
	second := c.RegisterCast("real", "text", "a", "b", 0)

	if second.OID != first.OID {
		t.Fatalf("re-registering via synonym allocated a new OID: first=%d second=%d", first.OID, second.OID)
	}
	if got := len(c.ListCasts()); got != 1 {
		t.Fatalf("ListCasts() len = %d, want 1 (synonym re-register must not duplicate)", got)
	}
	if second.Context != "a" {
		t.Errorf("Context after re-register = %q, want %q (refreshed)", second.Context, "a")
	}
}

// TestCastByTypesDistinctForUnrelatedTypes is a control case ensuring the
// OID-keyed lookup doesn't over-match: unrelated base types must not
// collide.
func TestCastByTypesDistinctForUnrelatedTypes(t *testing.T) {
	c := NewInMemory()
	c.RegisterCast("float4", "text", "e", "b", 0)

	if cs := c.CastByTypes("int4", "text"); cs != nil {
		t.Fatalf("CastByTypes(\"int4\", \"text\") unexpectedly found a cast: %+v", cs)
	}
	if c.DropCast("int4", "text") {
		t.Fatal("DropCast(\"int4\", \"text\") = true, want false (no such cast)")
	}
}

// TestCastByTypesDistinctForUnknownUserDefinedTypes guards against a
// collision the OID-keyed lookup could otherwise introduce: TypeNameToOID
// falls back to OIDText for any name it doesn't recognize (domains, enums,
// composite types aren't in its builtin synonym table), so naively keying on
// its result would bucket every unrelated user-defined-type cast into the
// same OIDText entry. Two distinct user-defined-type casts must stay
// distinct entries.
func TestCastByTypesDistinctForUnknownUserDefinedTypes(t *testing.T) {
	c := NewInMemory()
	c.RegisterCast("my_enum", "int4", "e", "f", 0)
	c.RegisterCast("my_domain", "int4", "e", "f", 0)

	if got := len(c.ListCasts()); got != 2 {
		t.Fatalf("ListCasts() len = %d, want 2 (distinct user-defined-type casts must not collide)", got)
	}
	if cs := c.CastByTypes("my_domain", "int4"); cs == nil {
		t.Fatal("CastByTypes(\"my_domain\", \"int4\") = nil, want the my_domain->int4 cast")
	}
	if !c.DropCast("my_enum", "int4") {
		t.Fatal("DropCast(\"my_enum\", \"int4\") = false, want true")
	}
	if cs := c.CastByTypes("my_domain", "int4"); cs == nil {
		t.Fatal("dropping the my_enum cast must not remove the unrelated my_domain cast")
	}
}
