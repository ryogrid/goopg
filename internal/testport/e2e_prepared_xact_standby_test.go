package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestE2E_PreparedXactSurvivesPromotion: a goopg standby that replays a
// PREPARE keeps the prepared transaction (PrepareRedoAdd). Its rows stay
// invisible on the standby and after promotion, and once promoted the node
// can COMMIT PREPARED (or ROLLBACK PREPARED) it. A prepared transaction the
// primary finished before promotion is not carried over. M0149-0015b.
func TestE2E_PreparedXactSurvivesPromotion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	for _, tc := range []struct {
		name    string
		primary func(t *testing.T) *tapnode.Node
		finish  string
		want    string
	}{
		{"goopg-primary-commit", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, "COMMIT PREPARED 'g2'", "1,2,3"},
		{"goopg-primary-rollback", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, "ROLLBACK PREPARED 'g2'", ""},
		// A PG 18.3 primary is covered by 012's pg18-primary pairing: in this
		// shorter script the goopg standby does not see the table at all
		// (M0149-0015d), which is not what this test is about.
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.primary(t)
			p.AppendConf("max_prepared_transactions = 10")
			p.Start()
			bkp := p.Backup("b")
			// Created after the backup and before the standby starts, as 012
			// does.
			p.SafePSQL("postgres", "CREATE TABLE q (id int)")
			s := tapnode.InitFromBackupGoopg(t, repo, "standby", p, bkp, true)
			s.Start()
			// g1 is prepared and finished on the primary: nothing to carry.
			p.SafePSQL("postgres", "BEGIN; INSERT INTO q VALUES (100); PREPARE TRANSACTION 'g1';")
			p.SafePSQL("postgres", "ROLLBACK PREPARED 'g1'")
			p.SafePSQL("postgres", `BEGIN;
				INSERT INTO q VALUES (1);
				SAVEPOINT a;
				INSERT INTO q VALUES (2);
				SAVEPOINT b;
				INSERT INTO q VALUES (3);
				PREPARE TRANSACTION 'g2';`)
			waitForCatchup(t, p, s)
			if got := s.SafePSQL("postgres", "SELECT count(*) FROM q"); got != "0" {
				t.Fatalf("standby sees prepared rows: %s", got)
			}
			p.Stop(tapnode.StopFast)
			s.Promote()
			if got := s.SafePSQL("postgres", "SELECT count(*) FROM q"); got != "0" {
				t.Fatalf("promoted node sees prepared rows before finishing: %s", got)
			}
			if _, _, rc := s.PSQLExit("postgres", "COMMIT PREPARED 'g1'"); rc == 0 {
				t.Fatal("g1 was finished on the primary but the promoted node still had it")
			}
			s.SafePSQL("postgres", tc.finish)
			if got := s.SafePSQL("postgres", "SELECT coalesce(string_agg(id::text, ',' ORDER BY id), '') FROM q"); got != tc.want {
				t.Fatalf("after %s: rows %q, want %q", tc.finish, got, tc.want)
			}
			s.Stop(tapnode.StopFast)
		})
	}
}
