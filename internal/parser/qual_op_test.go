package parser

import "testing"

// TestQualifiedOperatorParity — gram.y's `qual_Op` (:16658), the
// `OPERATOR(schema.op)` spelling of an operator, in the four positions
// upstream uses it: `a_expr qual_Op a_expr` (:15009), `qual_Op a_expr`
// (:15011) and the b_expr pair (:15488, :15490).
//
// This is not an exotic spelling: psql emits it on EVERY pattern-matching
// describe meta-command. processSQLNamePattern
// (postgres/src/fe_utils/string_utils.c:1121-1152) writes
// `<namevar> OPERATOR(pg_catalog.~) '^(pat)$' COLLATE pg_catalog.default`,
// so before this rule existed `\dRp+ name`, `\d name`, `\dt pat` and every
// sibling failed with `syntax error at or near "OPERATOR"` — 69 of them in
// the publication.sql regress diff alone (M0134-0158).
//
// goopg's AST has no schema-qualified operator node, so the qualifier is
// dropped and the spelling resolves to the same OpCode the bare operator
// does. That is why the pins below are AST goldens rather than error pins:
// `a OPERATOR(pg_catalog.=) b` must dump identically to `a = b`.
func TestQualifiedOperatorParity(t *testing.T) {
	for _, q := range []string{
		// binary, schema-qualified and bare
		"SELECT 1 WHERE 1 OPERATOR(pg_catalog.=) 1",
		"SELECT 1 WHERE 1 OPERATOR(=) 1",
		"SELECT 1 WHERE 'a' OPERATOR(pg_catalog.~) '^a$'",
		"SELECT a OPERATOR(pg_catalog.||) b FROM t",
		// the multi-character run: the lexer splits `<=` style spellings per
		// character and op_run rejoins them inside the parens
		"SELECT 1 WHERE 1 OPERATOR(pg_catalog.<=) 2",
		"SELECT 1 WHERE 1 OPERATOR(pg_catalog.<>) 2",
		// prefix: '-' and '+' can only reach a prefix position through
		// OPERATOR(...), and must fold exactly like the bare spelling does
		"SELECT OPERATOR(pg_catalog.-) 1",
		"SELECT OPERATOR(pg_catalog.+) 1",
		"SELECT OPERATOR(pg_catalog.~) 5",
		// b_expr — BETWEEN's operands
		"SELECT 1 WHERE 3 BETWEEN OPERATOR(pg_catalog.-) 1 AND 5",
		// the shape psql actually sends
		"SELECT pubname FROM pg_catalog.pg_publication WHERE pubname OPERATOR(pg_catalog.~) '^(testpub)$' COLLATE pg_catalog.default",
		// guards: the bare-operator rules must be untouched
		"SELECT 1 WHERE 1 = 1",
		"SELECT a || b FROM t",
		"SELECT -1",
		"SELECT ~5",
		"SELECT 1 WHERE 3 BETWEEN -1 AND 5",
	} {
		assertParity(t, q)
	}
}

// TestQualifiedOperatorFoldsLikeBareSpelling asserts the property the goldens
// only imply one statement at a time: dropping the qualifier must leave the
// SAME AST, otherwise psql's describe queries would parse but mean something
// else than the query PG runs.
func TestQualifiedOperatorFoldsLikeBareSpelling(t *testing.T) {
	for _, pair := range [][2]string{
		{"SELECT 1 WHERE 1 OPERATOR(pg_catalog.=) 1", "SELECT 1 WHERE 1 = 1"},
		{"SELECT a OPERATOR(pg_catalog.||) b FROM t", "SELECT a || b FROM t"},
		{"SELECT 1 WHERE 1 OPERATOR(pg_catalog.<=) 2", "SELECT 1 WHERE 1 <= 2"},
		// doNegate folding (playbook §12.5): `-1` is an IntegerConst{-1}, not
		// a UnaryOp, and the qualified spelling must not build the other shape
		{"SELECT OPERATOR(pg_catalog.-) 1", "SELECT -1"},
		{"SELECT OPERATOR(pg_catalog.~) 5", "SELECT ~5"},
	} {
		qualified, bare := yaccDump(pair[0]), yaccDump(pair[1])
		if qualified != bare {
			t.Errorf("qualified spelling does not fold to the bare one\n %q => %s\n %q => %s",
				pair[0], truncForLog(qualified), pair[1], truncForLog(bare))
		}
	}
}

// TestPatternOperatorSpellingsStillUnsupported pins a gap this change did NOT
// close, so that closing it later is a deliberate, reviewed golden drift
// rather than a side effect. PG spells LIKE/ILIKE as the operators ~~, !~~,
// ~~* and !~~* (postgres/src/include/catalog/pg_operator.dat) and accepts them
// wherever an operator is legal. goopg's ParseBinaryOp (op.go:101) has no case
// for them, so the OPERATOR(...) path — which only inherits that lookup —
// reports `unsupported operator`. Nothing psql sends needs them.
//
// The BARE spelling is worse and is deliberately NOT pinned here: the lexer
// emits `~~` as two separate `~` tokens, so `'a' ~~ 'b'` parses as
// `'a' ~ (~ 'b')` and reaches the executor as a regex match over a bitwise
// NOT instead of erroring. op_run rejoins the run inside OPERATOR(...), which
// is why only the qualified spelling gets the honest message. Both are in the
// M0134-0158 deferral-ledger row.
func TestPatternOperatorSpellingsStillUnsupported(t *testing.T) {
	assertBothReject(t, "SELECT 1 WHERE 'a' OPERATOR(pg_catalog.!~~) 'b'")
}

// TestCollateAnyNameParity — gram.y takes `any_name` after COLLATE (:14867)
// and any_name's second component is attr_name/ColLabel (:9161, :17724), which
// admits reserved keywords. goopg used ColId there, so `COLLATE
// pg_catalog.default` — the only qualified collation psql ever writes, and one
// whose second component is RESERVED — was a hard 42601.
func TestCollateAnyNameParity(t *testing.T) {
	for _, q := range []string{
		"SELECT 'a' COLLATE pg_catalog.default",
		"SELECT 'a' COLLATE \"C\"",
		"SELECT 'a' COLLATE pg_catalog.\"C\"",
		// reserved words after the dot are legal for the same reason
		"SELECT 'a' COLLATE pg_catalog.\"default\"",
		"CREATE TABLE t (a text COLLATE pg_catalog.default)",
	} {
		assertParity(t, q)
	}
}
