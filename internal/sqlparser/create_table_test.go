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
		"ALTER TABLE t ADD COLUMN c int",
		"ALTER TABLE t ADD PRIMARY KEY (a)",
		"ALTER TABLE t DROP COLUMN c",
		"ALTER TABLE t ALTER COLUMN a TYPE bigint",
		"ALTER TABLE t RENAME TO t2",
		"ALTER TABLE t ADD COLUMN c int, ADD COLUMN d text",
		"ALTER TABLE t DROP COLUMN c, ALTER COLUMN a TYPE bigint, RENAME TO t2",
		"ALTER TABLE t ALTER COLUMN a SET DEFAULT 5",
		"ALTER TABLE t ALTER COLUMN a SET NOT NULL",
		"ALTER TABLE t ALTER COLUMN a DROP NOT NULL",
		"ALTER TABLE t ALTER COLUMN a DROP DEFAULT",
		"ALTER TABLE t DROP CONSTRAINT k CASCADE",
		"ALTER TABLE t OWNER TO role1",
		"ALTER TABLE t SET SCHEMA s2",
		"ALTER TABLE t SET (fillfactor = 70)",
		"ALTER TABLE t SET LOGGED",
		"ALTER TABLE t SET UNLOGGED",
		"ALTER TABLE t RENAME COLUMN a TO b",
		"ALTER TABLE t VALIDATE CONSTRAINT k",
		"ALTER TABLE t REPLICA IDENTITY FULL",
		"ALTER TABLE t REPLICA IDENTITY NOTHING",
		"ALTER TABLE t REPLICA IDENTITY USING INDEX i",
		"ALTER TABLE t ATTACH PARTITION c FOR VALUES FROM (1) TO (10)",
		"ALTER TABLE t ATTACH PARTITION c FOR VALUES IN (1, 2)",
		"ALTER TABLE t ATTACH PARTITION c DEFAULT",
		"ALTER TABLE t DETACH PARTITION c",
		"CREATE TABLE c (a int) PARTITION OF p FOR VALUES FROM (1) TO (10)",
		"CREATE TABLE c PARTITION OF p FOR VALUES IN (1, 2)",
		"CREATE TABLE c PARTITION OF p DEFAULT",
		"CREATE TABLE c (a int) WITH (fillfactor=70) PARTITION BY RANGE (a)",
		"CREATE VIEW v AS SELECT 1",
		"CREATE OR REPLACE VIEW s.v (a, b) AS SELECT 1, 2",
		"BEGIN", "BEGIN WORK", "START TRANSACTION",
		"COMMIT", "END", "ROLLBACK", "ABORT",
		"BEGIN ISOLATION LEVEL SERIALIZABLE",
		"BEGIN READ ONLY",
		"START TRANSACTION ISOLATION LEVEL READ COMMITTED",
		"SET x = 1",
		"SET x TO 'v'",
		"SET SESSION x = 1",
		"SET LOCAL x = off",
		"SET search_path TO a, b",
		"SHOW x", "SHOW ALL", "RESET x", "RESET ALL",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
