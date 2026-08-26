package sqlparser

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestVShapes(t *testing.T) {
	parser.RouteBatch = RouteBatch
	defer func() { parser.RouteBatch = nil }()
	for _, q := range []string{
		"CREATE VIEW v AS SELECT 1",
		"CREATE OR REPLACE VIEW s.v (a, b) AS SELECT 1, 2",
		"CREATE TEMP VIEW v AS SELECT 1",
	} {
		sts, err := parser.Parse(q)
		if err != nil {
			fmt.Printf("%q ERR %v\n", q, err)
			continue
		}
		cv := any(sts[0]).(*parser.CreateViewStmt)
		fmt.Printf("%q -> OrReplace=%v Temp=%v Name=%s Cols=%v Raw=%q\n", q, cv.OrReplace, cv.Temporary, cv.Name.Name, cv.Columns, cv.RawDef)
	}
}
