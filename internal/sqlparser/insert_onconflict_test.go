package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestInsertOnConflictReturning(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT ON CONSTRAINT c DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2 WHERE t.w > 0",
		"INSERT INTO t VALUES (1) RETURNING *",
		"INSERT INTO t VALUES (1) RETURNING id, v + 1 AS nv",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
