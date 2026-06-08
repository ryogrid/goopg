package parser

import (
	"fmt"
	"testing"
)

func TestPartBoundParse(t *testing.T) {
	sqls := []string{
		"CREATE TABLE p (a int, b int) PARTITION BY RANGE (((a, b)))",
		"CREATE TABLE p (a int, b int) PARTITION BY RANGE (a, ('unknown'))",
		"CREATE TABLE p (a int) PARTITION BY RANGE ((42))",
		"CREATE TABLE p (a int) PARTITION BY RANGE ((avg(a)))",
	}
	for _, sql := range sqls {
		stmts, err := Parse(sql)
		if err != nil {
			fmt.Printf("parse error for %q: %v\n", sql[:40], err)
			continue
		}
		for _, stmt := range stmts {
			ct, ok := stmt.(*CreateTableStmt)
			if !ok { continue }
			if ct.PartitionBy != nil {
				for i, e := range ct.PartitionBy.KeyExprs {
					fmt.Printf("  SQL: %s  [%d] type=%T\n", sql[:40], i, e)
				}
				for i, k := range ct.PartitionBy.KeyCols {
					fmt.Printf("  SQL: %s  col[%d]=%q\n", sql[:40], i, k)
				}
			}
		}
	}
}
