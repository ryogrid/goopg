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

// TestAlterView pins P5.13 — ALTER VIEW. Like ALTER INDEX it produces an
// AlterTableStmt, but it tags EVERY form "ALTER VIEW" and it takes IF EXISTS.
// OwnerTo lives on the STATEMENT rather than on an action, and the three
// self-referential role spellings collapse to the "current_user" sentinel.
func TestAlterView(t *testing.T) {
	for _, q := range []string{

		"ALTER VIEW v OWNER TO r", "ALTER VIEW v OWNER TO CURRENT_USER",
		"ALTER VIEW v RENAME TO w", "ALTER VIEW IF EXISTS v RENAME TO w",
		"ALTER VIEW v RENAME COLUMN a TO b", "ALTER VIEW v RENAME a TO b",
		"ALTER VIEW v SET (security_barrier=true)", "ALTER VIEW v RESET (security_barrier)",
		"ALTER VIEW v SET SCHEMA s",
		"ALTER VIEW v ALTER COLUMN a SET DEFAULT 1", "ALTER VIEW v ALTER a DROP DEFAULT",
		"ALTER VIEW s.v OWNER TO r",
		} {
		assertParity(t, q)
	}
	// Outside these seven forms legacy falls through to a skip-to-semicolon
	// no-op, so the rest stays on the legacy path.
	assertNotRouted(t, "ALTER VIEW v DEPENDS ON EXTENSION e")
}
