package parser

import (
	"fmt"
	"testing"
)

// TestCreatePublicationColumnList pins gram.y's `TABLE relation_expr
// opt_column_list`: each table carries its own column list (nil for none).
func TestCreatePublicationColumnList(t *testing.T) {
	stmts, err := Parse("CREATE PUBLICATION p FOR TABLE t1 (a, b), t2, TABLE t3 (c) WITH (publish_generated_columns = none)")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*CreatePublicationStmt)
	if len(s.Tables) != 3 || fmt.Sprint(s.TableColumns) != "[[a b] [] [c]]" {
		t.Fatalf("tables %v columns %v", s.Tables, s.TableColumns)
	}
	if s.TableColumns[1] != nil {
		t.Fatalf("t2 without a column list must carry nil, got %v", s.TableColumns[1])
	}
	if _, err := Parse("CREATE PUBLICATION p FOR TABLE t1 (a,"); err == nil {
		t.Fatal("an unterminated column list must not parse")
	}
}
