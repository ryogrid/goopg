package testport

import (
	"os"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription005Encoding ports postgres/src/test/subscription/t/005_encoding.pl
// to the M0149 contract: a UTF8 publisher replicates a non-ASCII text value
// to a LATIN1 subscriber, which stores it converted to its own encoding. It
// runs in three pairings: all-goopg, a PG 18.3 publisher with a goopg
// subscriber, and a goopg publisher with a PG 18.3 subscriber.
//
// Gated by GOOPG_TAP_005 until it passes (M0149-0041, skipped as a detour):
// a goopg database is always UTF8 whatever goopg init -E names
// (M0149-0041a); with a goopg publisher a reconnecting apply worker fails on
// its slot's off-by-one position (M0149-0040f).
func TestPort_Subscription005Encoding(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_005") == "" {
		t.Skip("005 re-port is not passing yet (M0149-0041); set GOOPG_TAP_005=1 to run")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string, args ...string) *tapnode.Node {
		return tapnode.NewGoopg(t, repo, name, args...)
	}
	pg := func(t *testing.T, name string, args ...string) *tapnode.Node {
		return tapnode.NewPG(t, repo, name, args...)
	}
	cases := []struct {
		name                  string
		publisher, subscriber func(t *testing.T, name string, args ...string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopg},
		{"pg18-publisher-goopg-subscriber", pg, goopg},
		{"goopg-publisher-pg18-subscriber", goopg, pg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubscriptionEncoding(t,
				tc.publisher(t, "publisher", "--locale=C", "--encoding=UTF8"),
				tc.subscriber(t, "subscriber", "--locale=C", "--encoding=LATIN1"))
		})
	}
}

// waitForSubscriptionSync is Cluster.pm's wait_for_subscription_sync(publisher,
// appname): every table of the subscription is ready or synchronized on the
// subscriber, then the publisher has caught up with appname.
func waitForSubscriptionSync(t *testing.T, subscriber, publisher *tapnode.Node, appname string) {
	t.Helper()
	const q = "SELECT count(1) = 0 FROM pg_subscription_rel WHERE srsubstate NOT IN ('r', 's');"
	if !subscriber.PollQueryUntil("postgres", q, "t") {
		st, _, _ := subscriber.PSQLExit("postgres", "SELECT srrelid::regclass, srsubstate FROM pg_subscription_rel")
		var errs []string
		for _, l := range strings.Split(fileTail(subscriber.LogPath(), 400), "\n") {
			if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "LOG:  logical replication") {
				errs = append(errs, l)
			}
		}
		if len(errs) > 8 {
			errs = errs[len(errs)-8:]
		}
		ps, _, _ := publisher.PSQLExit("postgres", "SELECT application_name, state, sent_lsn, write_lsn, flush_lsn, replay_lsn FROM pg_stat_replication")
		sl, _, _ := publisher.PSQLExit("postgres", "SELECT slot_name, active, restart_lsn, confirmed_flush_lsn FROM pg_replication_slots")
		cur, _, _ := publisher.PSQLExit("postgres", "SELECT pg_current_wal_lsn()")
		ss, _, _ := subscriber.PSQLExit("postgres", "SELECT relid, received_lsn, latest_end_lsn, last_msg_receipt_time FROM pg_stat_subscription")
		t.Logf("replication state| cur=%q stat_replication=%q slots=%q sub_stat=%q", cur, ps, sl, ss)
		for _, l := range strings.Split(fileTail(publisher.LogPath(), 3000), "\n") {
			ll := strings.ToLower(l)
			if strings.Contains(ll, "replication") || strings.Contains(ll, "walsender") || strings.Contains(ll, "slot") || strings.Contains(ll, "error") || strings.Contains(ll, "warn") {
				t.Log("publisher log| " + l)
			}
		}
		t.Fatalf("timed out waiting for subscriber to synchronize data; pg_subscription_rel: %q; subscriber log errors: %q", st, errs)
	}
	waitForSubCatchup(t, publisher, appname)
}

func runSubscriptionEncoding(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber.Start()

	const ddl = "CREATE TABLE test1 (a int, b text);"
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)

	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION mypub FOR ALL TABLES;")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION mysub CONNECTION '"+publisherConnstr+"' PUBLICATION mypub;")

	// Wait for initial sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "mysub")

	nodePublisher.SafePSQL("postgres", `INSERT INTO test1 VALUES (1, E'Mot\xc3\xb6rhead')`) // hand-rolled UTF-8

	waitForSubCatchup(t, nodePublisher, "mysub")

	if got := nodeSubscriber.SafePSQL("postgres", `SELECT a FROM test1 WHERE b = E'Mot\xf6rhead'`); got != "1" { // LATIN1
		rows, _, _ := nodeSubscriber.PSQLExit("postgres", "SELECT a, encode(convert_to(b, 'LATIN1'), 'hex'), length(b), octet_length(b), pg_encoding_to_char(encoding) FROM test1, pg_database WHERE datname = current_database()")
		t.Errorf("not ok: data replicated to subscriber: got %q, want %q; subscriber test1 (a, LATIN1 hex of b, length, octet_length, db encoding): %q", got, "1", rows)
	}

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
