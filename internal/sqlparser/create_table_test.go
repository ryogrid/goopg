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
		"CREATE TABLE t (a int) WITH (fillfactor=70)",
		"CREATE TABLE t (a int) INHERITS (p)",
		"CREATE TABLE IF NOT EXISTS t AS SELECT 1",
		"CREATE TABLE t (a int check (a > 0))",
		"CREATE TABLE t (a int references o(id) on delete cascade)",
		"CREATE TABLE t (a int, check (a < 10))",
		"CREATE TABLE t (a int, b int, foreign key (b) references o (id) on update set null)",
		"CREATE INDEX i ON t (a)",
		"CREATE UNIQUE INDEX i ON t (a, b)",
		"CREATE INDEX IF NOT EXISTS i ON t USING btree (a)",
		"DROP INDEX i",
		"DROP INDEX IF EXISTS i CASCADE",
		"BEGIN", "BEGIN WORK", "START TRANSACTION",
		"COMMIT", "END", "ROLLBACK", "ABORT",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
