package server

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestPGHeapEncodingPreservesTextLikeInsertCoercions(t *testing.T) {
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
		`CREATE TABLE pgenc_varchar (id int PRIMARY KEY, v varchar(1))`,
		`CREATE TABLE pgenc_char (id int PRIMARY KEY, v char)`,
		`INSERT INTO pgenc_varchar VALUES (1, '1'), (2, 2), (3, ''), (4, 'c     ')`,
		`INSERT INTO pgenc_char VALUES (1, '1'), (2, 2), (3, ''), (4, 'c     ')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	gotVarchar := map[int]sql.NullString{}
	rows, err := db.QueryContext(ctx, `SELECT id, v FROM pgenc_varchar ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		var v sql.NullString
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		gotVarchar[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if gotVarchar[1].String != "1" || !gotVarchar[1].Valid {
		t.Fatalf("varchar id=1 got %v, want \"1\"", gotVarchar[1])
	}
	if gotVarchar[2].String != "2" || !gotVarchar[2].Valid {
		t.Fatalf("varchar id=2 got %v, want \"2\"", gotVarchar[2])
	}
	if gotVarchar[4].String != "c" || !gotVarchar[4].Valid {
		t.Fatalf("varchar id=4 got %v, want \"c\"", gotVarchar[4])
	}
	if v := gotVarchar[3]; v.Valid && v.String != "" {
		t.Fatalf("varchar id=3 got %v, want empty-string-ish row", v)
	}

	gotChar := map[int]sql.NullString{}
	rows, err = db.QueryContext(ctx, `SELECT id, v FROM pgenc_char ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		var v sql.NullString
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		gotChar[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if gotChar[1].String != "1" || !gotChar[1].Valid {
		t.Fatalf("char id=1 got %v, want \"1\"", gotChar[1])
	}
	if gotChar[2].String != "2" || !gotChar[2].Valid {
		t.Fatalf("char id=2 got %v, want \"2\"", gotChar[2])
	}
	if gotChar[4].String != "c" || !gotChar[4].Valid {
		t.Fatalf("char id=4 got %v, want \"c\"", gotChar[4])
	}
	if v := gotChar[3]; v.Valid && v.String != "" {
		t.Fatalf("char id=3 got %v, want empty-string-ish row", v)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO pgenc_varchar VALUES (5, 'cd')`); err == nil {
		t.Fatal("expected varchar(1) insert to fail")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pgenc_char VALUES (5, 'cd')`); err == nil {
		t.Fatal("expected char insert to fail")
	}
}
