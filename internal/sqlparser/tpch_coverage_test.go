package sqlparser

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

func TestTPCHGrammarCoverage(t *testing.T) {
	ok := 0
	for i := 1; i <= 22; i++ {
		qs := tpch.Queries()[i]
		pass := true
		for _, part := range strings.Split(qs, "\\;") {
			toks, err := parser.Lex(strings.TrimSpace(part))
			if err != nil {
				t.Logf("Q%d: lex err %v", i, err)
				pass = false
				break
			}
			if _, err := ParseOne(toks, 0); err != nil {
				t.Logf("Q%d: FAIL %v", i, err)
				pass = false
				break
			}
		}
		if pass {
			ok++
		}
	}
	// Q7/Q8/Q9 (complex FROM-subquery waves) and Q15 (CREATE VIEW DDL) are
	// the documented remaining gaps; 19/22 is today's floor.
	if ok < 19 {
		t.Fatalf("TPC-H grammar coverage %d/22 < floor 19", ok)
	}
}
