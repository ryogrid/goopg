package sqlparser

import (
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestLegacyCorpusRejects closes the hole that let ~30 gaps accumulate
// unnoticed: TestLegacyCorpusParity `continue`s whenever EITHER parser rejects
// a statement, so "legacy accepts it and the yacc parser does not" — a hard
// 42601 on a routed class — was invisible to it.
//
// The count must stay at ZERO. A new entry means a routed class lost a form
// that internal/parser's own tests cover but the regress corpus does not,
// which is exactly the class of gap the P7.1 dry run found 73 of.
func TestLegacyCorpusRejects(t *testing.T) {
	var bad []string
	for _, q := range harvestSQLLiterals(t) {
		if _, lerr := parser.Parse(q); lerr != nil {
			continue // legacy rejects it too: not a gap
		}
		toks, err := parser.Lex(q)
		if err != nil {
			continue
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue // unrouted: legacy owns it by design
		}
		if _, yerr := ParseOneSrc(q, toks); yerr != nil {
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
// go down. The remaining entries are the AST-shape differences left over the
// legacy test corpus for routed classes — a mix of documented known-diffs
// (the unary-minus fold) and unfixed gaps.
//
// 2026-08-28: 37 -> 29 after FK NOT VALID / NOT ENFORCED were RECORDED rather
// than dropped, `CONSTRAINT n UNIQUE NULLS NOT DISTINCT` kept its name, and a
// quoted `"float"` stopped taking the float8 normalisation.
const legacyCorpusDivergenceCeiling = 29

func TestLegacyCorpusDivergence(t *testing.T) {
	n := 0
	byClass := map[string]int{}
	for _, q := range harvestSQLLiterals(t) {
		toks, err := parser.Lex(q)
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
