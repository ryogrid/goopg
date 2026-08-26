package sqlparser

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableRoutedParity pins the ALTER TABLE forms the grammar claims to
// cover against the legacy parser. Until 2026-08-27 none of this had ever
// executed: fragmentRouted only delegated create/drop to secondKeywordRouted,
// so every ALTER TABLE went to legacy despite eight commits claiming a flip.
func TestAlterTableRoutedParity(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE t ADD COLUMN c int",
		"ALTER TABLE t ADD c int",
		"ALTER TABLE t ADD COLUMN c varchar(20) NOT NULL DEFAULT 'x'",
		"ALTER TABLE t ADD PRIMARY KEY (a)",
		"ALTER TABLE t ADD PRIMARY KEY (a, b)",
		"ALTER TABLE t DROP COLUMN c",
		"ALTER TABLE t DROP CONSTRAINT c",
		"ALTER TABLE t DROP CONSTRAINT c CASCADE",
		"ALTER TABLE t DROP CONSTRAINT IF EXISTS c",
		"ALTER TABLE t VALIDATE CONSTRAINT c",
		"ALTER TABLE t ALTER COLUMN c TYPE numeric(10,2)",
		"ALTER TABLE t ALTER c TYPE int",
		"ALTER TABLE t ALTER COLUMN c SET DEFAULT 0",
		"ALTER TABLE t ALTER COLUMN c DROP DEFAULT",
		"ALTER TABLE t ALTER COLUMN c SET NOT NULL",
		"ALTER TABLE t ALTER COLUMN c DROP NOT NULL",
		"ALTER TABLE t RENAME TO t2",
		"ALTER TABLE t RENAME COLUMN a TO b",
		"ALTER TABLE t RENAME a TO b",
		"ALTER TABLE t REPLICA IDENTITY FULL",
		"ALTER TABLE t REPLICA IDENTITY NOTHING",
		"ALTER TABLE t REPLICA IDENTITY DEFAULT",
		"ALTER TABLE t REPLICA IDENTITY USING INDEX i",
		"ALTER TABLE t OWNER TO bob",
		"ALTER TABLE t SET SCHEMA s",
		"ALTER TABLE t SET LOGGED",
		"ALTER TABLE t SET UNLOGGED",
		"ALTER TABLE t SET (fillfactor=70)",
		"ALTER TABLE IF EXISTS t ADD COLUMN c int",
		"ALTER TABLE ONLY t ADD COLUMN c int",
		"ALTER TABLE t ADD COLUMN a int, ADD COLUMN b text",
		"ALTER TABLE t DROP COLUMN a, ALTER COLUMN b SET NOT NULL",
		"ALTER TABLE p ATTACH PARTITION c FOR VALUES IN (1, 2)",
		"ALTER TABLE p ATTACH PARTITION c FOR VALUES FROM (1) TO (10)",
		"ALTER TABLE p ATTACH PARTITION c DEFAULT",
		"ALTER TABLE p DETACH PARTITION c",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
			continue
		}
		if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, truncForLog(l), truncForLog(n))
		}
	}
}

// TestAlterTableRoutingIsNarrow is the INVERSE gate, and the one that makes the
// narrow flip safe. It harvests every ALTER TABLE form the legacy test corpus
// knows (~138 distinct shapes) and asserts, for each: either the dispatcher
// leaves it on the legacy parser, or the yacc parser reproduces legacy's AST
// exactly.
//
// routeBatch does not fall back once a fragment is routed (dispatch.go:91-95),
// it surfaces the yacc error — so "routed but unparseable" is a hard 42601 for
// the user. This test makes that state unreachable, and turns every future
// widening of routedAlterTableActions into a test-enforced obligation to add
// the grammar alternative at the same time.
func TestAlterTableRoutingIsNarrow(t *testing.T) {
	all := harvestSQLLiterals(t)
	checked, routed := 0, 0
	for _, q := range all {
		if !strings.HasPrefix(strings.ToUpper(q), "ALTER TABLE") {
			continue
		}
		checked++
		toks, err := parser.Lex(q)
		if err != nil {
			continue // lexer-level rejects are not this test's concern
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue // stays legacy: safe by construction
		}
		routed++
		l, n, derr := diffParse(q)
		if derr != nil {
			if strings.HasPrefix(derr.Error(), "legacy:") {
				// Neither parser accepts it (the harvester also picks up
				// truncated literals and commented-out fragments). Routing is
				// not implicated.
				continue
			}
			t.Errorf("ROUTED BUT UNPARSEABLE (hard 42601 for the user):\n  %s\n  %v", q, derr)
			continue
		}
		if l != n {
			t.Errorf("ROUTED BUT DIVERGENT:\n  %s\n  L=%s\n  N=%s", q, truncForLog(l), truncForLog(n))
		}
	}
	if checked < 50 {
		t.Fatalf("harvested only %d ALTER TABLE forms — the corpus scanner rotted", checked)
	}
	t.Logf("ALTER TABLE: %d forms in corpus, %d routed to yacc, %d left on legacy",
		checked, routed, checked-routed)
}
