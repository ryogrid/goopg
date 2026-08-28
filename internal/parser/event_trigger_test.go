package parser

import "testing"

// TestEventTriggerStmts pins P7.2's first class: CREATE / ALTER EVENT TRIGGER
// (gram.y CreateEventTrigStmt / AlterEventTrigStmt).
//
// P5.14's note claimed every class legacy answers with a REAL AST node was
// routed. It was not: this one, CREATE AGGREGATE, CREATE ACCESS METHOD,
// CREATE RULE, ALTER DEFAULT PRIVILEGES and CREATE STATISTICS all build real
// nodes and all still fell to legacy. Measured over the whole regress corpus,
// 234 of 13,582 fragments (1.7%) genuinely reach the legacy parser; the other
// 310 unrouted ones never get there at all, because postmaster's
// compatNoopCommandTag (dispatch.go:1879) intercepts role DDL, GRANT/REVOKE
// and database/schema DDL by string prefix ABOVE Parse.
func TestEventTriggerStmts(t *testing.T) {
	for _, q := range []string{
		// EXECUTE takes FUNCTION, PROCEDURE, or neither — ddl.go's
		// `_ = p.acceptKeyword(KwFunction) || p.acceptKeyword(KwProcedure)`.
		"CREATE EVENT TRIGGER t ON ddl_command_start EXECUTE FUNCTION f()",
		"CREATE EVENT TRIGGER t ON ddl_command_end EXECUTE PROCEDURE s.f()",
		"CREATE EVENT TRIGGER t ON sql_drop EXECUTE f()",
		"CREATE EVENT TRIGGER t ON table_rewrite EXECUTE FUNCTION public.f()",
		// WHEN <var> IN (...) — only a variable named "tag" contributes to
		// Tags, but every clause appends to FilterVars, and FilterVar is
		// last-write-wins (event_trigger.c needs the sequence to spot a
		// duplicate filter variable).
		"CREATE EVENT TRIGGER t ON ddl_command_start WHEN tag IN ('DROP TABLE') EXECUTE FUNCTION f()",
		"CREATE EVENT TRIGGER t ON ddl_command_start WHEN tag IN ('DROP TABLE','CREATE TABLE') EXECUTE FUNCTION f()",
		"CREATE EVENT TRIGGER t ON ddl_command_start WHEN tag IN ('a') AND tag IN ('b') EXECUTE FUNCTION f()",
		"CREATE EVENT TRIGGER t ON ddl_command_start WHEN other IN ('a') EXECUTE FUNCTION f()",
		"CREATE EVENT TRIGGER t ON ddl_command_start WHEN other IN ('a') AND tag IN ('b') EXECUTE FUNCTION f()",

		// enable_trigger, verbatim from gram.y:6319 — the four spellings and
		// no others.
		"ALTER EVENT TRIGGER t DISABLE",
		"ALTER EVENT TRIGGER t ENABLE",
		"ALTER EVENT TRIGGER t ENABLE REPLICA",
		"ALTER EVENT TRIGGER t ENABLE ALWAYS",
		// RENAME TO / OWNER TO live in upstream's RenameStmt / AlterOwnerStmt;
		// goopg folds them into the same statement type.
		"ALTER EVENT TRIGGER t RENAME TO u",
		"ALTER EVENT TRIGGER t OWNER TO alice",
		"ALTER EVENT TRIGGER t OWNER TO CURRENT_USER",
		"ALTER EVENT TRIGGER t OWNER TO SESSION_USER",
	} {
		assertParity(t, q)
	}

	// The action words are TERMINALS, not ColIds. A ColId pair would have
	// matched these and built an AlterEventTriggerStmt with an empty Action;
	// ddl.go leaves the extra word unconsumed so it surfaces as a
	// trailing-token error, which exact terminals reproduce for free.
	assertBothReject(t, "ALTER EVENT TRIGGER t ENABLE BOGUS")
	assertBothReject(t, "ALTER EVENT TRIGGER t DISABLE ALWAYS")
	assertBothReject(t, "ALTER EVENT TRIGGER t BOGUS")
	// An event trigger function takes no arguments (ddl.go: "event trigger
	// functions take no arguments").
	assertBothReject(t, "CREATE EVENT TRIGGER t ON ddl_command_start EXECUTE FUNCTION f(1)")
	assertBothReject(t, "CREATE EVENT TRIGGER t ON ddl_command_start EXECUTE FUNCTION f")
}

// TestCreateAccessMethodStmt pins gram.y:5991 CreateAmStmt. It is the ONLY
// other unrouted class whose legacy handler is a strict recursive-descent
// parser rather than a token walk — see the survey in
// docs/design/not_ralph/TODO.md under "P7.2 scope, measured".
func TestCreateAccessMethodStmt(t *testing.T) {
	for _, q := range []string{
		"CREATE ACCESS METHOD m TYPE INDEX HANDLER h",
		"CREATE ACCESS METHOD m TYPE TABLE HANDLER s.h",
		"CREATE ACCESS METHOD gist2 TYPE INDEX HANDLER pg_catalog.gisthandler",
	} {
		assertParity(t, q)
	}
	// am_type is INDEX or TABLE and nothing else (gram.y:6002); TYPE and
	// HANDLER are terminals, so all three malformations reject without an
	// explicit check, at the same offset legacy reports.
	assertBothReject(t, "CREATE ACCESS METHOD m TYPE BOGUS HANDLER h")
	assertBothReject(t, "CREATE ACCESS METHOD m HANDLER h")
	assertBothReject(t, "CREATE ACCESS METHOD m TYPE INDEX h")
}
