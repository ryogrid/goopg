package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

// TestFKDelete_RR_RaisesSerializationOnConcurrentChildInsert pins
// M0100-0005w: when a parent DELETE under REPEATABLE READ would touch
// (CASCADE / SET NULL) or be guarded against (NO ACTION) by a child row
// inserted by a concurrent in-flight transaction that the deleter cannot
// see in its snapshot, the deleter must (a) block until the inserter
// settles, and (b) on commit, surface SQLSTATE 40001 "could not
// serialize access due to concurrent update" — matching upstream's
// RI_FKey_*_del crosscheck-snapshot serialization error path.
//
// Mirrors `fk-snapshot.spec` permutations
// `s2ip2 s1brr s1ifp2 s2brr s2dp2 s1c s2c` (CASCADE leg) and
// `s2ip2 s1brr s1ifn2 s2brr s2dp2 s1c s2c` (SET NULL leg).
func TestFKDelete_RR_RaisesSerializationOnConcurrentChildInsert(t *testing.T) {
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
		`DROP TABLE IF EXISTS fkchild_cascade`,
		`DROP TABLE IF EXISTS fk_parent`,
		`CREATE TABLE fk_parent (a INTEGER PRIMARY KEY)`,
		`CREATE TABLE fkchild_cascade (a INTEGER REFERENCES fk_parent ON DELETE CASCADE)`,
		`INSERT INTO fk_parent VALUES (2)`,
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

	// s1: insert a child row that references parent a=2.
	if _, err := s1.ExecContext(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ`); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.ExecContext(ctx, `INSERT INTO fkchild_cascade VALUES (2)`); err != nil {
		t.Fatal(err)
	}

	// s2: open RR transaction with a snapshot that does NOT see s1's
	// in-flight child INSERT, then DELETE the parent row. The deleter
	// must block on s1.
	if _, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ`); err != nil {
		t.Fatal(err)
	}
	s2Done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s2.ExecContext(ctx, `DELETE FROM fk_parent WHERE a = 2`)
		s2Done <- err
	}()

	select {
	case err := <-s2Done:
		t.Fatalf("s2 DELETE returned before s1 committed (err=%v); FK on-delete wait on in-flight child INSERT not implemented", err)
	case <-time.After(250 * time.Millisecond):
		// Expected: s2 is blocked.
	}

	// Commit s1. s2 should now wake and surface 40001.
	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s2Done:
		if err == nil {
			t.Fatal("s2 DELETE returned nil error; expected SQLSTATE 40001")
		}
		pqErr, ok := err.(*pq.Error)
		if !ok {
			t.Fatalf("s2 DELETE: expected *pq.Error, got %T: %v", err, err)
		}
		if pqErr.Code != "40001" {
			t.Fatalf("s2 DELETE: expected SQLSTATE 40001, got %q (msg=%q)", pqErr.Code, pqErr.Message)
		}
		if !strings.Contains(pqErr.Message, "could not serialize access") {
			t.Fatalf("s2 DELETE: expected message containing 'could not serialize access', got %q", pqErr.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 DELETE did not unblock within 5s of s1 commit")
	}
	// s2 is now in aborted state — COMMIT rolls it back, both fine.
	_, _ = s2.ExecContext(ctx, `ROLLBACK`)
	wg.Wait()
}

// TestFKDelete_RC_CompletesAfterConcurrentChildInsertCommit pins the
// RC complement of M0100-0005w: under READ COMMITTED, the parent
// DELETE still blocks on the in-flight child INSERT, but post-wait
// it refreshes its snapshot and proceeds — for CASCADE, the now-
// committed child row IS deleted; for NO ACTION (the default when
// the FK clause is bare REFERENCES), 23503 is raised.  Mirrors
// upstream's RC FK-trigger behaviour where the FK action runs against
// the up-to-date snapshot rather than aborting with 40001.
func TestFKDelete_RC_CompletesAfterConcurrentChildInsertCommit(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	setup, _ := db.Conn(ctx)
	for _, q := range []string{
		`DROP TABLE IF EXISTS fkchild_rc`,
		`DROP TABLE IF EXISTS fkparent_rc`,
		`CREATE TABLE fkparent_rc (a INTEGER PRIMARY KEY)`,
		`CREATE TABLE fkchild_rc (a INTEGER REFERENCES fkparent_rc ON DELETE CASCADE)`,
		`INSERT INTO fkparent_rc VALUES (2)`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = setup.Close()

	s1, _ := db.Conn(ctx)
	defer s1.Close()
	s2, _ := db.Conn(ctx)
	defer s2.Close()

	if _, err := s1.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.ExecContext(ctx, `INSERT INTO fkchild_rc VALUES (2)`); err != nil {
		t.Fatal(err)
	}

	if _, err := s2.ExecContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`); err != nil {
		t.Fatal(err)
	}
	s2Done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s2.ExecContext(ctx, `DELETE FROM fkparent_rc WHERE a = 2`)
		s2Done <- err
	}()

	select {
	case err := <-s2Done:
		t.Fatalf("s2 DELETE returned before s1 committed (err=%v); FK on-delete wait not engaged under RC", err)
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := s1.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s2Done:
		if err != nil {
			t.Fatalf("s2 DELETE post-RC-wait: expected nil error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 DELETE did not unblock within 5s of s1 commit (RC)")
	}
	if _, err := s2.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// Both the parent row and the cascaded child row must be gone.
	read, _ := db.Conn(ctx)
	defer read.Close()
	var n int
	if err := read.QueryRowContext(ctx, `SELECT count(*) FROM fkparent_rc`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 parent rows after RC DELETE, got %d", n)
	}
	if err := read.QueryRowContext(ctx, `SELECT count(*) FROM fkchild_rc`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 child rows after CASCADE under RC, got %d", n)
	}
}
