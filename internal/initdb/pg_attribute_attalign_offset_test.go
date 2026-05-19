package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
)

// TestAllPgAttributeHeapRowsHaveValidAttalignByte scans every
// pg_attribute heap row we would emit for every nailed rel and
// validates that the byte at offset 83 (attalign) decodes to one of
// 'c'/'s'/'i'/'d' — the four TYPALIGN_* values PG18's
// populate_compact_attribute_internal accepts.
//
// Diagnostic context (M0106-0010 step 3cq): the FATAL
// `invalid attalign value:` we see at
// `populate_compact_attribute_internal, tupdesc.c:105` during PG-standby
// `InitPostgres` is NOT caused by goopg's pg_attribute heap — this
// test pins that invariant. The actual root cause is upstream
// `TupleDescInitEntry` reading `typeForm->typalign` from pg_type
// after `RelationCacheInitFileRemove()` (xlog.c:5633) wipes the
// init file at WAL recovery start. goopg's pg_type heap is still
// in v0 encoding (`catalog.EncodePGTypeRow`) and lacks
// PG18-canonical Form_pg_type typalign at the expected struct
// offset. Step 3cq rebuilds pg_type heap with PG-canonical layout.
func TestAllPgAttributeHeapRowsHaveValidAttalignByte(t *testing.T) {
	attrCols := pgAttrColDefs()

	allRels := append([]nailedRel{}, nailedSharedRels...)
	allRels = append(allRels, nailedLocalRels...)

	bad := 0
	for _, rel := range allRels {
		attrs := pgAttrEntriesForRel(rel)
		for _, a := range attrs {
			row := pgAttributeRow(rel.OID, a)
			payload, err := executor.EncodeRowPG(attrCols, row)
			if err != nil {
				t.Fatalf("rel=%d (%s) attr=%s: encode error: %v",
					rel.OID, rel.RelName, a.Name, err)
			}
			if len(payload) < 84 {
				t.Errorf("rel=%d (%s) attr=%s: payload too short (%d bytes)",
					rel.OID, rel.RelName, a.Name, len(payload))
				bad++
				continue
			}
			align := payload[83]
			if align != 'c' && align != 's' && align != 'i' && align != 'd' {
				t.Errorf("rel=%d (%s) attr=%s (typeOID=%d, len=%d): attalign byte at offset 83 is %#x (%q), expected 'c'/'s'/'i'/'d'",
					rel.OID, rel.RelName, a.Name, a.TypeOID, a.Len, align, align)
				bad++
			}
		}
	}
	if bad > 0 {
		t.Logf("found %d pg_attribute rows with invalid attalign", bad)
	}
}
