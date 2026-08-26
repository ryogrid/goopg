package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestP41Shapes(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (a int CONSTRAINT nn NOT NULL, CONSTRAINT pk1 PRIMARY KEY (a))",
		"CREATE TABLE t (a int) WITH (fillfactor=70)",
		"CREATE TABLE t (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE t (a int) INHERITS (p)",
		"CREATE TABLE IF NOT EXISTS t AS SELECT 1",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
