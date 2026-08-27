package parser

import "testing"

// TestSmallDDLClasses pins P5.9 — DROP DATABASE, CREATE EXTENSION,
// ALTER SCHEMA and CREATE POLICY, the four remaining DDL classes with a clean,
// fully-specified AST.
//
// CREATE POLICY's `WITH CHECK (expr)` forced an adapter change: the CHECK fold
// (which turns a constraint's `CHECK ( ... )` into one opaque terminal, since
// legacy never parses a constraint body) was swallowing the policy's row
// filter, which IS a real expression. The fold now skips a CHECK preceded by
// WITH; `WITH CHECK OPTION` needs no guard because no paren follows it.
func TestSmallDDLClasses(t *testing.T) {
	for _, q := range []string{

		"DROP DATABASE d", "DROP DATABASE IF EXISTS d", "DROP DATABASE d CASCADE",
		"CREATE EXTENSION e", "CREATE EXTENSION IF NOT EXISTS e SCHEMA s",
		"CREATE EXTENSION e WITH SCHEMA s VERSION '1.0' CASCADE",
		"ALTER SCHEMA s RENAME TO t", "ALTER SCHEMA s OWNER TO r",
		"ALTER SCHEMA s OWNER TO CURRENT_USER",
		"CREATE POLICY p ON t USING (true)",
		"CREATE POLICY p ON t AS RESTRICTIVE FOR SELECT TO r USING (a) WITH CHECK (b)",
		"CREATE POLICY p ON s.t AS PERMISSIVE FOR ALL TO a, b USING (x > 1)",
		"CREATE POLICY p ON t FOR INSERT WITH CHECK (true)",
		"CREATE POLICY p ON t FOR UPDATE USING (a) WITH CHECK (b)",
		"CREATE POLICY p ON t FOR DELETE USING (a)",
		} {
		assertParity(t, q)
	}
	// An extension name is an IDENT or a string literal, never a quoted
	// identifier or a number — legacy rejects both spellings.
	assertBothReject(t, "CREATE EXTENSION \"e\"")
	assertBothReject(t, "CREATE EXTENSION e VERSION 1")
}
