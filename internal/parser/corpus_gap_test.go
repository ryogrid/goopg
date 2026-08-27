package parser

import (
	"sort"
	"strings"
	"testing"

)

// TestLegacyCorpusRejects closes the hole that let ~30 gaps accumulate
// unnoticed: TestLegacyCorpusParity `continue`s whenever EITHER parser rejects
// a statement, so "legacy accepts it and the yacc parser does not" — a hard
// 42601 on a routed class — was invisible to it.
//
// The count must stay at ZERO. A new entry means a routed class lost a form
// that internal/parser's own tests cover but the regress corpus does not,
// which is exactly the class of gap the P7.1 dry run found 73 of.
// stricterThanLegacy lists the statements the grammar refuses BY DESIGN
// because upstream refuses them too — legacy's hand-written lexer simply
// never classified the word. Each entry cites the kwlist.h line that settles
// it. Refusing them is a PG-compatibility FIX, so they must not read as gaps;
// naming them individually keeps every other reject a failure.
var stricterThanLegacy = map[string]string{
	// kwlist.h:480 — RESERVED_KEYWORD, so never a bare ColId.
	"CREATE TABLE t (user text)": "user is reserved upstream",
	// kwlist.h:491 — TYPE_FUNC_NAME_KEYWORD; ColId covers only unreserved
	// and col_name keywords.
	"CREATE TABLE t (verbose text)": "verbose is a type_func_name keyword upstream",
}

func TestLegacyCorpusRejects(t *testing.T) {
	var bad []string
	for _, q := range harvestSQLLiterals(t) {
		if _, lerr := parseLegacyOnly(q); lerr != nil {
			continue // legacy rejects it too: not a gap
		}
		toks, err := Lex(q)
		if err != nil {
			continue
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue // unrouted: legacy owns it by design
		}
		if _, yerr := ParseOneSrc(q, toks); yerr != nil {
			if _, ok := stricterThanLegacy[q]; ok {
				continue
			}
			bad = append(bad, q+"  ||  "+yerr.Error())
		}
	}
	sort.Strings(bad)
	for i, b := range bad {
		if i >= 15 {
			t.Errorf("... and %d more", len(bad)-15)
			break
		}
		t.Errorf("routed class rejects a form legacy accepts: %s", b)
	}
}

// legacyCorpusDivergenceCeiling is a RATCHET, not a target: it may only ever
// go down, and the test fails in BOTH directions so a drop has to be recorded
// here rather than silently absorbed.
//
// It is now ZERO: over the 1,485 statements harvested from internal/parser's
// own tests, every one the dispatcher routes produces a byte-identical AST in
// both parsers. Getting there was 37 -> 29 -> 15 -> 0 across P7.1a/b/c.
const legacyCorpusDivergenceCeiling = 0

// knownDiffCorpus lists the statements whose two ASTs differ ON PURPOSE, each
// backed by a row in docs/design/not_ralph/difftest_known_diffs.md and by its
// own TestKnownDiff* pin. They entered this corpus only when P7.1 merged the
// packages and the harvester began reading the yacc tests' own files.
//
// Naming them EXACTLY, rather than raising the ceiling, is what keeps the
// ratchet meaningful: a seventh divergence still fails the test.
var knownDiffCorpus = map[string]string{
	// Legacy's inline-UNIQUE branch eats the NOT of a following NOT NULL while
	// looking for NOT DEFERRABLE; its CHECK branch loses it the same way. The
	// yacc parser deliberately keeps the constraint.
	"CREATE TABLE t (a int UNIQUE NOT NULL)":       "legacy drops NOT NULL after UNIQUE",
	"CREATE TABLE t (x int CHECK (x > 0) NOT NULL)": "legacy drops NOT NULL after CHECK",
	// Legacy builds UnaryOp(-) over the literal; gram.y folds the sign into
	// the constant, which is what PG's AexprConst does.
	"SELECT f1 FROM t WHERE f1 BETWEEN -1 AND 1":            "unary-minus fold",
	"SELECT f1 FROM t WHERE f1 BETWEEN -1e6 AND 1e6":        "unary-minus fold",
	"SELECT f1 FROM t WHERE f1 BETWEEN SYMMETRIC -1 AND 1":  "unary-minus fold",
	"SELECT f1 FROM t WHERE f1 NOT BETWEEN -1e6 AND 1e6":    "unary-minus fold",
}

func TestLegacyCorpusDivergence(t *testing.T) {
	n := 0
	byClass := map[string]int{}
	for _, q := range harvestSQLLiterals(t) {
		toks, err := Lex(q)
		if err != nil {
			continue
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue
		}
		l, y, err := diffParse(q)
		if err != nil || l == y {
			continue
		}
		if _, known := knownDiffCorpus[q]; known {
			continue
		}
		n++
		byClass[strings.ToUpper(strings.Fields(q)[0])]++
	}
	if n > legacyCorpusDivergenceCeiling {
		t.Errorf("divergence rose to %d (ceiling %d) — by class: %v", n, legacyCorpusDivergenceCeiling, byClass)
	}
	if n < legacyCorpusDivergenceCeiling {
		t.Errorf("divergence fell to %d — lower legacyCorpusDivergenceCeiling to match (by class: %v)", n, byClass)
	}
}
