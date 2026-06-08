package parser_test

import (
	"testing"
	"github.com/goopg/goopg/internal/parser"
)

func TestLexAt(t *testing.T) {
	cases := []string{
		"SELECT * FROM tbl WHERE c4 <@ box(1)",
		"SELECT * FROM tbl_gist where c4 <@ box(point(1,1),point(10,10))",
		"EXPLAIN (costs off) SELECT * FROM tbl_gist where c4 <@ box(point(1,1),point(10,10))",
		"SELECT * FROM t WHERE c4 && box(point(1,1),point(10,10))",
	}
	for _, sql := range cases {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Errorf("parse error for %q: %v", sql, err)
			continue
		}
		if len(stmts) == 0 {
			t.Errorf("no statements for %q", sql)
		}
	}
}
