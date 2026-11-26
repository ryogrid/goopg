package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription014Binary ports postgres/src/test/subscription/t/014_binary.pl
// to the M0149 contract: a binary = true subscription copies its tables
// with COPY ... TO STDOUT WITH (FORMAT binary) and applies changes in the
// pgoutput binary tuple format (numeric, float, bigint and array columns,
// UPDATEs that set an array element and NULLs); ALTER SUBSCRIPTION switches
// to text and back; a type without send/receive functions fails the binary
// table sync ("no binary input function available for type") until
// ALTER TYPE adds them; and mismatched column types fail binary sync
// ("incorrect binary data format") but sync once binary = false. Three
// pairings: all-goopg, a PG 18.3 publisher with a goopg subscriber, a goopg
// publisher with a PG 18.3 subscriber.
//
// Landed by M0149-0050a (COPY statement logging), 0050b (binary array
// COPY), 0050e (ALTER TYPE SET send/receive) and 0050c (subscriber binary
// mode and the READY-only apply gate).
func TestPort_Subscription014Binary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
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
			runSubscriptionBinary(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionBinary(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	// Cluster.pm init writes log_statement = all on every node.
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10", "log_statement = all"} {
		nodePublisher.AppendConf(l)
	}
	nodeSubscriber.AppendConf("log_statement = all")
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

	ddl := `
	CREATE TABLE public.test_numerical (
		a INTEGER PRIMARY KEY,
		b NUMERIC,
		c FLOAT,
		d BIGINT
		);
	CREATE TABLE public.test_arrays (
		a INTEGER[] PRIMARY KEY,
		b NUMERIC[],
		c TEXT[]
		);`
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)

	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tpub FOR ALL TABLES")

	// Ensure binary mode also executes COPY in binary format
	nodePublisher.SafePSQL("postgres", `
    INSERT INTO public.test_numerical (a, b, c, d) VALUES
		(1, 1.2, 1.3, 10),
        (2, 2.2, 2.3, 20);
	INSERT INTO public.test_arrays (a, b, c) VALUES
		('{1,2,3}', '{1.1, 1.2, 1.3}', '{"one", "two", "three"}'),
        ('{3,1,2}', '{1.3, 1.1, 1.2}', '{"three", "one", "two"}');
	`)
	publisherConnstring := nodePublisher.ConnStr()
	nodeSubscriber.SafePSQL("postgres",
		"CREATE SUBSCRIPTION tsub CONNECTION '"+publisherConnstring+"' "+
			"PUBLICATION tpub WITH (slot_name = tpub_slot, binary = true)")

	// Ensure the COPY command is executed in binary format on the publisher
	waitForLog(t, nodePublisher, regexp.MustCompile(`LOG: ( [A-Z0-9]+:)? statement: COPY (.+)? TO STDOUT WITH \(FORMAT binary\)`), 0)

	waitForSubscriberSync(t, nodeSubscriber)

	syncCheck := `
	SELECT a, b, c, d FROM test_numerical ORDER BY a;
	SELECT a, b, c FROM test_arrays ORDER BY a;
`
	is(nodeSubscriber.SafePSQL("postgres", syncCheck), "1|1.2|1.3|10\n2|2.2|2.3|20\n{1,2,3}|{1.1,1.2,1.3}|{one,two,three}\n{3,1,2}|{1.3,1.1,1.2}|{three,one,two}",
		"check synced data on subscriber")

	// Ensure apply works in binary mode
	nodePublisher.SafePSQL("postgres", `
	INSERT INTO public.test_arrays (a, b, c) VALUES
		('{2,1,3}', '{1.2, 1.1, 1.3}', '{"two", "one", "three"}'),
		('{1,3,2}', '{1.1, 1.3, 1.2}', '{"one", "three", "two"}');

	INSERT INTO public.test_numerical (a, b, c, d) VALUES
		(3, 3.2, 3.3, 30),
		(4, 4.2, 4.3, 40);
	`)
	waitForSubCatchup(t, nodePublisher, "tsub")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c, d FROM test_numerical ORDER BY a"),
		"1|1.2|1.3|10\n2|2.2|2.3|20\n3|3.2|3.3|30\n4|4.2|4.3|40", "check replicated data on subscriber")

	// Test updates as well
	nodePublisher.SafePSQL("postgres", `
	UPDATE public.test_arrays SET b[1] = 42, c = NULL;
	UPDATE public.test_numerical SET b = 42, c = NULL;
	`)
	waitForSubCatchup(t, nodePublisher, "tsub")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c FROM test_arrays ORDER BY a"),
		"{1,2,3}|{42,1.2,1.3}|\n{1,3,2}|{42,1.3,1.2}|\n{2,1,3}|{42,1.1,1.3}|\n{3,1,2}|{42,1.1,1.2}|", "check updated replicated data on subscriber")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c, d FROM test_numerical ORDER BY a"),
		"1|42||10\n2|42||20\n3|42||30\n4|42||40", "check updated replicated data on subscriber")

	// Use ALTER SUBSCRIPTION to change to text format and then back to binary
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tsub SET (binary = false);")
	nodePublisher.SafePSQL("postgres", `
	INSERT INTO public.test_numerical (a, b, c, d) VALUES
		(5, 5.2, 5.3, 50);
	`)
	waitForSubCatchup(t, nodePublisher, "tsub")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c, d FROM test_numerical ORDER BY a"),
		"1|42||10\n2|42||20\n3|42||30\n4|42||40\n5|5.2|5.3|50", "check replicated data on subscriber")

	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tsub SET (binary = true);")
	nodePublisher.SafePSQL("postgres", `
	INSERT INTO public.test_arrays (a, b, c) VALUES
		('{2,3,1}', '{1.2, 1.3, 1.1}', '{"two", "three", "one"}');
	`)
	waitForSubCatchup(t, nodePublisher, "tsub")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c FROM test_arrays ORDER BY a"),
		"{1,2,3}|{42,1.2,1.3}|\n{1,3,2}|{42,1.3,1.2}|\n{2,1,3}|{42,1.1,1.3}|\n{2,3,1}|{1.2,1.3,1.1}|{two,three,one}\n{3,1,2}|{42,1.1,1.2}|",
		"check replicated data on subscriber")

	// Test binary replication without and with send/receive functions
	ddl = `
    CREATE TYPE myvarchar;
    CREATE FUNCTION myvarcharin(cstring, oid, integer) RETURNS myvarchar
        LANGUAGE internal IMMUTABLE PARALLEL SAFE STRICT AS 'varcharin';
    CREATE FUNCTION myvarcharout(myvarchar) RETURNS cstring
        LANGUAGE internal IMMUTABLE PARALLEL SAFE STRICT AS 'varcharout';
    CREATE TYPE myvarchar (
        input = myvarcharin,
        output = myvarcharout);
    CREATE TABLE public.test_myvarchar (
        a myvarchar
    );`
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)
	nodePublisher.SafePSQL("postgres", `
    INSERT INTO public.test_myvarchar (a) VALUES
		('a');
    `)
	offset := nodeSubscriber.LogSize()
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tsub REFRESH PUBLICATION")
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`ERROR: ( [A-Z0-9]+:)? no binary input function available for type`), offset)

	ddl = `
    CREATE FUNCTION myvarcharsend(myvarchar) RETURNS bytea
        LANGUAGE internal STABLE PARALLEL SAFE STRICT AS 'varcharsend';
    CREATE FUNCTION myvarcharrecv(internal, oid, integer) RETURNS myvarchar
        LANGUAGE internal STABLE PARALLEL SAFE STRICT AS 'varcharrecv';
    ALTER TYPE myvarchar SET (
        send = myvarcharsend,
        receive = myvarcharrecv
    );`
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)
	waitForSubscriberSync(t, nodeSubscriber)
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a FROM test_myvarchar;"), "a", "check synced data on subscriber with custom type")

	// Test mismatched column types with/without binary mode
	nodePublisher.SafePSQL("postgres", `
    CREATE TABLE public.test_mismatching_types (
        a bigint PRIMARY KEY
    );
    INSERT INTO public.test_mismatching_types (a)
        VALUES (1), (2);
    `)
	offset = nodeSubscriber.LogSize()
	nodeSubscriber.SafePSQL("postgres", `
    CREATE TABLE public.test_mismatching_types (
        a int PRIMARY KEY
    );
    ALTER SUBSCRIPTION tsub REFRESH PUBLICATION;
    `)
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`ERROR: ( [A-Z0-9]+:)? incorrect binary data format`), offset)

	offset = nodePublisher.LogSize()
	nodeSubscriber.SafePSQL("postgres", `
    ALTER SUBSCRIPTION tsub SET (binary = false);`)
	waitForLog(t, nodePublisher, regexp.MustCompile(`LOG: ( [A-Z0-9]+:)? statement: COPY (.+)? TO STDOUT\n`), offset)
	waitForSubscriberSync(t, nodeSubscriber)
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a FROM test_mismatching_types ORDER BY a;"), "1\n2",
		"check synced data on subscriber with binary = false")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
