package postmaster

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// TestTableoidRegclass_NonPartitioned pins the M0100-0005y planner +
// executor wiring for the `tableoid` system column on a plain
// (non-partitioned) base relation. resolveColumnRefAt synthesises a
// constant TableOidExpr against the binding's table OID; the executor
// emits an `oid` Datum and the `::regclass` cast resolves it to the
// relation name via catalog.LookupTableByOID.
//
// Pre-fix, `SELECT tableoid::regclass FROM t` failed at parse-time
// resolution with `ERROR:  column "tableoid" does not exist` (42703)
// because lookupColumn rejected the system column name.
func TestTableoidRegclass_NonPartitioned(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, q := range []string{
		`DROP TABLE IF EXISTS toid_plain`,
		`CREATE TABLE toid_plain (a int, b text)`,
		`INSERT INTO toid_plain VALUES (1, 'x'), (2, 'y')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	// Both qualifications must resolve.
	for _, q := range []string{
		`SELECT tableoid::regclass FROM toid_plain LIMIT 1`,
		`SELECT toid_plain.tableoid::regclass FROM toid_plain LIMIT 1`,
	} {
		var name string
		if err := db.QueryRowContext(ctx, q).Scan(&name); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if name != "toid_plain" {
			t.Fatalf("%s: got %q want %q", q, name, "toid_plain")
		}
	}
}

// TestRegclassCast_ParamLookup pins the pgbench catalog probe shape:
// `SELECT relkind FROM pg_catalog.pg_class WHERE oid = $1::pg_catalog.regclass`.
// Pre-fix, the CastExpr path left text input unchanged, so the WHERE clause
// compared pg_class.oid against the relation name and returned zero rows.
func TestRegclassCast_ParamLookup(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, q := range []string{
		`DROP TABLE IF EXISTS pgbench_branches`,
		`CREATE TABLE pgbench_branches (bid int)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	var relkind string
	if err := db.QueryRowContext(ctx,
		`SELECT relkind FROM pg_catalog.pg_class WHERE oid = $1::pg_catalog.regclass`,
		"pgbench_branches").Scan(&relkind); err != nil {
		t.Fatalf("pgbench relkind probe: %v", err)
	}
	if relkind != "r" {
		t.Fatalf("relkind=%q want %q", relkind, "r")
	}
}

// TestTableoidColumnName pins the RowDescription field NAME for a bare
// `tableoid` system-column projection. resolveColumnRefAt lowers
// `tableoid` on a non-partitioned base relation to a constant
// TableOidExpr; pre-fix the planner's targetMeta had no case for that
// node so the output column was labelled `?column?` instead of
// `tableoid`. pg_dump's getNamespaces does PQfnumber(res,"tableoid")
// and segfaults (SIGSEGV) when that returns -1, so the label is
// load-bearing for pg_dump parity (DU-002 slice 3). The value path is
// already covered by TestTableoidRegclass_NonPartitioned.
func TestTableoidColumnName(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, q := range []string{
		`DROP TABLE IF EXISTS toid_name`,
		`CREATE TABLE toid_name (a int, b text)`,
		`INSERT INTO toid_name VALUES (1, 'x')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	// Both bare and table-qualified references must label the field
	// `tableoid` in the RowDescription.
	for _, q := range []string{
		`SELECT tableoid FROM toid_name`,
		`SELECT toid_name.tableoid FROM toid_name`,
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("%s: Columns: %v", q, err)
		}
		rows.Close()
		if len(cols) != 1 || cols[0] != "tableoid" {
			t.Fatalf("%s: column names = %v, want [tableoid]", q, cols)
		}
	}
}

// TestTableoidRegclass_Partitioned pins the per-leaf partition union
// wrapping (planFromTable's partition arm calling wrapWithTableoid)
// plus the rangeBinding.tableOidColIdx accounting that makes
// `SELECT tableoid::regclass, * FROM <partitioned> ORDER BY a`
// report each row's actual leaf relname rather than the
// partitioned-parent name. This is the M0100-0005y core test:
// without the per-leaf wrap, a partitioned-table query reports
// the parent's OID for every row (so all rows appear under
// `<parent>` in the cast output) and `partition-key-update-4`
// fails its first parity check.
func TestTableoidRegclass_Partitioned(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, q := range []string{
		`DROP TABLE IF EXISTS toid_part`,
		`CREATE TABLE toid_part (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE toid_part1 PARTITION OF toid_part FOR VALUES IN (1)`,
		`CREATE TABLE toid_part2 PARTITION OF toid_part FOR VALUES IN (2)`,
		`INSERT INTO toid_part VALUES (1, 'one')`,
		`INSERT INTO toid_part VALUES (2, 'two')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	rows, err := db.QueryContext(ctx,
		`SELECT tableoid::regclass, a, b FROM toid_part ORDER BY a`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	type record struct {
		oid string
		a   int
		b   string
	}
	got := make([]record, 0, 2)
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.oid, &r.a, &r.b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].a < got[j].a })
	want := []record{
		{oid: "toid_part1", a: 1, b: "one"},
		{oid: "toid_part2", a: 2, b: "two"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %d (%+v), want %d (%+v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
