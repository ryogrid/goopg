package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestColTypeInfoVarlenaMatchesLive pins colTypeInfo.isVarlena against the
// live catalog.PhysicalTypeIsVarlena for every type the decode path can meet.
//
// The memo exists to stop that function being called per value. The whole
// value of the memo is that it gives the SAME answer, so this test is the
// thing that makes the optimisation safe rather than merely fast.
//
// The tid/money/macaddr rows are the point of the test, not padding.
// PhysicalTypeIsVarlena deliberately reports them as varlena although their
// pg_type typlen is fixed (catalog/physical_align.go:78-84). A "tidier"
// implementation deriving isVarlena from attLen == -1 would disagree on
// exactly those three and would move on-disk offsets for them.
func TestColTypeInfoVarlenaMatchesLive(t *testing.T) {
	types := []catalog.Type{
		{Name: "int4"}, {Name: "int8"}, {Name: "int2"}, {Name: "bool"},
		{Name: "float4"}, {Name: "float8"}, {Name: "oid"}, {Name: "name"},
		{Name: "date"}, {Name: "timestamp"}, {Name: "timestamptz"},
		{Name: "interval"}, {Name: "uuid"}, {Name: "xid"}, {Name: "pg_lsn"},
		{Name: "text"}, {Name: "varchar"}, {Name: "bytea"}, {Name: "numeric"},
		{Name: "json"}, {Name: "jsonb"},
		// char with and without a length modifier: one is fixed-width
		// internal "char", the other is bpchar (varlena). Same name.
		{Name: "char"}, {Name: "char", Args: []int64{10}},
		// Mixed case must lower to the same answer.
		{Name: "INT4"}, {Name: "TeXt"}, {Name: "TIMESTAMPTZ"},
		// The deliberate disagreements with typlen.
		{Name: "tid"}, {Name: "money"}, {Name: "macaddr"}, {Name: "macaddr8"},
		// Arrays: element name plus IsArray.
		{Name: "int4", IsArray: true}, {Name: "text", IsArray: true},
		// An unknown/user type falls to the default arm.
		{Name: "some_user_enum"},
	}
	cols := make([]catalog.Column, len(types))
	for i, ty := range types {
		cols[i] = catalog.Column{Name: "c", Type: ty}
	}

	info := resolveColTypeInfo(cols)
	if len(info) != len(cols) {
		t.Fatalf("resolveColTypeInfo returned %d entries for %d columns", len(info), len(cols))
	}
	for i, ty := range types {
		want := catalog.PhysicalTypeIsVarlena(ty)
		if info[i].isVarlena != want {
			t.Errorf("%s (args=%v array=%v): memo isVarlena=%v, live=%v",
				ty.Name, ty.Args, ty.IsArray, info[i].isVarlena, want)
		}
		// The memo's own `lower` must also still agree, since the decoder
		// indexes both positionally off the same entry.
		if info[i].lower != strings.ToLower(ty.Name) {
			t.Errorf("%s: memo lower=%q want %q", ty.Name, info[i].lower, strings.ToLower(ty.Name))
		}
	}
}

// TestColInfoMatchesDetectsMismatch pins the positional precondition that
// decodeRowIntoInfo depends on: a memo resolved from a DIFFERENT column list
// than the decoder walks would decode column i with column j's descriptor.
// That is a wrong-answer risk, so the detector must actually detect.
func TestColInfoMatchesDetectsMismatch(t *testing.T) {
	a := []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}, {Name: "y", Type: catalog.Type{Name: "text"}}}
	b := []catalog.Column{{Name: "x", Type: catalog.Type{Name: "text"}}, {Name: "y", Type: catalog.Type{Name: "int4"}}}

	if !colInfoMatches(a, resolveColTypeInfo(a)) {
		t.Error("a memo resolved from its own column list must match it")
	}
	if colInfoMatches(a, resolveColTypeInfo(b)) {
		t.Error("a memo resolved from a DIFFERENT list must NOT match — this is the wrong-answer guard")
	}
	if !colInfoMatches(a, nil) {
		t.Error("nil info must be reported as safe: the decoder re-derives")
	}
	if colInfoMatches(a, resolveColTypeInfo(a[:1])) {
		t.Error("a shorter memo must NOT match")
	}
}
