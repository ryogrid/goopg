package initdb

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestPgClassRelaclReloptionsEncodedAsBinaryArrayType pins the
// bootstrap pg_class encoding for M0106-0010: relacl and reloptions
// must be emitted as 16-byte PG-native ArrayType blobs (not text
// varlena "{}"), so that vanilla PG's deconstruct_array does not
// trip the ARR_ELEMTYPE assertion when a backend reads cached
// pg_class tuples.
//
// The test re-encodes a synthetic nailed relation through the same
// path bootstrapPgClassTuples uses (pgClassColDefs + pgClassRow +
// executor.EncodeRowPG) and locates the relacl/reloptions slots by
// summing the aligned column sizes up to each varlena column. Any
// alignment / type / encoding regression that breaks the PG read
// path is caught here without needing the full E2E harness.
func TestPgClassRelaclReloptionsEncodedAsBinaryArrayType(t *testing.T) {
	cols := pgClassColDefs()
	rel := nailedRel{
		OID:      catalog.RelationRelationId,
		RelName:  "pg_class",
		RelType:  catalog.RelationRelationId,
		RelKind:  'r',
		RelNatts: int16(len(cols)),
		IsShared: false,
	}
	row := pgClassRow(rel)

	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}

	// Fixed-size prefix ends at relminmxid (offset 140, 4 bytes →
	// next field starts at 144). Walk the columns to derive the
	// runtime offset of relacl rather than hard-coding 144, so that
	// any future alignment change surfaces as an explicit failure.
	relaclIdx := indexOf(cols, "relacl")
	reloptionsIdx := indexOf(cols, "reloptions")
	if relaclIdx < 0 || reloptionsIdx < 0 {
		t.Fatalf("relacl/reloptions not found in pgClassColDefs")
	}
	relaclOff, reloptionsOff := offsetsForVarlenaColumns(t, cols, relaclIdx, reloptionsIdx)

	if got := payload[relaclOff:][:16]; !isEmptyArrayType(got, 1033) {
		t.Fatalf("relacl @offset %d: not a valid empty aclitem[] ArrayType: % x", relaclOff, got)
	}
	if got := payload[reloptionsOff:][:16]; !isEmptyArrayType(got, 25) {
		t.Fatalf("reloptions @offset %d: not a valid empty text[] ArrayType: % x", reloptionsOff, got)
	}
}

func indexOf(cols []catalog.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// offsetsForVarlenaColumns walks pgClassColDefs through executor's
// EncodeRowPG alignment rules and returns the runtime byte offsets of
// the two named varlena columns. Mirrors encodeRowPG's per-column loop
// (alignment then encoded-size advance) for fixed-size types only,
// stopping just before each requested column.
func offsetsForVarlenaColumns(t *testing.T, cols []catalog.Column, relaclIdx, reloptionsIdx int) (int, int) {
	t.Helper()
	off := 0
	want := []int{relaclIdx, reloptionsIdx}
	want0 := -1
	want1 := -1
	for i, c := range cols {
		off = alignTo(off, runtimeAlign(c))
		if i == want[0] {
			want0 = off
		}
		if i == want[1] {
			want1 = off
		}
		off += fixedSizeForCatalogType(c.Type.Name)
	}
	if want0 < 0 || want1 < 0 {
		t.Fatalf("could not locate offsets (relacl=%d, reloptions=%d)", want0, want1)
	}
	return want0, want1
}

func alignTo(off, align int) int {
	if align <= 1 {
		return off
	}
	return (off + align - 1) &^ (align - 1)
}

// runtimeAlign mirrors physicalPGTypeAlign (executor pkg) using the
// type names actually present in pgClassColDefs. We re-derive the
// rules locally so this test does not depend on un-exported helpers.
func runtimeAlign(c catalog.Column) int {
	switch c.Type.Name {
	case "bool", "char":
		return 1
	case "int2":
		return 2
	case "int4", "oid", "float4", "date", "xid":
		return 4
	case "int8", "float8", "timestamp", "timestamptz":
		return 8
	case "name":
		return 1
	case "aclitem[]", "_aclitem", "text[]", "_text", "pg_node_tree":
		return 4
	}
	return 4
}

// fixedSizeForCatalogType returns the encoded byte length for a
// fixed-size pg_class column. Only fixed-size types appear before
// relacl; for varlena columns we do not need to know the size
// (the test stops at the first varlena column).
func fixedSizeForCatalogType(name string) int {
	switch name {
	case "bool", "char":
		return 1
	case "int2":
		return 2
	case "int4", "oid", "float4", "xid":
		return 4
	case "int8", "float8":
		return 8
	case "name":
		return 64
	case "aclitem[]", "_aclitem", "text[]", "_text":
		return 16
	}
	return 0
}

func isEmptyArrayType(b []byte, wantElemType uint32) bool {
	if len(b) != 16 {
		return false
	}
	hdr := binary.LittleEndian.Uint32(b[0:4])
	if hdr != uint32(16)<<2 {
		return false
	}
	if binary.LittleEndian.Uint32(b[4:8]) != 0 {
		return false
	}
	if binary.LittleEndian.Uint32(b[8:12]) != 0 {
		return false
	}
	return binary.LittleEndian.Uint32(b[12:16]) == wantElemType
}
