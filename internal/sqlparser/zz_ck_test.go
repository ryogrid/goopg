package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestCkShapes(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (a int check (a > 0))",
		"CREATE TABLE t (a int references o(id))",
		"CREATE TABLE t (a int references o(id) on delete cascade)",
		"CREATE TABLE t (a int, b int, foreign key (b) references o (id) on update set null)",
		"CREATE TABLE t (a int, check (a < 10))",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
