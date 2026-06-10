package parser_test

import (
    "testing"
    "github.com/goopg/goopg/internal/parser"
)

func TestPartitionKeyExprs(t *testing.T) {
    tests := []string{
        "CREATE TABLE t (a int, b int) PARTITION BY RANGE (a, (b+1))",
        "CREATE TABLE t (a int, b int, c int) PARTITION BY RANGE (abs(a), abs(b), c)",
        "CREATE TABLE t (a text) PARTITION BY RANGE (a COLLATE \"POSIX\")",
        "CREATE TABLE fail_part PARTITION OF t3 FOR VALUES FROM (0, minvalue) TO (0, 1)",
        "CREATE TABLE fail_part PARTITION OF t4 FOR VALUES FROM (MINVALUE, MINVALUE, MINVALUE) TO (MAXVALUE, MAXVALUE, MAXVALUE)",
    }
    for _, sql := range tests {
        stmts, err := parser.Parse(sql)
        if err != nil {
            t.Logf("PARSE ERROR: %s -> %v", sql, err)
        } else {
            t.Logf("OK: %s -> %T", sql, stmts[0])
        }
    }
}
