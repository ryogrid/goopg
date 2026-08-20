package parser

import "testing"

// TestLexJSONArrowOperators pins the lexer's greedy match of -> (2-char)
// and ->> (3-char), distinct from a bare '-' followed by '>'. M0118-0009.
func TestLexJSONArrowOperators(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"a -> b", "->"},
		{"a ->> b", "->>"},
		{"a->b", "->"},
		{"a->>b", "->>"},
	}
	for _, c := range cases {
		l := &lexer{src: c.src}
		var ops []string
		for {
			tok, err := l.next()
			if err != nil {
				t.Fatalf("%q: lex error %v", c.src, err)
			}
			if tok.Kind == TokenEOF {
				break
			}
			if tok.Kind == TokenOperator {
				ops = append(ops, tok.Value)
			}
		}
		if len(ops) != 1 || ops[0] != c.want {
			t.Errorf("%q: got operators %v, want [%s]", c.src, ops, c.want)
		}
	}
}

// TestParseJSONArrowChain pins that j->0->'Plan'->'Heap Fetches' parses
// left-associatively into nested BinaryOp{OpJSONGet} nodes — the shape the
// horizons isolation spec relies on. M0118-0009.
func TestParseJSONArrowChain(t *testing.T) {
	stmts, err := Parse("SELECT j -> 0 -> 'Plan' -> 'Heap Fetches' FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	top, ok := s.Targets[0].Expr.(*BinaryOp)
	if !ok || top.Op != OpJSONGet {
		t.Fatalf("top = %+v, want BinaryOp OpJSONGet", s.Targets[0].Expr)
	}
	// Right operand is the final key; left is the rest of the chain.
	if sl, ok := top.Right.(*StringConst); !ok || sl.Value != "Heap Fetches" {
		t.Errorf("top.Right = %+v, want StringConst 'Heap Fetches'", top.Right)
	}
	mid, ok := top.Left.(*BinaryOp)
	if !ok || mid.Op != OpJSONGet {
		t.Fatalf("mid = %+v, want BinaryOp OpJSONGet", top.Left)
	}
	if sl, ok := mid.Right.(*StringConst); !ok || sl.Value != "Plan" {
		t.Errorf("mid.Right = %+v, want StringConst 'Plan'", mid.Right)
	}
	bottom, ok := mid.Left.(*BinaryOp)
	if !ok || bottom.Op != OpJSONGet {
		t.Fatalf("bottom = %+v, want BinaryOp OpJSONGet", mid.Left)
	}
	if il, ok := bottom.Right.(*IntegerConst); !ok || il.Value != 0 {
		t.Errorf("bottom.Right = %+v, want IntegerConst 0", bottom.Right)
	}
}

// TestParseJSONArrowText pins ->> as OpJSONGetText.
func TestParseJSONArrowText(t *testing.T) {
	stmts, err := Parse("SELECT j ->> 'a' FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	bo, ok := s.Targets[0].Expr.(*BinaryOp)
	if !ok || bo.Op != OpJSONGetText {
		t.Fatalf("expr = %+v, want BinaryOp OpJSONGetText", s.Targets[0].Expr)
	}
}

// TestJSONPathGetLex pins the lexer's greedy match of #> (2-char) and #>>
// (3-char), distinct from a bare '#' followed by '>' — and confirms the
// bare '#' single-char fallback (e.g. bitwise XOR) still lexes on its own
// when not followed by '>'. M0134-0039.
func TestJSONPathGetLex(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"a #> b", []string{"#>"}},
		{"a #>> b", []string{"#>>"}},
		{"a#>b", []string{"#>"}},
		{"a#>>b", []string{"#>>"}},
		{"a # b", []string{"#"}},
	}
	for _, c := range cases {
		l := &lexer{src: c.src}
		var ops []string
		for {
			tok, err := l.next()
			if err != nil {
				t.Fatalf("%q: lex error %v", c.src, err)
			}
			if tok.Kind == TokenEOF {
				break
			}
			if tok.Kind == TokenOperator {
				ops = append(ops, tok.Value)
			}
		}
		if len(ops) != len(c.want) {
			t.Fatalf("%q: got operators %v, want %v", c.src, ops, c.want)
		}
		for i := range ops {
			if ops[i] != c.want[i] {
				t.Errorf("%q: got operators %v, want %v", c.src, ops, c.want)
			}
		}
	}
}

// TestParseJSONPathGetChain pins j #> array['a','b'] #> array['c'] parsing
// left-associatively into nested BinaryOp{OpJSONPathGet} nodes, mirroring
// TestParseJSONArrowChain. M0134-0039.
func TestParseJSONPathGetChain(t *testing.T) {
	stmts, err := Parse("SELECT j #> array['a','b'] #> array['c'] FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	top, ok := s.Targets[0].Expr.(*BinaryOp)
	if !ok || top.Op != OpJSONPathGet {
		t.Fatalf("top = %+v, want BinaryOp OpJSONPathGet", s.Targets[0].Expr)
	}
	mid, ok := top.Left.(*BinaryOp)
	if !ok || mid.Op != OpJSONPathGet {
		t.Fatalf("mid = %+v, want BinaryOp OpJSONPathGet", top.Left)
	}
}

// TestParseJSONPathGetText pins #>> as OpJSONPathGetText.
func TestParseJSONPathGetText(t *testing.T) {
	stmts, err := Parse("SELECT j #>> array['a'] FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	bo, ok := s.Targets[0].Expr.(*BinaryOp)
	if !ok || bo.Op != OpJSONPathGetText {
		t.Fatalf("expr = %+v, want BinaryOp OpJSONPathGetText", s.Targets[0].Expr)
	}
}
