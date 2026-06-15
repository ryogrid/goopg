package executor

// Tests for coerceUnnestElem (DU-002 slice 45). expandArrayDatum returns every
// array element as a text KindString; for non-text element types that string
// derives a DIFFERENT datumKey than the same value held by a catalog column of
// that type, so a hash/merge join such as pg_dump's getTableAttrs
//
//	unnest('{16403}'::oid[]) AS src(tbloid) JOIN pg_attribute a
//	  ON src.tbloid = a.attrelid
//
// matched nothing. coerceUnnestElem casts the element to its declared type so
// the join key lines up with how scalar values of that type are produced
// elsewhere (catalog oid columns are KindInt via NewIntDatum).

import "testing"

func TestCoerceUnnestElem_JoinKeyParity(t *testing.T) {
	// An oid catalog column (pg_attribute.attrelid) is stored as KindInt.
	catalogKey := datumKey(NewIntDatum(16403))

	cases := []struct {
		name     string
		typeName string
	}{
		{"oid", "oid"},
		{"int4", "int4"},
		{"int8", "int8"},
		{"integer", "integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			elem := NewStringDatum("16403") // as produced by expandArrayDatum
			if datumKey(elem) == catalogKey {
				t.Fatalf("precondition failed: raw string already keys equal to the "+
					"catalog int value for %s — coercion would be a no-op", tc.typeName)
			}
			got := coerceUnnestElem(elem, tc.typeName)
			if k := datumKey(got); k != catalogKey {
				t.Fatalf("coerceUnnestElem(%q, %q) key=%q, want catalog key %q (kind=%d)",
					"16403", tc.typeName, k, catalogKey, got.Kind)
			}
		})
	}
}

func TestCoerceUnnestElem_TextUnchanged(t *testing.T) {
	// Text-family elements stay strings: both join sides are already strings,
	// and casting must not perturb a text value.
	for _, typeName := range []string{"text", "name", "varchar", "bpchar"} {
		elem := NewStringDatum("public")
		got := coerceUnnestElem(elem, typeName)
		if got.Kind != KindString || got.StringValue() != "public" {
			t.Fatalf("coerceUnnestElem(%q, %q) = kind %d/%q, want unchanged string",
				"public", typeName, got.Kind, got.StringValue())
		}
	}
}

func TestCoerceUnnestElem_NullPassthrough(t *testing.T) {
	if got := coerceUnnestElem(NullDatum, "oid"); !got.IsNull() {
		t.Fatalf("coerceUnnestElem(NULL, oid) = %v, want NULL", got)
	}
}

func TestCoerceUnnestElem_BadValueFallsBack(t *testing.T) {
	// A non-numeric element targeted at a numeric type must fall back to the raw
	// element rather than abort the scan.
	elem := NewStringDatum("not-a-number")
	got := coerceUnnestElem(elem, "oid")
	if got.Kind != KindString || got.StringValue() != "not-a-number" {
		t.Fatalf("coerceUnnestElem bad value = kind %d/%q, want raw string fallback",
			got.Kind, got.StringValue())
	}
}
