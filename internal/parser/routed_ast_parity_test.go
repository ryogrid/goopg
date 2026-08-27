package parser

import (
	"testing"

)

// Parity pins for four AST-carrier defects that were live on ALREADY-ROUTED
// statement classes. None of them was a parse failure — each produced a
// well-formed statement carrying the WRONG value, which is why the grammar
// gates, pgbench and TPC-H all stayed green while they shipped.

// TestSetValueParity — `set` is routed, and every `SET name = value` was
// storing the WRONG value.
//
// The rule captured its value with a mid-rule markSpanStart(), whose peek() is
// not lookahead-stable: the parser has already consumed the first value token
// to decide the set_eq_to reduce, so peek() pointed one token past it.
// `SET x = 1` stored "" (peek was EOF) and `SET search_path TO public,
// pg_catalog` stored ", pg_catalog".
//
// The port is also not a source span at all: legacy's parseSetValueAtoms
// (internal/parser/parser.go:3056) joins each value token's DECODED text with
// ", ", so quoted literals lose their quotes. A span matches only by accident.
func TestSetValueParity(t *testing.T) {
	for _, q := range []string{
		"SET x = 1",
		"SET x TO 'v'",
		"SET x = DEFAULT",
		"SET SESSION x = 1",
		"SET LOCAL x = off",
		"SET work_mem = '64MB'",
		"SET timezone = 'UTC'",
		"SET search_path TO public, pg_catalog",
		"SET search_path TO a, b",
		"SET seq_page_cost = 0.1",
		"SET statement_timeout = 0",
		"SHOW x", "SHOW ALL", "RESET x", "RESET ALL",
	} {
		assertParity(t, q)
	}
}

// TestOnConflictArbiterParity — `insert` is routed. Legacy keeps
// OnConflictTarget.Exprs PARALLEL to Columns (one entry per column, nil for a
// plain name; internal/parser/dml.go parseConflictTargetColumnList), and the
// port left it nil, so every column arbiter diverged as ∅ vs [∅].
func TestOnConflictArbiterParity(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (a, b) DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2 WHERE t.w > 0",
		"INSERT INTO t VALUES (1) ON CONFLICT ON CONSTRAINT c DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING",
	} {
		assertParity(t, q)
	}
}

// TestTransactionModeListParity — the tx_mode_list recursion returned $1
// unchanged, silently dropping every mode after the first comma, so
// `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` started a READ WRITE
// transaction. All the begin/start keywords are routed.
func TestTransactionModeListParity(t *testing.T) {
	for _, q := range []string{
		"BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY",
		"BEGIN READ ONLY, DEFERRABLE",
		"BEGIN ISOLATION LEVEL REPEATABLE READ, READ WRITE",
		"START TRANSACTION ISOLATION LEVEL READ COMMITTED, READ ONLY, DEFERRABLE",
		"BEGIN ISOLATION LEVEL SERIALIZABLE",
		"BEGIN READ ONLY",
		"BEGIN NOT DEFERRABLE",
	} {
		assertParity(t, q)
	}
}

// TestZeroArgCallParity — name_or_call coerced nil args to []Expr{},
// but legacy's parseFuncCallTail returns on the empty-parens path without
// touching fc.Args (select.go:4778), so `now()` is Args=∅. canonDump
// distinguishes nil from an empty slice, so every non-OVER zero-arg call
// diverged. (The OVER alternatives already passed the args through.)
func TestZeroArgCallParity(t *testing.T) {
	for _, q := range []string{
		"SELECT now()",
		"SELECT now()::timestamp with time zone",
		"SELECT pg_backend_pid()",
		"SELECT current_database(), txid_current()",
		"SELECT count(*) FROM t",
		"SELECT count(a) FROM t",
		"SELECT row_number() OVER (ORDER BY a) FROM t",
	} {
		assertParity(t, q)
	}
}

// TestRawSpanQuotedTailParity — fragEndPos used the last token's
// Pos+len(Value), but Value is the DECODED text: a trailing quoted literal or
// quoted identifier under-counted by its quotes and truncated the captured
// span. A delimiter's position (EOF token, or ';') is exact.
func TestRawSpanQuotedTailParity(t *testing.T) {
	for _, q := range []string{
		"CREATE VIEW v AS SELECT 'x'",
		"CREATE VIEW v AS SELECT 'x';",
		`CREATE VIEW v AS SELECT a FROM t WHERE b = 'lit'`,
		`CREATE VIEW v AS SELECT "Quoted"`,
		"CREATE TABLE t (a int CHECK (a > 0))",
		`CREATE TABLE t (a text CHECK (a <> 'x'))`,
		"CREATE MATERIALIZED VIEW mv AS SELECT 'x' WITH NO DATA",
	} {
		assertParity(t, q)
	}
}

// assertParity runs one statement through both parsers and requires identical
// canonical dumps.
func assertParity(t *testing.T, q string) {
	t.Helper()
	recordGolden(q, yaccDump(q))
	l, n, err := diffParse(q)
	if err != nil {
		t.Errorf("%q -> %v", q, err)
		return
	}
	if l != n {
		t.Errorf("DIFF %q\n L=%s\n N=%s", q, truncForLog(l), truncForLog(n))
	}
}

// assertBothReject pins a form that BOTH parsers must refuse. The differential
// harness only reports legacy-accepts/yacc-rejects, so a widening — the yacc
// parser accepting something legacy does not — is invisible to it; this is the
// explicit guard for that direction.
func assertBothReject(t *testing.T, q string) {
	t.Helper()
	recordGolden(q, yaccDump(q))
	if _, err := parseLegacyOnly(q); err == nil {
		t.Fatalf("legacy ACCEPTS %q — this guard assumes it does not", q)
	}
	if _, _, err := diffParse(q); err == nil {
		t.Errorf("yacc ACCEPTS %q but legacy rejects it", q)
	}
}

// assertNotRouted pins a statement the dispatcher must LEAVE on the legacy
// path — typically because legacy answers it with a CompatNoopStmt built by a
// skip-to-semicolon scan, which a grammar cannot reproduce without accepting
// arbitrary token soup. Routing one of these would turn a working statement
// into a 42601, since routeBatch never falls back.
func assertNotRouted(t *testing.T, q string) {
	t.Helper()
	toks, err := Lex(q)
	if err != nil {
		t.Fatalf("lex %q: %v", q, err)
	}
	frags := SplitStatements(toks)
	if len(frags) == 1 && fragmentRouted(frags[0]) {
		t.Errorf("%q is routed, but the grammar does not cover it", q)
	}
}
