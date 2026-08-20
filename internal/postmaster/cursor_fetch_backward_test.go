package postmaster

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
)

// TestCursorFetchBackwardExcludesCurrentRow verifies the M0134-0056 fix for
// finite `FETCH BACKWARD n`: it must return the n rows strictly BEFORE the
// cursor's current row and must NOT re-return the row that the preceding
// forward fetch already returned.
//
// Mirrors the PG oracle regress case (postgres/src/test/regress/sql/portals.sql,
// foo22/foo23):
//   - `FETCH 23` then `FETCH BACKWARD 1` returns exactly 1 row: the row just
//     before the current one (index total-2), not the current row itself
//     (index total-1).
//   - `FETCH 22` then `FETCH BACKWARD 2` returns rows at index total-2 then
//     total-3, in that (nearest-first) order.
func TestCursorFetchBackwardExcludesCurrentRow(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addrHost(addr) + " port=" + addrPort(addr) + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE cur_bwd_test(id int primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer db.ExecContext(ctx, `DROP TABLE IF EXISTS cur_bwd_test`) //nolint:errcheck
	for i := 0; i < 30; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO cur_bwd_test(id) VALUES ($1)`, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	t.Run("FETCH_23_then_BACKWARD_1", func(t *testing.T) {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn: %v", err)
		}
		defer conn.Close()

		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck

		if _, err := conn.ExecContext(ctx, "DECLARE c23 CURSOR FOR SELECT id FROM cur_bwd_test ORDER BY id"); err != nil {
			t.Fatalf("DECLARE: %v", err)
		}
		rows, err := conn.QueryContext(ctx, "FETCH 23 FROM c23")
		if err != nil {
			t.Fatalf("FETCH 23: %v", err)
		}
		var got []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, id)
		}
		rows.Close()
		if len(got) != 23 {
			t.Fatalf("FETCH 23 returned %d rows, want 23", len(got))
		}
		if last := got[len(got)-1]; last != 22 {
			t.Fatalf("FETCH 23 last row id = %d, want 22", last)
		}

		rows, err = conn.QueryContext(ctx, "FETCH BACKWARD 1 FROM c23")
		if err != nil {
			t.Fatalf("FETCH BACKWARD 1: %v", err)
		}
		var back []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			back = append(back, id)
		}
		rows.Close()
		if len(back) != 1 {
			t.Fatalf("FETCH BACKWARD 1 returned %d rows, want 1: %v", len(back), back)
		}
		if back[0] != 21 {
			t.Fatalf("FETCH BACKWARD 1 returned id=%d, want 21 (not the current row 22)", back[0])
		}
	})

	t.Run("FETCH_22_then_BACKWARD_2", func(t *testing.T) {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn: %v", err)
		}
		defer conn.Close()

		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck

		if _, err := conn.ExecContext(ctx, "DECLARE c22 CURSOR FOR SELECT id FROM cur_bwd_test ORDER BY id"); err != nil {
			t.Fatalf("DECLARE: %v", err)
		}
		if rows, err := conn.QueryContext(ctx, "FETCH 22 FROM c22"); err != nil {
			t.Fatalf("FETCH 22: %v", err)
		} else {
			rows.Close()
		}

		rows, err := conn.QueryContext(ctx, "FETCH BACKWARD 2 FROM c22")
		if err != nil {
			t.Fatalf("FETCH BACKWARD 2: %v", err)
		}
		var back []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			back = append(back, id)
		}
		rows.Close()
		if len(back) != 2 {
			t.Fatalf("FETCH BACKWARD 2 returned %d rows, want 2: %v", len(back), back)
		}
		if back[0] != 20 || back[1] != 19 {
			t.Fatalf("FETCH BACKWARD 2 returned %v, want [20 19]", back)
		}
	})
}
