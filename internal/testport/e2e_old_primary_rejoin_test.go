package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestE2E_OldPrimaryRejoinsAfterPromotion is the role swap that
// recovery/t/012_subtransactions.pl (and 009_twophase.pl) do twice: stop the
// primary (fast), promote its standby, then restart the old primary as a
// standby of the promoted node, with no pg_rewind. It works only if the
// stopped primary's last WAL — its shutdown checkpoint — reached the standby
// before the primary exited (walsender WalSndDone), and if a promoted node
// that had followed a newer timeline chains its history through that
// timeline. M0149-0015c. Run all-goopg and with a goopg primary over a PG 18.3
// standby; each pairing swaps twice, so both engines play both roles.
func TestE2E_OldPrimaryRejoinsAfterPromotion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication E2E in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, goopgStandby},
		// pg18-primary-goopg-standby is left out: the goopg node promoted
		// from a PG primary's backup cannot see the user table at all
		// (M0149-0015d), which is not what this test is about.
		{"goopg-primary-pg18-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := tc.primary(t)
			primary.Start()
			primary.SafePSQL("postgres", "CREATE TABLE r (id int)")
			bkp := primary.Backup("b")
			standby := tc.standby(t, "standby", primary, bkp)
			standby.Start()

			for round, v := range []string{"1", "2"} {
				primary.SafePSQL("postgres", "INSERT INTO r VALUES ("+v+")")
				waitForCatchup(t, primary, standby)
				primary.Stop(tapnode.StopFast)
				standby.Promote()
				primary, standby = standby, primary
				standby.EnableStreaming(primary)
				standby.Start()
				// A write on the new primary must reach the old one.
				primary.SafePSQL("postgres", "INSERT INTO r VALUES (10)")
				waitForCatchup(t, primary, standby)
				got := standby.SafePSQL("postgres", "SELECT string_agg(id::text, ',' ORDER BY id) FROM r")
				wantRows := map[int]string{0: "1,10", 1: "1,2,10,10"}[round]
				if got != wantRows {
					t.Fatalf("round %d: old primary as standby sees %q, want %q", round+1, got, wantRows)
				}
			}
			standby.Stop(tapnode.StopFast)
			primary.Stop(tapnode.StopFast)
		})
	}
}
