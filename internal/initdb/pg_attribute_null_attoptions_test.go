package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
)

// TestPgAttributeRowEmitsNullForOptionalArrayColumns pins Step 3u of
// M0106-0010. The four trailing array/varlena pg_attribute columns
// (attacl, attoptions, attfdwoptions, attmissingval) must be SQL NULL
// for nailed catalog rows. If any of them is encoded as a non-NULL
// empty varlena, PG's RelationGetIndexAttOptions →
// index_opclass_options interprets it as "options present" and
// ereports an ERROR whose errmsg argument calls generate_opclass_name
// → OpclassIsVisible → get_namespace_oid → opens
// pg_namespace_nspname_index (OID 2684) → recursive
// RelationInitIndexAccessInfo on the very index whose error message is
// being formatted → ERRORDATA_STACK_SIZE PANIC at every backend start.
func TestPgAttributeRowEmitsNullForOptionalArrayColumns(t *testing.T) {
	row := pgAttributeRow(2684, nailedAttr{Name: "nspname", TypeOID: 19, Num: 1, Len: 64, NotNull: true})
	if len(row) != 25 {
		t.Fatalf("pgAttributeRow: expected 25 columns, got %d", len(row))
	}
	// The four trailing nullable varlena columns that
	// RelationGetIndexAttOptions inspects, plus attstattarget (also NULL).
	// Resolved by NAME against pgAttrColDefs rather than by hardcoded index:
	// M0131-S14.1 moved attstattarget from #25 to its PG18-canonical #21 and
	// this map silently kept asserting the right positions under the wrong
	// labels, which is the sort of drift the whole slice is about.
	nullable := map[int]string{}
	for i, c := range pgAttrColDefs() {
		switch c.Name {
		case "attstattarget", "attacl", "attoptions", "attfdwoptions", "attmissingval":
			nullable[i] = c.Name
		}
	}
	if len(nullable) != 5 {
		t.Fatalf("pgAttrColDefs: found %d of the 5 nullable trailing columns", len(nullable))
	}
	for idx, name := range nullable {
		if !row[idx].IsNull() {
			t.Errorf("pgAttributeRow column %d (%s): want NULL, got Kind=%v Buf=%q",
				idx, name, row[idx].Kind, string(row[idx].Buf))
		}
	}
	// Sanity: the leading required columns are still non-NULL.
	if row[0].IsNull() {
		t.Errorf("pgAttributeRow column 0 (attrelid) must not be NULL")
	}
	if row[1].IsNull() {
		t.Errorf("pgAttributeRow column 1 (attname) must not be NULL")
	}
	_ = executor.NullDatum // ensure import stays referenced even if NullDatum becomes a value type
}
