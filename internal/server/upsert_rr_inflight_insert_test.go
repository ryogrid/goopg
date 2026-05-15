package server

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestUpsertDoNothing_RR_RaisesSerializationOnInFlightInsertCommit pins
// M0100-0005x: under REPEATABLE READ / SERIALIZABLE isolation, an INSERT
// … ON CONFLICT DO NOTHING that waits on an in-flight inserter for the
// same conflict key must surface 40001 ("could not serialize access due
// to concurrent update") once the inserter commits.  Without the raise,
// our frozen snapshot reports "no conflict" (the committed insert is
// invisible to a snapshot taken before its commit) and we silently slip
// into INSERT, producing a duplicate unique-key violation downstream or
// (under DO NOTHING) silently no-op'ing in a way that diverges from
// upstream `_bt_check_unique`.
//
// This is the partition-key-update-3 RR/SER permutation pattern (and the
// generic "RR DO NOTHING on in-flight insert" pattern): s1 starts an
// INSERT VALUES(1,...), holds it in-flight, s2 (RR) tries to INSERT
// VALUES(1,...) ON CONFLICT DO NOTHING.  s2 must block on s1's xmin and
// then return 40001 when s1 commits.
func TestUpsertDoNothing_RR_RaisesSerializationOnInFlightInsertCommit(t *testing.T) {
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
		`DROP TABLE IF EXISTS up_rr_wait`,
		`CREATE TABLE up_rr_wait (id INTEGER PRIMARY KEY, label TEXT)`,
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

	// s2 begins RR FIRST so its snapshot is taken before s1's INSERT
	// xmin is materialised.  Then s1 starts its in-flight INSERT.
	if _, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ`); err != nil {
		t.Fatal(err)
	}
	// Force snapshot materialisation by reading from the table.
	if _, err := s2.ExecContext(ctx, `SELECT * FROM up_rr_wait`); err != nil {
		t.Fatal(err)
	}

	if _, err := s1.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.ExecContext(ctx, `INSERT INTO up_rr_wait VALUES (1, 's1')`); err != nil {
		t.Fatal(err)
	}

	// s2: INSERT … ON CONFLICT DO NOTHING for the same key.  Must block
	// on s1's in-flight xmin and then surface 40001 once s1 commits.
	s2Done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s2.ExecContext(ctx,
			`INSERT INTO up_rr_wait VALUES (1, 's2') ON CONFLICT DO NOTHING`)
		s2Done <- err
	}()

	// While s1 is still in flight, s2's INSERT should be blocked.
	select {
	case err := <-s2Done:
		t.Fatalf("s2 INSERT returned before s1 committed (err=%v); waiting on xmin not implemented", err)
	case <-time.After(250 * time.Millisecond):
		// Expected: s2 is blocked.
	}

	// Commit s1; s2 should now finish quickly with a 40001 error.
	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s2Done:
		if err == nil {
			t.Fatalf("s2 INSERT after s1 commit: expected 40001, got nil error")
		}
		if !strings.Contains(err.Error(), "could not serialize access due to concurrent update") {
			t.Fatalf("s2 INSERT error: want 40001 serialization, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 INSERT did not unblock within 5s of s1 commit")
	}
	// s2's xact is aborted by the serialization error; ROLLBACK clears it.
	_, _ = s2.ExecContext(ctx, `ROLLBACK`)
	wg.Wait()
}

// TestUpsertDoNothing_RC_DoesNotRaiseSerializationOnInFlightInsertCommit
// pins the negative case: under READ COMMITTED, the same pattern as
// TestUpsertDoNothing_RR_RaisesSerializationOnInFlightInsertCommit must
// NOT raise 40001.  s2 should re-probe with a fresh snapshot, see the
// committed row, and silently DO NOTHING.
func TestUpsertDoNothing_RC_DoesNotRaiseSerializationOnInFlightInsertCommit(t *testing.T) {
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
		`DROP TABLE IF EXISTS up_rc_wait`,
		`CREATE TABLE up_rc_wait (id INTEGER PRIMARY KEY, label TEXT)`,
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
	if _, err := s1.ExecContext(ctx, `INSERT INTO up_rc_wait VALUES (1, 's1')`); err != nil {
		t.Fatal(err)
	}

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
			`INSERT INTO up_rc_wait VALUES (1, 's2') ON CONFLICT DO NOTHING`)
		s2Done <- err
	}()

	select {
	case err := <-s2Done:
		t.Fatalf("s2 INSERT returned before s1 committed (err=%v)", err)
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s2Done:
		if err != nil {
			t.Fatalf("RC s2 INSERT should NOT raise 40001 after s1 commit, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 INSERT did not unblock within 5s of s1 commit")
	}
	if _, err := s2.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// Verify the row exists exactly once with s1's value (DO NOTHING
	// skipped the duplicate).
	read, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	var id int
	var label string
	if err := read.QueryRowContext(ctx, `SELECT id, label FROM up_rc_wait WHERE id=1`).Scan(&id, &label); err != nil {
		t.Fatal(err)
	}
	if id != 1 || label != "s1" {
		t.Fatalf("expected (1,'s1'), got (%d,%q)", id, label)
	}
}
