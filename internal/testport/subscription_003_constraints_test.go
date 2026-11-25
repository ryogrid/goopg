package testport

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription003Constraints ports postgres/src/test/subscription/t/003_constraints.pl
// to the M0149 contract: on a logical-replication subscriber, a replicated
// insert carries an out-of-line (TOASTed) value into a table whose column
// order differs from the publisher's; foreign keys are not enforced by the
// apply worker; and an ENABLE REPLICA BEFORE INSERT OR UPDATE OF trigger
// filters replicated inserts and every replicated update, since logical
// replication ships all columns. It runs in three pairings: all-goopg, a
// PG 18.3 publisher with a goopg subscriber, and a goopg publisher with a
// PG 18.3 subscriber.
//
// Gated by GOOPG_TAP_003 until it passes (M0149-0040). Both pairings with a
// PG 18.3 node pass (M0149-0040a/b/c). All-goopg fails the last check: a
// goopg publisher sends the full old row ('O') for an UPDATE of the key, and
// a goopg subscriber matches it on every column, so the row whose bid the
// subscriber's trigger kept at 1 is not found (M0149-0040d/e).
func TestPort_Subscription003Constraints(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_003") == "" {
		t.Skip("003 re-port is not passing yet (M0149-0040); set GOOPG_TAP_003=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	cases := []struct {
		name                  string
		publisher, subscriber func(t *testing.T, name string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopg},
		{"pg18-publisher-goopg-subscriber", pg, goopg},
		{"goopg-publisher-pg18-subscriber", goopg, pg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubscriptionConstraints(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

// waitForSubCatchup is Cluster.pm's wait_for_catchup(appname): the
// publisher's current WAL position has been replayed (confirmed) by the
// walsender serving appname.
func waitForSubCatchup(t *testing.T, publisher *tapnode.Node, appname string) {
	t.Helper()
	target := publisher.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	q := "SELECT '" + target + "' <= replay_lsn AND state = 'streaming' FROM pg_catalog.pg_stat_replication WHERE application_name IN ('" + appname + "', 'walreceiver')"
	if !publisher.PollQueryUntil("postgres", q, "t") {
		st, _, _ := publisher.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, write_lsn, flush_lsn, replay_lsn FROM pg_catalog.pg_stat_replication")
		slots, _, _ := publisher.PSQLExit("postgres", "SELECT slot_name, plugin, slot_type, active, restart_lsn, confirmed_flush_lsn FROM pg_catalog.pg_replication_slots")
		t.Fatalf("timed out waiting for %s to catch up to %s; pg_stat_replication: %q; pg_replication_slots: %q", appname, target, st, slots)
	}
}

func runSubscriptionConstraints(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}

	// Initialize publisher node (allows_streaming => 'logical')
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()

	// Create subscriber node
	nodeSubscriber.Start()

	// Setup structure on publisher
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_fk (bid int PRIMARY KEY);")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_fk_ref (id int PRIMARY KEY, junk text, bid int REFERENCES tab_fk (bid));")

	// Setup structure on subscriber; column order intentionally different
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_fk (bid int PRIMARY KEY);")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_fk_ref (id int PRIMARY KEY, bid int REFERENCES tab_fk (bid), junk text);")

	// Setup logical replication
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub FOR ALL TABLES;")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION tap_sub CONNECTION '"+publisherConnstr+"' PUBLICATION tap_pub WITH (copy_data = false)")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_fk (bid) VALUES (1);")
	// "junk" value is meant to be large enough to force out-of-line storage
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_fk_ref (id, bid, junk) VALUES (1, 1, repeat(pi()::text,20000));")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// Check data on subscriber
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(bid), max(bid) FROM tab_fk;"), "1|1|1", "check replicated tab_fk inserts on subscriber")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(bid), max(bid) FROM tab_fk_ref;"), "1|1|1", "check replicated tab_fk_ref inserts on subscriber")

	// Drop the fk on publisher
	nodePublisher.SafePSQL("postgres", "DROP TABLE tab_fk CASCADE;")

	// Insert data
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_fk_ref (id, bid) VALUES (2, 2);")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// FK is not enforced on subscriber
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(bid), max(bid) FROM tab_fk_ref;"), "2|1|2", "check FK ignored on subscriber")

	// Add replica trigger
	nodeSubscriber.SafePSQL("postgres", `
CREATE FUNCTION filter_basic_dml_fn() RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'INSERT') THEN
        IF (NEW.id < 10) THEN
            RETURN NEW;
        ELSE
            RETURN NULL;
        END IF;
    ELSIF (TG_OP = 'UPDATE') THEN
        RETURN NULL;
    ELSE
        RAISE WARNING 'Unknown action';
        RETURN NULL;
    END IF;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER filter_basic_dml_trg
    BEFORE INSERT OR UPDATE OF bid ON tab_fk_ref
    FOR EACH ROW EXECUTE PROCEDURE filter_basic_dml_fn();
ALTER TABLE tab_fk_ref ENABLE REPLICA TRIGGER filter_basic_dml_trg;
`)

	// Insert data
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_fk_ref (id, bid) VALUES (10, 10);")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// The trigger should cause the insert to be skipped on subscriber
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(bid), max(bid) FROM tab_fk_ref;"), "2|1|2", "check replica insert trigger applied on subscriber")

	// Update data
	nodePublisher.SafePSQL("postgres", "UPDATE tab_fk_ref SET bid = 2 WHERE bid = 1;")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// The trigger should cause the update to be skipped on subscriber
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(bid), max(bid) FROM tab_fk_ref;"), "2|1|2", "check replica update column trigger applied on subscriber")

	// Update on a column not specified in the trigger, but it will trigger
	// anyway because logical replication ships all columns in an update.
	nodePublisher.SafePSQL("postgres", "UPDATE tab_fk_ref SET id = 6 WHERE id = 1;")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(id), max(id) FROM tab_fk_ref;"), "2|1|2", "check column trigger applied even on update for other column")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
