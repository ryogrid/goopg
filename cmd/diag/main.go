// diag is a tiny helper that runs an arbitrary SQL via lib/pq
// against the bench goopg server and prints rows. Used to
// hand-debug TPC-H residuals during M0071-0009 development; safe
// to delete once Phase 1 lands.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
)

func main() {
	q := flag.String("q", "", "SQL query")
	flag.Parse()
	if *q == "" {
		fmt.Fprintln(os.Stderr, "need -q SQL")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", "host=127.0.0.1 port=65433 user=tpch password=tpch dbname=tpch sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	start := time.Now()
	rows, err := db.Query(*q)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	n := 0
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		for i := range vals {
			var v interface{}
			vals[i] = &v
		}
		if err := rows.Scan(vals...); err != nil {
			fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
			os.Exit(1)
		}
		n++
		if n <= 10 {
			fmt.Print("  ")
			for i, v := range vals {
				p := v.(*interface{})
				fmt.Printf("%s=%v ", cols[i], *p)
			}
			fmt.Println()
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "err: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rows=%d elapsed=%v\n", n, time.Since(start))
}
