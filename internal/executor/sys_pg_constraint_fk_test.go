package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// R126 P0. The pg_constraint FK row is the first system-catalog row goopg
// writes with a NON-NULL varlena ARRAY column, so two things that had never
// been exercised must hold: the int2[] blob must round-trip, and the tuple's
// HEAP_HASVARWIDTH infomask bit must be set.
//
// Both were broken before R126 and neither is visible to a naive round-trip
// test written against goopg alone — see the sub-tests' comments.

func fkTestRow(t *testing.T, conkey, confkey []int16) Row {
	t.Helper()
	fk := catalog.ForeignKey{
		Name: "child_pid_fkey", OID: 20001,
		Columns: []string{"pid"}, RefTable: "parent", RefColumns: []string{"id"},
		OnDelete: parser.FKActionCascade, OnUpdate: parser.FKActionNoAction,
	}
	row, err := buildPGConstraintRowForForeignKey(fk, 16400, 16390, conkey, confkey, nil)
	if err != nil {
		t.Fatalf("buildPGConstraintRowForForeignKey: %v", err)
	}
	return row
}

// TestPGConstraintFKRowArrayRoundTrip pins that conkey/confkey survive an
// encode→decode cycle with their attnums intact.
//
// Mutation test: replacing int2ArrayDatum's encodeArrayValuePGCtx call with a
// plain NewStringDatum("{2,3}") makes this fail with conkey "{}" — because
// encodeValuePGCtx's `case "int2[]"` arm returns emptyArrayTypeBytes(21) for
// any non-KindBytes datum. That silent empty array is what would have made
// keysCovering decline and cost the round its entire purpose.
func TestPGConstraintFKRowArrayRoundTrip(t *testing.T) {
	cols := PGConstraintColumnsPG18()

	for _, tc := range []struct {
		name           string
		conkey, confkey []int16
		wantCon, wantConf string
	}{
		// Single-column is tested alongside multi-column deliberately: a
		// conkey that comes back NULL sets no varlena bit either, so the
		// infomask half of this pin would pass vacuously on a NULL-only case.
		{"single column", []int16{2}, []int16{1}, "{2}", "{1}"},
		{"multi column", []int16{2, 3}, []int16{1, 4}, "{2,3}", "{1,4}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := fkTestRow(t, tc.conkey, tc.confkey)

			data, err := EncodeRowPG(cols, row)
			if err != nil {
				t.Fatalf("EncodeRowPG: %v", err)
			}
			bitmap := NullBitmapPG(row)
			decoded := make(Row, len(cols))
			if err := DecodeRowIntoMctxPGTupleStyled(decoded, cols, data, bitmap, len(cols), nil,
				array.DefaultOutputStyle()); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got := decoded[20].StringValue(); got != tc.wantCon {
				t.Errorf("conkey round-trip = %q, want %q "+
					"(pre-R126 this decoded as varlena TEXT: the scalar switch had no int2[] arm)", got, tc.wantCon)
			}
			if got := decoded[21].StringValue(); got != tc.wantConf {
				t.Errorf("confkey round-trip = %q, want %q", got, tc.wantConf)
			}
		})
	}
}

// TestPGConstraintFKRowHasVarWidthInfomask pins the bit that no goopg-only
// round-trip test can see.
//
// goopg's own encode and decode both branch on Type.IsArray, so they stay
// symmetric under a wrong descriptor and the test above would pass either way.
// What breaks is the ON-DISK infomask: PhysicalTypeIsVarlena switches on
// Name with no IsArray arm (physical_align.go:85-107), so declaring these
// columns {Name:"int2", IsArray:true} reports them FIXED-WIDTH, leaves
// HEAP_HASVARWIDTH unset, and trips PG18's nocachegetattr fast-path walker
// (heaptuple.c:642) on a real standby.
//
// Mutation test: change PGConstraintColumnsPG18's conkey/confkey to
// {Name:"int2", IsArray:true} and this fails while the round-trip above still
// passes — which is exactly why this pin exists separately.
func TestPGConstraintFKRowHasVarWidthInfomask(t *testing.T) {
	cols := PGConstraintColumnsPG18()
	row := fkTestRow(t, []int16{2}, []int16{1})

	// conbin MUST be NULL for an FK. An empty string would be a non-null
	// varlena and would set HEAP_HASVARWIDTH unconditionally, masking this
	// entire check — so assert the premise before asserting the bit.
	if !row[27].IsNull() {
		t.Fatalf("conbin must be NULL for an FK row, got %q — an empty TEXT would "+
			"mask the varlena check below", row[27].StringValue())
	}
	for _, i := range []int{22, 23, 24, 26} { // conpfeqop, conppeqop, conffeqop, conexclop
		if !row[i].IsNull() {
			t.Fatalf("column %d must be NULL on an FK row", i)
		}
	}

	if !pgRowHasVarWidth(cols, row) {
		t.Errorf("HEAP_HASVARWIDTH would be UNSET on an FK row with a non-null conkey; " +
			"on a correct row conkey is the ONLY varlena (conname is `name` → fixed, " +
			"the four char columns have no Args → fixed), so this bit is load-bearing")
	}
}

// TestPGConstraintFKRowFieldFidelity pins every field R125 and the planner
// depend on. conenforced and convalidated are called out because losing them
// would silently re-break R125's NOT VALID work.
func TestPGConstraintFKRowFieldFidelity(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		fk                    catalog.ForeignKey
		wantEnforced, wantValid bool
		wantUpd, wantDel, wantMatch string
	}{
		{"plain validated", catalog.ForeignKey{Name: "f", OID: 1}, true, true, "a", "a", "s"},
		{"not valid", catalog.ForeignKey{Name: "f", OID: 1, NotValid: true}, true, false, "a", "a", "s"},
		// NOT ENFORCED implies not validated, mirroring PG's processCASbits.
		{"not enforced", catalog.ForeignKey{Name: "f", OID: 1, NotEnforced: true}, false, false, "a", "a", "s"},
		{"match full", catalog.ForeignKey{Name: "f", OID: 1, MatchFull: true}, true, true, "a", "a", "f"},
		{"cascade/setnull", catalog.ForeignKey{Name: "f", OID: 1,
			OnDelete: parser.FKActionCascade, OnUpdate: parser.FKActionSetNull}, true, true, "n", "c", "s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, err := buildPGConstraintRowForForeignKey(tc.fk, 16400, 16390, []int16{1}, []int16{1}, nil)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := row[6].BoolValue(); got != tc.wantEnforced {
				t.Errorf("conenforced = %v, want %v (losing this re-breaks R125)", got, tc.wantEnforced)
			}
			if got := row[7].BoolValue(); got != tc.wantValid {
				t.Errorf("convalidated = %v, want %v", got, tc.wantValid)
			}
			if got := row[13].StringValue(); got != tc.wantUpd {
				t.Errorf("confupdtype = %q, want %q", got, tc.wantUpd)
			}
			if got := row[14].StringValue(); got != tc.wantDel {
				t.Errorf("confdeltype = %q, want %q", got, tc.wantDel)
			}
			if got := row[15].StringValue(); got != tc.wantMatch {
				t.Errorf("confmatchtype = %q, want %q", got, tc.wantMatch)
			}
			if got := row[3].StringValue(); got != "f" {
				t.Errorf("contype = %q, want \"f\"", got)
			}
		})
	}
}

// TestPGConstraintFKRowEmptyRefColumnsStaysNull pins that an FK declared
// without explicit REFERENCES columns keeps an EMPTY confkey rather than
// having the parent's PK attnums materialised into it — preserving
// catalog.ForeignKey's documented "empty = use parent PK" convention verbatim
// (catalog.go:1665) so pg_get_constraintdef's output does not change.
func TestPGConstraintFKRowEmptyRefColumnsStaysNull(t *testing.T) {
	row, err := buildPGConstraintRowForForeignKey(
		catalog.ForeignKey{Name: "f", OID: 1}, 16400, 16390, []int16{2}, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !row[21].IsNull() {
		t.Errorf("confkey = %q, want NULL when RefColumns is empty", row[21].StringValue())
	}
	if row[20].IsNull() {
		t.Error("conkey must still be populated")
	}
}
