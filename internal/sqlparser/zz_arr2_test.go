package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestArrLex(t *testing.T) {
	toks, _ := parser.Lex("SELECT ARRAY[1]")
	for _, tk := range toks {
		fmt.Printf("%v:%q ", tk.Kind, tk.Value)
	}
	fmt.Println()
	_, err := ParseOne(mustLex(t, "SELECT ARRAY[1]"), 0)
	t.Logf("ParseOne err=%v", err)
}

func mustLex(t *testing.T, q string) []parser.Token {
	t.Helper()
	toks, err := parser.Lex(q)
	if err != nil {
		t.Fatal(err)
	}
	return toks
}
