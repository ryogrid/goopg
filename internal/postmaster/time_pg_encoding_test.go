package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestPGHeapEncodingPreservesTimeRows(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	dsn := "host=" + addr[:colonIdx] + " port=" + addr[colonIdx+1:] + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE time_pgenc (f1 time(2))`,
		`INSERT INTO time_pgenc VALUES ('00:00')`,
		`INSERT INTO time_pgenc VALUES ('02:03 PST')`,
		`INSERT INTO time_pgenc VALUES ('11:59:59.99 PM')`,
		`INSERT INTO time_pgenc VALUES ('2003-03-07 15:36:39 America/New_York')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM time_pgenc`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("count=%d, want 4", count)
	}

	rows, err := db.QueryContext(ctx, `SELECT f1::text FROM time_pgenc ORDER BY f1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"00:00:00", "02:03:00", "15:36:39", "23:59:59.99"}
	if len(got) != len(want) {
		t.Fatalf("rows=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows=%v, want %v", got, want)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO time_pgenc VALUES ('15:36:39 America/New_York')`); err == nil {
		t.Fatal("expected bare named timezone insert to fail")
	}
}
