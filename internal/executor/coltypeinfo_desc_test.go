package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// D-01 (MD-01) agreement tests — 06 §3 MD-01.
//
// The descriptor fields on colTypeInfo are derived through
// catalog.TypeNameToOID -> userTypeAttrsForOID rather than transcribed, so
// what needs pinning is (a) that the derivation actually reaches the shared
// table, (b) that it agrees with the OTHER in-tree answer for the same
// question, and (c) that unknown types fail safe.
//
// (b) matters most. The D-02 audit found four independent transcriptions of
// the same type list already in tree, disagreeing about at least one
// spelling. A test that compares two of them mechanically is the only thing
// that keeps a fifth from being written by hand later.

func tdInfo(t *testing.T, typ catalog.Type) colTypeInfo {
	t.Helper()
	info := resolveColTypeInfo([]catalog.Column{{Name: "c", Type: typ}})
	if len(info) != 1 {
		t.Fatalf("resolveColTypeInfo returned %d entries, want 1", len(info))
	}
	return info[0]
}

// TestColTypeDescriptorMatchesPGTypeDat pins the descriptor for the types
// user CREATE TABLE can produce, against
// postgres/src/include/catalog/pg_type.dat. These are the values PG's
// heap_deform_tuple reads out of a TupleDesc, so a wrong one here would
// mis-shape a packed row rather than merely mis-report it.
func TestColTypeDescriptorMatchesPGTypeDat(t *testing.T) {
	cases := []struct {
		name    string
		typ     catalog.Type
		len     int16
		byVal   bool
		storage byte
	}{
		{"bool", catalog.Type{Name: "bool"}, 1, true, 'p'},
		{"boolean alias", catalog.Type{Name: "boolean"}, 1, true, 'p'},
		{"int2", catalog.Type{Name: "int2"}, 2, true, 'p'},
		{"smallint alias", catalog.Type{Name: "smallint"}, 2, true, 'p'},
		{"int4", catalog.Type{Name: "int4"}, 4, true, 'p'},
		{"integer alias", catalog.Type{Name: "integer"}, 4, true, 'p'},
		{"int8", catalog.Type{Name: "int8"}, 8, true, 'p'},
		{"bigint alias", catalog.Type{Name: "bigint"}, 8, true, 'p'},
		{"float4", catalog.Type{Name: "float4"}, 4, true, 'p'},
		{"float8", catalog.Type{Name: "float8"}, 8, true, 'p'},
		{"oid", catalog.Type{Name: "oid"}, 4, true, 'p'},
		{"text", catalog.Type{Name: "text"}, -1, false, 'x'},
		{"bytea", catalog.Type{Name: "bytea"}, -1, false, 'x'},
		// bpchar/varchar are varlena; the typmod lives in Args and does not
		// change the descriptor.
		{"char(n)", catalog.Type{Name: "char", Args: []int64{25}}, -1, false, 'x'},
		{"varchar(n)", catalog.Type{Name: "varchar", Args: []int64{55}}, -1, false, 'x'},
		// Fixed-width non-integer types, pinned because the first draft of
		// this test wrongly assumed they were unknown: PG's `point` is 16
		// bytes by reference and `money` is 8 bytes BY VALUE on a 64-bit
		// build (FLOAT8PASSBYVAL), and the bridge already answers both
		// correctly.
		{"point", catalog.Type{Name: "point"}, 16, false, 'p'},
		{"money", catalog.Type{Name: "money"}, 8, true, 'p'},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tdInfo(t, tc.typ)
			if got.attLen != tc.len {
				t.Errorf("attLen = %d, want %d", got.attLen, tc.len)
			}
			if got.attByVal != tc.byVal {
				t.Errorf("attByVal = %v, want %v", got.attByVal, tc.byVal)
			}
			if got.attStorage != tc.storage {
				t.Errorf("attStorage = %q, want %q", got.attStorage, tc.storage)
			}
		})
	}
}

// TestColTypeDescriptorArraysAreVarlena pins the array short-circuit.
// catalog.Type carries the ELEMENT name with IsArray beside it, so a naive
// OID lookup would hand back int4's by-value descriptor for an int4[]
// column — a 4-byte by-val descriptor over a varlena payload, which is the
// shape that dereferences inline bytes as a pointer.
func TestColTypeDescriptorArraysAreVarlena(t *testing.T) {
	for _, elem := range []string{"int4", "int8", "text", "bool", "numeric"} {
		t.Run(elem, func(t *testing.T) {
			got := tdInfo(t, catalog.Type{Name: elem, IsArray: true})
			if got.attLen != -1 || got.attByVal {
				t.Fatalf("%s[]: attLen=%d attByVal=%v, want -1/false — every PG "+
					"array type is a varlena whatever its element is",
					elem, got.attLen, got.attByVal)
			}
			if got.attStorage != 'x' {
				t.Errorf("%s[]: attStorage=%q, want 'x'", elem, got.attStorage)
			}
		})
	}
}

// TestColTypeDescriptorUnknownFailsSafe pins the fallback DIRECTION. A type
// name the OID bridge does not recognise must resolve to a varlena
// descriptor, never to a by-value one: a by-value descriptor over
// variable-length bytes makes a reader dereference inline data as a
// pointer, while a varlena descriptor over a fixed-width value merely
// mis-reads it. userTypeAttrsForOID's own comment states this preference;
// this test is what holds it.
func TestColTypeDescriptorUnknownFailsSafe(t *testing.T) {
	for _, name := range []string{"my_custom_type", "definitely_not_a_type", ""} {
		got := tdInfo(t, catalog.Type{Name: name})
		if got.attByVal {
			t.Errorf("%q: attByVal=true — an unknown type must fail safe to a "+
				"varlena descriptor, never to by-value", name)
		}
		if got.attLen != -1 {
			t.Errorf("%q: attLen=%d, want -1", name, got.attLen)
		}
	}
}

// TestColTypeDescriptorAgreesWithAlignTable is the sibling-path check.
//
// `colTypeInfo.align` comes from physicalPGTypeAlign; the descriptor's
// TypAlign comes from userTypeAttrsForOID. Both claim to answer "how is a
// value of this type aligned", from two independent transcriptions of
// pg_type.dat. Where both recognise a type they must agree, or a packed row
// and the on-disk codec will lay the same column out differently.
//
// Types the OID bridge does not recognise are skipped rather than asserted:
// there the descriptor is the deliberate varlena fallback and carries no
// opinion about alignment.
func TestColTypeDescriptorAgreesWithAlignTable(t *testing.T) {
	alignOf := map[byte]int{'c': 1, 's': 2, 'i': 4, 'd': 8}
	names := []string{
		"bool", "boolean", "int2", "smallint", "int4", "integer", "int",
		"int8", "bigint", "float4", "real", "float8", "double precision",
		"oid", "text", "bytea", "varchar", "bpchar", "numeric", "date",
		"time", "timetz", "timestamp", "timestamptz", "uuid", "name",
	}
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			typ := catalog.Type{Name: n}
			if catalog.TypeNameToOID(n) == 0 {
				t.Skipf("%q is not in the OID bridge; descriptor is the "+
					"varlena fallback and asserts nothing about alignment", n)
			}
			ta := colTypeDescriptor(typ)
			want, ok := alignOf[ta.TypAlign]
			if !ok {
				t.Fatalf("descriptor reports TypAlign=%q, not one of c/s/i/d", ta.TypAlign)
			}
			got := tdInfo(t, typ).align
			if got != want {
				t.Errorf("alignment disagreement for %q: physicalPGTypeAlign says %d, "+
					"pg_type descriptor says %d (typalign %q). Two transcriptions of "+
					"pg_type.dat have drifted; fix the table, not this test",
					n, got, want, ta.TypAlign)
			}
		})
	}
}
