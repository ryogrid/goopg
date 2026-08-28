package parser

import "testing"

// TestAsyncAndLastClasses pins P5.14 — LISTEN / NOTIFY / UNLISTEN, DROP
// LANGUAGE, and the ONE ALTER MATERIALIZED VIEW form that has a real AST.
//
// With this wave every statement class in the regress corpus that legacy
// answers with a real AST node is routed. What stays on the legacy path is now
// exactly two things, neither of which is grammar work: the classes handled by
// token scanners ABOVE Parse (role DDL, GRANT/REVOKE — Parse
// REJECTS them), and the parse-and-ignore compat classes whose legacy handler
// ends in parseSkipToSemicolon and therefore accepts arbitrary token soup.
func TestAsyncAndLastClasses(t *testing.T) {
	for _, q := range []string{

		"LISTEN c", "NOTIFY c", "NOTIFY c, 'payload'", "UNLISTEN c", "UNLISTEN *",
		"DROP LANGUAGE l", "DROP LANGUAGE IF EXISTS l CASCADE",
		"ALTER MATERIALIZED VIEW mv SET SCHEMA s",
		"ALTER MATERIALIZED VIEW s.mv SET SCHEMA t",
		} {
		assertParity(t, q)
	}
	// Every other ALTER MATERIALIZED VIEW form is a CompatNoopStmt in legacy.
	assertNotRouted(t, "ALTER MATERIALIZED VIEW mv RENAME TO m2")
	assertNotRouted(t, "ALTER MATERIALIZED VIEW mv OWNER TO r")
	assertNotRouted(t, "ALTER MATERIALIZED VIEW mv SET ACCESS METHOD heap2")
}
