package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAtShapes(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE t ADD COLUMN c int",
		"ALTER TABLE t DROP COLUMN c",
		"ALTER TABLE t ALTER COLUMN a TYPE bigint",
		"ALTER TABLE t RENAME TO t2",
		"ALTER TABLE t ADD PRIMARY KEY (a)",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			t.Logf("%q ERR %v", q, err)
			continue
		}
		fmt.Printf("%q -> %s\n", q, dumpStmts(sts))
	}
}
