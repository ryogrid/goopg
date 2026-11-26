package testport

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription012Collation ports postgres/src/test/subscription/t/012_collation.pl
// to the M0149 contract: a subscriber whose replica identity column uses a
// nondeterministic ICU collation finds the row to update by collation
// semantics, both through a primary key (replica identity index) and under
// REPLICA IDENTITY FULL — the publisher's U&'\00E4bc' and the subscriber's
// canonically equivalent U&'\0061\0308bc' are the same row.
//
// The .pl skips unless the build has ICU (with_icu). goopg always has it;
// a PG 18.3 node built without ICU skips the pairing where it must create
// the ICU collation (the subscriber). Three pairings: all-goopg, a PG 18.3
// publisher with a goopg subscriber, a goopg publisher with a PG 18.3
// subscriber.
//
// Landed by M0149-0049a (the apply row lookup compares by the
// nondeterministic collation) and M0149-0049b (a goopg publisher logs the
// old tuple of a REPLICA IDENTITY FULL HOT update).
func TestPort_Subscription012Collation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	// init(extra => [ '--locale=C', '--encoding=UTF8' ]) on both nodes.
	goopg := func(t *testing.T, name string) *tapnode.Node {
		return tapnode.NewGoopg(t, repo, name, "--locale=C", "--encoding=UTF8")
	}
	pg := func(t *testing.T, name string) *tapnode.Node {
		return tapnode.NewPG(t, repo, name, "--locale=C", "--encoding=UTF8")
	}
	cases := []struct {
		name                  string
		publisher, subscriber func(t *testing.T, name string) *tapnode.Node
		subscriberIsPG        bool
	}{
		{"all-goopg", goopg, goopg, false},
		{"pg18-publisher-goopg-subscriber", pg, goopg, false},
		{"goopg-publisher-pg18-subscriber", goopg, pg, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// if ($ENV{with_icu} ne 'yes') { plan skip_all => ... } — the
			// ICU collation is created on the subscriber only.
			if tc.subscriberIsPG && !pgBuildHasICU(repo) {
				t.Skip("ICU not supported by this build")
			}
			runSubscriptionCollation(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

// pgBuildHasICU is the with_icu flag of the PG 18.3 build the PG nodes run:
// whether it was configured --with-icu.
func pgBuildHasICU(repo string) bool {
	out, err := exec.Command(filepath.Join(tapnode.BinDir(repo), "pg_config"), "--configure").Output()
	return err == nil && strings.Contains(string(out), "--with-icu")
}

func runSubscriptionCollation(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber.Start()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, n := range []*tapnode.Node{nodeSubscriber, nodePublisher} {
			for _, l := range strings.Split(fileTail(n.LogPath(), 2000), "\n") {
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		}
	})

	publisherConnstr := nodePublisher.ConnStr()

	nodeSubscriber.SafePSQL("postgres",
		`CREATE COLLATION ctest_nondet (provider = icu, locale = 'und', deterministic = false)`)

	// table with replica identity index
	nodePublisher.SafePSQL("postgres", `CREATE TABLE tab1 (a text PRIMARY KEY, b text)`)
	nodePublisher.SafePSQL("postgres", `INSERT INTO tab1 VALUES (U&'\00E4bc', 'foo')`)
	nodeSubscriber.SafePSQL("postgres", `CREATE TABLE tab1 (a text COLLATE ctest_nondet PRIMARY KEY, b text)`)
	nodeSubscriber.SafePSQL("postgres", `INSERT INTO tab1 VALUES (U&'\0061\0308bc', 'foo')`)

	// table with replica identity full
	nodePublisher.SafePSQL("postgres", `CREATE TABLE tab2 (a text, b text)`)
	nodePublisher.SafePSQL("postgres", `ALTER TABLE tab2 REPLICA IDENTITY FULL`)
	nodePublisher.SafePSQL("postgres", `INSERT INTO tab2 VALUES (U&'\00E4bc', 'foo')`)
	nodeSubscriber.SafePSQL("postgres", `CREATE TABLE tab2 (a text COLLATE ctest_nondet, b text)`)
	nodeSubscriber.SafePSQL("postgres", `ALTER TABLE tab2 REPLICA IDENTITY FULL`)
	nodeSubscriber.SafePSQL("postgres", `INSERT INTO tab2 VALUES (U&'\0061\0308bc', 'foo')`)

	// set up publication, subscription
	nodePublisher.SafePSQL("postgres", `CREATE PUBLICATION pub1 FOR ALL TABLES`)
	nodeSubscriber.SafePSQL("postgres",
		`CREATE SUBSCRIPTION sub1 CONNECTION '`+publisherConnstr+`' PUBLICATION pub1 WITH (copy_data = false)`)
	waitForSubCatchup(t, nodePublisher, "sub1")

	// test with replica identity index
	nodePublisher.SafePSQL("postgres", `UPDATE tab1 SET b = 'bar' WHERE b = 'foo'`)
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", `SELECT b FROM tab1`), "bar",
		"update with primary key with nondeterministic collation")

	// test with replica identity full
	nodePublisher.SafePSQL("postgres", `UPDATE tab2 SET b = 'bar' WHERE b = 'foo'`)
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", `SELECT b FROM tab2`), "bar",
		"update with replica identity full with nondeterministic collation")
}
