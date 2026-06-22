package testport

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_TimeoutsRowLevel is a focused port of the row-level half of
// postgres/src/test/isolation/specs/timeouts.spec (the wrtbl + update
// permutations). One session holds an uncommitted UPDATE on a row; a second
// session's DELETE of that row must block on the row lock and then be aborted
// by whichever of lock_timeout / statement_timeout fires first, with the
// matching PostgreSQL error message.
//
// The table-level half (rdtbl + locktbl) is covered by
// TestPort_TimeoutsTableLevel. M0118-0009.
func TestPort_TimeoutsRowLevel(t *testing.T) {
	c := newCluster(t, "timeouts_rowlevel")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	dsn := buildDSN(t, c)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := db.ExecContext(ctx, `CREATE TABLE accounts (accountid text PRIMARY KEY, balance numeric not null)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts VALUES ('checking', 600), ('savings', 600)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		setup   []string // statements run on the blocked session before the DELETE
		wantMsg string
	}{
		{
			name:    "statement_timeout",
			setup:   []string{`SET statement_timeout = '300ms'`},
			wantMsg: "canceling statement due to statement timeout",
		},
		{
			name:    "lock_timeout",
			setup:   []string{`SET lock_timeout = '300ms'`},
			wantMsg: "canceling statement due to lock timeout",
		},
		{
			// lock_timeout shorter ⇒ it fires first.
			name:    "lock_timeout_wins",
			setup:   []string{`SET lock_timeout = '300ms'`, `SET statement_timeout = '30s'`},
			wantMsg: "canceling statement due to lock timeout",
		},
		{
			// statement_timeout shorter ⇒ it fires first.
			name:    "statement_timeout_wins",
			setup:   []string{`SET lock_timeout = '30s'`, `SET statement_timeout = '300ms'`},
			wantMsg: "canceling statement due to statement timeout",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Holder session: hold an uncommitted UPDATE on the 'checking' row.
			holder, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			if _, err := holder.ExecContext(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.ExecContext(ctx, "UPDATE accounts SET balance = balance + 100"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = holder.ExecContext(context.Background(), "ROLLBACK") }()

			// Blocked session: its DELETE conflicts with the holder's xmax.
			blocked, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Close()
			if _, err := blocked.ExecContext(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = blocked.ExecContext(context.Background(), "ROLLBACK") }()
			for _, s := range tc.setup {
				if _, err := blocked.ExecContext(ctx, s); err != nil {
					t.Fatalf("setup %q: %v", s, err)
				}
			}

			start := time.Now()
			_, err = blocked.ExecContext(ctx, "DELETE FROM accounts WHERE accountid = 'checking'")
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("DELETE unexpectedly succeeded; want error %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("DELETE error = %q, want it to contain %q (after %v)", err.Error(), tc.wantMsg, elapsed)
			}
			// The abort must come from the timeout, not instantly nor only at
			// holder rollback: it should land near the 300ms budget.
			if elapsed < 150*time.Millisecond {
				t.Errorf("DELETE aborted after only %v; expected ~300ms timeout", elapsed)
			}
			if elapsed > 20*time.Second {
				t.Errorf("DELETE aborted after %v; the shorter timeout did not fire first", elapsed)
			}
		})
	}
}

// TestPort_TimeoutsTableLevel is the table-level half of
// postgres/src/test/isolation/specs/timeouts.spec (the rdtbl + locktbl
// permutations). One session holds an open transaction that has read a table
// (a plain SELECT, which takes a transaction-scoped ACCESS SHARE heavyweight
// lock); a second session's LOCK TABLE (ACCESS EXCLUSIVE) must block on that
// ACCESS SHARE and then be aborted by whichever of lock_timeout /
// statement_timeout fires first, with the matching PostgreSQL error message.
// M0118-0009 (timeouts table-level half).
func TestPort_TimeoutsTableLevel(t *testing.T) {
	c := newCluster(t, "timeouts_tablelevel")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	dsn := buildDSN(t, c)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := db.ExecContext(ctx, `CREATE TABLE accounts (accountid text PRIMARY KEY, balance numeric not null)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts VALUES ('checking', 600), ('savings', 600)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		setup   []string // statements run on the blocked session before LOCK TABLE
		wantMsg string
	}{
		{
			name:    "statement_timeout",
			setup:   []string{`SET statement_timeout = '300ms'`},
			wantMsg: "canceling statement due to statement timeout",
		},
		{
			name:    "lock_timeout",
			setup:   []string{`SET lock_timeout = '300ms'`},
			wantMsg: "canceling statement due to lock timeout",
		},
		{
			// lock_timeout shorter ⇒ it fires first.
			name:    "lock_timeout_wins",
			setup:   []string{`SET lock_timeout = '300ms'`, `SET statement_timeout = '30s'`},
			wantMsg: "canceling statement due to lock timeout",
		},
		{
			// statement_timeout shorter ⇒ it fires first.
			name:    "statement_timeout_wins",
			setup:   []string{`SET lock_timeout = '30s'`, `SET statement_timeout = '300ms'`},
			wantMsg: "canceling statement due to statement timeout",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Holder session: an open transaction that has read accounts and so
			// holds a transaction-scoped ACCESS SHARE lock on it.
			holder, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			if _, err := holder.ExecContext(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = holder.ExecContext(context.Background(), "ROLLBACK") }()
			if _, err := holder.ExecContext(ctx, "SELECT * FROM accounts"); err != nil {
				t.Fatalf("holder SELECT: %v", err)
			}

			// Blocked session: its LOCK TABLE (ACCESS EXCLUSIVE) conflicts with
			// the holder's ACCESS SHARE.
			blocked, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Close()
			if _, err := blocked.ExecContext(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = blocked.ExecContext(context.Background(), "ROLLBACK") }()
			for _, s := range tc.setup {
				if _, err := blocked.ExecContext(ctx, s); err != nil {
					t.Fatalf("setup %q: %v", s, err)
				}
			}

			start := time.Now()
			_, err = blocked.ExecContext(ctx, "LOCK TABLE accounts")
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("LOCK TABLE unexpectedly succeeded; want error %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("LOCK TABLE error = %q, want it to contain %q (after %v)", err.Error(), tc.wantMsg, elapsed)
			}
			if elapsed < 150*time.Millisecond {
				t.Errorf("LOCK TABLE aborted after only %v; expected ~300ms timeout", elapsed)
			}
			if elapsed > 20*time.Second {
				t.Errorf("LOCK TABLE aborted after %v; the shorter timeout did not fire first", elapsed)
			}
		})
	}
}
