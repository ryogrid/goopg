package sqlparser

import "testing"

// TestAlterIndex pins P5.12 — ALTER INDEX. Legacy builds an AlterTableStmt for
// it (index and table share the ALTER machinery) and overrides the
// CommandComplete tag for exactly TWO of the six forms, SET TABLESPACE and
// RENAME TO; the other four answer "ALTER TABLE".
//
// Two shapes look like their ALTER TABLE namesakes but are not: the column may
// be a NUMBER (an expression index's position), and the per-column
// `SET ( ... )` option list is CONSUMED AND DISCARDED here while ALTER TABLE's
// identical spelling records it in SetOptions.
func TestAlterIndex(t *testing.T) {
	for _, q := range []string{

		"ALTER INDEX i RENAME TO j", "ALTER INDEX s.i RENAME TO j",
		"ALTER INDEX i SET (fastupdate = off)",
		"ALTER INDEX i SET TABLESPACE ts",
		"ALTER INDEX i ATTACH PARTITION child",
		"ALTER INDEX i ALTER COLUMN 1 SET STATISTICS 100",
		"ALTER INDEX i ALTER COLUMN c SET STATISTICS 10",
		"ALTER INDEX i ALTER COLUMN 1 SET (n_distinct = 1)",
		} {
		assertParity(t, q)
	}
	// Everything else — IF EXISTS included — falls through in legacy to a
	// CompatNoopStmt built by a skip-to-semicolon scan, so it stays legacy.
	assertNotRouted(t, "ALTER INDEX IF EXISTS i RENAME TO j")
	assertNotRouted(t, "ALTER INDEX i DEPENDS ON EXTENSION e")
}
