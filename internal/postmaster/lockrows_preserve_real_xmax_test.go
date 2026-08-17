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

// TestForKeyShare_PreservesRealUpdaterXmax pins the M0100-0005f fix:
// when a row's xmax is already a real (non-lock-only) updater stamped
// by an in-flight UPDATE on session 1, a concurrent SELECT … FOR KEY
// SHARE on session 2 must NOT overwrite that xmax with its lock-only
// stamp. Otherwise, after s1 commits, the dead old version remains
// visible on subsequent SELECTs because TupleVisible's lock-only
// branch short-circuits the xmax check.
//
// Symptom without the fix: lock-committed-update spec produces two
// rows (1|one + 1|two) for s1hint after the commit; expected one
// (1|two).
func TestForKeyShare_PreservesRealUpdaterXmax(t *testing.T) {
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
		`DROP TABLE IF EXISTS lcu_unit`,
		`CREATE TABLE lcu_unit (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO lcu_unit VALUES (1, 'one')`,
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
	if _, err := s1.ExecContext(ctx, `UPDATE lcu_unit SET value='two' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	// s2: FOR KEY SHARE on the row whose xmax is now stamped with
	// s1's update XID. Run in a separate goroutine because in some
	// schedules KEY SHARE may briefly wait for the updater; with our
	// non-conflicting key (only `value` changed), it should proceed
	// quickly. The test enforces both: no deadlock within 5s, and
	// the post-commit SELECT shows exactly one row.
	s2Done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`)
		if err != nil {
			s2Done <- err
			return
		}
		_, err = s2.ExecContext(ctx, `SELECT * FROM lcu_unit WHERE id=1 FOR KEY SHARE`)
		s2Done <- err
	}()

	select {
	case err := <-s2Done:
		if err != nil {
			t.Fatalf("s2 KEY SHARE: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 KEY SHARE did not return within 5s")
	}

	// Commit s1; the row's xmax should still hold s1's XID, so the
	// old version becomes invisible to all future snapshots.
	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	// s2 commits and releases its KEY SHARE.
	if _, err := s2.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// Read on a fresh connection; must see exactly one row "1|two".
	read, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	rows, err := read.QueryContext(ctx, `SELECT id, value FROM lcu_unit`)
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
		t.Fatalf("expected 1 row after commit, got %d: %v", len(got), got)
	}
	if got[0].v != "two" {
		t.Fatalf("expected value=two, got %q", got[0].v)
	}
}
