package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestDebugSelectOne(t *testing.T) {
	toks, _ := parser.Lex("SELECT 1")
	for i, tk := range toks {
		t.Logf("tok%d kind=%v val=%q term=%d", i, tk.Kind, tk.Value, func() int {
			l := &lexerState{toks: toks}
			return l.mapToken(i).term
		}())
	}
	l := &lexerState{toks: toks}
	rc := yyParse(l)
	t.Logf("yyParse rc=%d err=%v out=%v", rc, l.err, l.out)
}
