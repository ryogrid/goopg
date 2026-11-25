package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestE2E_PreparedXactSurvivesRestart: a prepared transaction (with a
// subtransaction) survives a restart, stays invisible until finished, and
// COMMIT/ROLLBACK PREPARED finishes it from a new session. The fast stop
// leaves its state in a pg_twophase file (the shutdown checkpoint passed its
// PREPARE); the immediate stop leaves only the PREPARE record, restored from
// WAL. After the restart, new xids must not reuse the prepared
// transaction's subxids (012's "nextXid handling" check). M0149-0015a.
func TestE2E_PreparedXactSurvivesRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	for _, tc := range []struct {
		name   string
		stop   tapnode.StopMode
		finish string
		want   string
	}{
		{"fast-stop-commit", tapnode.StopFast, "COMMIT PREPARED 'g1'", "1,2"},
		{"immediate-stop-rollback", tapnode.StopImmediate, "ROLLBACK PREPARED 'g1'", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := tapnode.NewGoopg(t, repo, "node")
			n.AppendConf("max_prepared_transactions = 10")
			n.Start()
			n.SafePSQL("postgres", "CREATE TABLE p (id int)")
			n.SafePSQL("postgres", `BEGIN;
				INSERT INTO p VALUES (1);
				SAVEPOINT s1;
				INSERT INTO p VALUES (2);
				PREPARE TRANSACTION 'g1';`)
			n.Stop(tc.stop)
			n.Start()
			if got := n.SafePSQL("postgres", "SELECT count(*) FROM p"); got != "0" {
				t.Fatalf("prepared rows visible before finishing: %s", got)
			}
			// A rolled-back write takes new xids; it must not land on the
			// prepared transaction's subxid.
			n.SafePSQL("postgres", "BEGIN; INSERT INTO p VALUES (142); ROLLBACK;")
			n.SafePSQL("postgres", tc.finish)
			if _, _, rc := n.PSQLExit("postgres", tc.finish); rc == 0 {
				t.Fatal("finishing twice succeeded")
			}
			// The finish stamps the subtransaction too (TransactionIdCommitTree):
			// with no read in between to set hint bits, a checkpoint past the
			// finish record and a crash restart — whose clog replay starts
			// after it — must still show the savepoint row (M0149-0016a).
			n.SafePSQL("postgres", "CHECKPOINT")
			n.Stop(tapnode.StopImmediate)
			n.Start()
			if got := n.SafePSQL("postgres", "SELECT coalesce(string_agg(id::text, ',' ORDER BY id), '') FROM p"); got != tc.want {
				t.Fatalf("after %s, checkpoint and crash restart: rows %q, want %q", tc.finish, got, tc.want)
			}
			n.Stop(tapnode.StopFast)
		})
	}
}
