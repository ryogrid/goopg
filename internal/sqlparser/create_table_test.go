package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestCreateTableV0(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (a int, b text)",
		"CREATE TABLE t (a int primary key, b text not null default 'x')",
		"CREATE TABLE t (a int, b text, primary key (a))",
		"CREATE TABLE IF NOT EXISTS t (a int)",
		"CREATE TEMP TABLE tt (a int)",
		"CREATE UNLOGGED TABLE uu (a int)",
		"DROP TABLE t",
		"DROP TABLE IF EXISTS a, b CASCADE",
		"TRUNCATE t",
		"TRUNCATE TABLE a, b RESTART IDENTITY",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
