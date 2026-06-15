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
	nullable := map[int]string{
		20: "attacl",
		21: "attoptions",
		22: "attfdwoptions",
		23: "attmissingval",
		24: "attstattarget",
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
