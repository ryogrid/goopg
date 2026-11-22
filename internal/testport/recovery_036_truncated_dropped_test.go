package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery036TruncatedDropped ports
// postgres/src/test/recovery/t/036_truncated_dropped.pl (M0149-0002): crash
// recovery replaying WAL for relation files that are shorter than the WAL
// expects, because the relation was later dropped, truncated by TRUNCATE, or
// tail-truncated by VACUUM.
//
// Variants (M0149 contract C3, single-node): after every stop('immediate'),
// a PostgreSQL 18.3 server cold-starts on a copy of the crashed goopg
// directory and must recover and answer the scenario's assertions; then the
// goopg node itself restarts (upstream's own step) and answers them.
func TestPort_Recovery036TruncatedDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	node := tapnode.NewGoopg(t, repo, "n1")
	// Disable autovacuum to guarantee VACUUM can remove rows / truncate
	// relations.
	node.AppendConf("wal_level = 'replica'")
	node.AppendConf("autovacuum = off")
	node.Start()

	// crash stops the node immediately, then checks that both a PG 18.3 cold
	// start on a copy and the goopg restart recover (the `ok($node->start(),
	// name)` assertion) and pass check.
	crash := func(name string, check func(t *testing.T, n *tapnode.Node)) {
		t.Helper()
		node.Stop(tapnode.StopImmediate)
		t.Run(name+"/pg18-cold-start", func(t *testing.T) {
			pg := tapnode.ColdStartPG(t, node, "pg-on-goopg-copy")
			if check != nil {
				check(t, pg)
			}
			pg.Stop(tapnode.StopFast)
		})
		node.Start()
		if check != nil {
			check(t, node)
		}
	}
	contents := func(query, want, what string) func(t *testing.T, n *tapnode.Node) {
		return func(t *testing.T, n *tapnode.Node) {
			t.Helper()
			if got := n.SafePSQL("postgres", query); got != want {
				t.Errorf("%s: %s: got %q, want %q", n.Flavor(), what, got, want)
			}
		}
	}

	// Test: Replay of PRUNE records for a pre-existing, then dropped,
	// relation.
	node.SafePSQL("postgres", `
CREATE TABLE truncme(i int) WITH (fillfactor = 50);
INSERT INTO truncme SELECT generate_series(1, 1000);
UPDATE truncme SET i = 1;
CHECKPOINT; -- ensure relation exists at start of recovery
VACUUM truncme; -- generate prune records
DROP TABLE truncme;
`)
	crash("prune-preexisting-dropped", nil)

	// Test: Replay of PRUNE records for a newly created, then dropped,
	// relation.
	node.SafePSQL("postgres", `
CREATE TABLE truncme(i int) WITH (fillfactor = 50);
INSERT INTO truncme SELECT generate_series(1, 1000);
UPDATE truncme SET i = 1;
VACUUM truncme; -- generate prune records
DROP TABLE truncme;
`)
	crash("prune-new-dropped", nil)

	// Test: Replay of PRUNE records affecting truncated block. With FPIs
	// used for PRUNE.
	node.SafePSQL("postgres", `
CREATE TABLE truncme(i int) WITH (fillfactor = 50);
INSERT INTO truncme SELECT generate_series(1, 1000);
UPDATE truncme SET i = 1;
CHECKPOINT; -- generate FPIs
VACUUM truncme; -- generate prune records
TRUNCATE truncme; -- make blocks non-existing
INSERT INTO truncme SELECT generate_series(1, 10);
`)
	crash("prune-truncated-fpi", contents("select count(*), sum(i) FROM truncme", "10|55",
		"table contents as expected after recovery"))
	node.SafePSQL("postgres", "DROP TABLE truncme")

	// Test replay of PRUNE records for blocks that are later truncated.
	// Without FPIs used for PRUNE.
	node.SafePSQL("postgres", `
CREATE TABLE truncme(i int) WITH (fillfactor = 50);
INSERT INTO truncme SELECT generate_series(1, 1000);
UPDATE truncme SET i = 1;
VACUUM truncme; -- generate prune records
TRUNCATE truncme; -- make blocks non-existing
INSERT INTO truncme SELECT generate_series(1, 10);
`)
	crash("prune-truncated-nofpi", contents("select count(*), sum(i) FROM truncme", "10|55",
		"table contents as expected after recovery"))
	node.SafePSQL("postgres", "DROP TABLE truncme")

	// Test: Replay of partial truncation via VACUUM.
	node.SafePSQL("postgres", `
CREATE TABLE truncme(i int) WITH (fillfactor = 50);
INSERT INTO truncme SELECT generate_series(1, 1000);
UPDATE truncme SET i = i + 1;
-- ensure a mix of pre/post truncation rows
DELETE FROM truncme WHERE i > 500;

VACUUM truncme; -- should truncate relation

-- rows at TIDs that previously existed
INSERT INTO truncme SELECT generate_series(1000, 1010);
`)
	crash("vacuum-partial-truncation", contents("select count(*), sum(i), min(i), max(i) FROM truncme",
		"510|136304|2|1010", "table contents as expected after recovery"))
	node.SafePSQL("postgres", "DROP TABLE truncme")
}
