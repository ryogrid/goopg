package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestUpsertDoNothing_WaitsForInFlightDelete pins the M0100-0005s fix:
// INSERT … ON CONFLICT DO NOTHING must wait for an in-flight DELETE on a
// visible matching tuple before deciding the action. Without the wait,
// the visible-being-deleted tuple is mistaken for a settled conflict and
// the INSERT silently does nothing — but if the deleter commits, the
// row is gone and DO NOTHING should have inserted the new value.
//
// Repro: s1 DELETEs (id=1) and holds the txn open. s2 INSERTs (1,'new')
// ON CONFLICT DO NOTHING. Without the wait, s2 returns immediately and
// the final table has zero rows after both commit. With the wait, s2
// blocks on s1; after s1 commits, the row is gone, s2 re-probes, finds
// no conflict, and inserts (1,'new').
func TestUpsertDoNothing_WaitsForInFlightDelete(t *testing.T) {
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

	setup, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS up_wait`,
		`CREATE TABLE up_wait (id INTEGER PRIMARY KEY, label TEXT)`,
		`INSERT INTO up_wait VALUES (1, 'old')`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = setup.Close()

	s1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if _, err := s1.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.ExecContext(ctx, `DELETE FROM up_wait WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	// s2: INSERT … ON CONFLICT DO NOTHING for the same key.
	// Must block on s1's in-flight delete.
	s2Done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`); err != nil {
			s2Done <- err
			return
		}
		_, err := s2.ExecContext(ctx,
			`INSERT INTO up_wait VALUES (1, 'new') ON CONFLICT DO NOTHING`)
		s2Done <- err
	}()

	// While s1 is still in flight, s2's INSERT should be blocked.
	select {
	case err := <-s2Done:
		t.Fatalf("s2 INSERT returned before s1 committed (err=%v); waiting on xmax not implemented", err)
	case <-time.After(250 * time.Millisecond):
		// Expected: s2 is blocked.
	}

	// Commit s1; s2 should now finish quickly.
	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s2Done:
		if err != nil {
			t.Fatalf("s2 INSERT after s1 commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 INSERT did not unblock within 5s of s1 commit")
	}
	if _, err := s2.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// The old row was deleted, the new row should now exist. Final
	// table state on a fresh connection: exactly one row (1,'new').
	read, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	rows, err := read.QueryContext(ctx, `SELECT id, label FROM up_wait ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type pair struct {
		id int
		v  string
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.v); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row after both commits, got %d: %v", len(got), got)
	}
	if got[0].id != 1 || got[0].v != "new" {
		t.Fatalf("expected (1,'new'), got (%d,%q)", got[0].id, got[0].v)
	}
}
