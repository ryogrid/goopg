package sqlparser

import "testing"

// TestAsColLabel pins `AS <reserved keyword>`. Upstream's ColLabel admits every
// keyword class including reserved; this grammar's did not, so `SELECT true AS
// true` — and the 33 other must-pass regress fragments spelled that way, mostly
// boolean.sql's `AS true` / `AS false` idiom — were hard 42601s.
//
// The widening is a SEPARATE nonterminal rather than an extension of ColLabel
// because ColLabel's other user is set_value_atom: admitting reserved words
// there makes `SET ROLE TO x` ambiguous (TO becomes a candidate VALUE for SET
// ROLE, and the shift that wins swallows it). Measured — widening ColLabel
// itself costs that one conflict; as_col_label costs none.
func TestAsColLabel(t *testing.T) {
	for _, q := range []string{
		"SELECT true AS true",
		"SELECT false AS false",
		"SELECT bool 't' AS true",
		"SELECT bool 'false' AS false",
		"SELECT f1 AS five FROM text_tbl",
		"SELECT a AS user FROM t",
		"SELECT a AS from FROM t",
		"SELECT a AS select FROM t",
		"SELECT a AS all, b AS case FROM t",
		// Unreserved / col_name / type_func_name labels must still work.
		"SELECT a AS x FROM t",
		"SELECT a AS between FROM t",
		"SELECT a AS left FROM t",
	} {
		assertParity(t, q)
	}
}

// TestSetRoleToStillBinds is the other half of that decision: these are the
// forms the ColLabel widening would have broken.
func TestSetRoleToStillBinds(t *testing.T) {
	for _, q := range []string{
		"SET ROLE TO admin",
		"SET ROLE admin",
		"SET LOCAL ROLE admin",
		"SET SESSION ROLE admin",
		"SET search_path TO public, pg_catalog",
		"SET x = DEFAULT",
		"SET x TO on",
		"SET x TO true",
		"SET x = false",
	} {
		assertParity(t, q)
	}
}
