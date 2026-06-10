package main

import (
	"fmt"
	"github.com/goopg/goopg/internal/parser"
)

func main() {
	tests := []string{
		"SET LOCAL SESSION AUTHORIZATION regress_seq_user",
		"ALTER SEQUENCE sequence_test_unlogged SET UNLOGGED",
		"START TRANSACTION READ ONLY",
	}
	for _, sql := range tests {
		stmts, err := parser.Parse(sql + ";")
		if err != nil {
			fmt.Printf("PARSE ERROR for %q: %v\n", sql, err)
		} else {
			fmt.Printf("OK for %q: %T %+v\n", sql, stmts[0], stmts[0])
		}
	}
}
