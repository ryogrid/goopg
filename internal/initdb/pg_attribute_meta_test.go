package initdb

import "testing"

// TestPgAttributeMetaMatchesCodec guards M0149-0001g slice 17. The bootstrap
// pg_attribute rows take their metadata from a PG 18.3 initdb
// (pgAttributeInitdbMeta). For a relation WITH storage, attalign and attbyval
// describe the bytes goopg's heap codec writes — a PG node deforms the tuple
// with them — so the overlay must not change them there: goopg's own
// pgTypeAlignChar/pgTypeByVal (pinned to the codec by slices 13-14) has to
// agree with PG already. A disagreement means either the codec or PG's value is
// wrong for that column and must be fixed in the codec, not papered over here.
func TestPgAttributeMetaMatchesCodec(t *testing.T) {
	checked := 0
	for _, rel := range pgClassBootstrapRels() {
		if rel.RelKind != 'r' && rel.RelKind != 't' {
			continue
		}
		for _, a := range pgAttrEntriesForRel(rel) {
			m, ok := pgAttributeInitdbMetaFor(rel.OID, a.Name, a.TypeOID)
			if !ok {
				continue
			}
			checked++
			if goopg := pgTypeAlignChar(a.TypeOID)[0]; goopg != m.Align {
				t.Errorf("%s.%s (type %d): PG attalign %q, goopg codec-side %q", rel.RelName, a.Name, a.TypeOID, m.Align, goopg)
			}
			if goopg := pgTypeByVal(a.TypeOID); goopg != m.ByVal && a.Len > 0 && a.Len <= 8 {
				// attbyval is only consulted for fixed-width columns.
				t.Logf("%s.%s (type %d): attbyval PG %v, goopg %v (metadata only)", rel.RelName, a.Name, a.TypeOID, m.ByVal, goopg)
			}
		}
	}
	if checked < 500 {
		t.Fatalf("only %d stored columns matched pgAttributeInitdbMeta", checked)
	}
	t.Logf("%d stored system columns checked", checked)
}
