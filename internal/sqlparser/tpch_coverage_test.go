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
	// 2026-08-27: all 22 parse. The last two holdouts (Q21/Q22) were not a
	// grammar gap at all — EXISTS had been added to the NOT_LA follower set
	// in base_yylex.go, which upstream's parser.c does not do, so every
	// `NOT EXISTS (...)` was a syntax error. Floor is now the full set: a
	// drop here means a real regression, not an unported wave.
	if ok < 22 {
		t.Fatalf("TPC-H grammar coverage %d/22 < floor 22", ok)
	}
}
