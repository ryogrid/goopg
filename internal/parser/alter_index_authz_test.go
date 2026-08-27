package parser

import "testing"

// TestAddPrimaryKeyUsingIndexParity — `ALTER TABLE t ADD PRIMARY KEY USING
// INDEX i` promotes an existing unique index. AlterTableAction.UsingIndexName
// has existed on the AST; only the production was missing, and `add primary`
// is on the routed ALTER allowlist.
func TestAddPrimaryKeyUsingIndexParity(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE t ADD PRIMARY KEY USING INDEX i",
		"ALTER TABLE t ADD PRIMARY KEY (a)",
		"ALTER TABLE t ADD PRIMARY KEY (a, b)",
	} {
		assertParity(t, q)
	}
}

// TestSessionAuthorizationParity — `SET [SESSION] AUTHORIZATION name|DEFAULT`.
// SESSION is consumed by set_scope, so only AUTHORIZATION remains in the rule.
// A separate `AUTHORIZATION DEFAULT` alternative reduce/reduces against
// set_value_atom's own DEFAULT (14 conflicts), and the atom text is "default"
// for both the bare keyword and the literal 'default' — only the token KIND
// separates them — so the check lives in sessionAuthzStmt rather than in the
// grammar. (Legacy itself rejects SET LOCAL AUTHORIZATION, so only the SESSION
// spelling is comparable.)
func TestSessionAuthorizationParity(t *testing.T) {
	for _, q := range []string{
		"SET SESSION AUTHORIZATION u",
		"SET SESSION AUTHORIZATION DEFAULT",
		// guards: the plain SET forms must stay on their own rule
		"SET SESSION x = 1",
		"SET x = DEFAULT",
		"SET x = 1",
	} {
		assertParity(t, q)
	}
}
