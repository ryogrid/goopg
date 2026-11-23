package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestE2E_StandbyCrashKeepsCommitsBeforePrimaryCheckpoint: a standby replays
// from its own last restartpoint after a crash (pg_control's redo), not from
// the newest checkpoint record in the WAL it received. Here the standby's
// restartpoint precedes a commit, the primary then checkpoints, and the
// standby is crashed: the commit's clog status existed only in the standby's
// memory, and replay starting at the primary's checkpoint left it unknown, so
// the startup sweep stamped it aborted and the row vanished. M0149-0016b.
func TestE2E_StandbyCrashKeepsCommitsBeforePrimaryCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	p := tapnode.NewGoopg(t, repo, "primary")
	p.Start()
	bkp := p.Backup("b")
	s := tapnode.InitFromBackupGoopg(t, repo, "standby", p, bkp, true)
	s.Start()
	p.SafePSQL("postgres", "CREATE TABLE c (id int)")
	p.SafePSQL("postgres", "INSERT INTO c VALUES (1)")
	waitForCatchup(t, p, s)
	s.SafePSQL("postgres", "CHECKPOINT") // restartpoint before the commit below
	p.SafePSQL("postgres", "BEGIN; INSERT INTO c VALUES (2); SAVEPOINT a; INSERT INTO c VALUES (3); COMMIT;")
	p.SafePSQL("postgres", "CHECKPOINT")
	waitForCatchup(t, p, s)
	if got := s.SafePSQL("postgres", "SELECT string_agg(id::text, ',' ORDER BY id) FROM c"); got != "1,2,3" {
		t.Fatalf("standby before crash: %q", got)
	}
	s.Stop(tapnode.StopImmediate)
	s.Start()
	waitForCatchup(t, p, s)
	if got := s.SafePSQL("postgres", "SELECT string_agg(id::text, ',' ORDER BY id) FROM c"); got != "1,2,3" {
		t.Fatalf("standby after crash restart sees %q, want 1,2,3", got)
	}
	s.Stop(tapnode.StopFast)
	p.Stop(tapnode.StopFast)
}
