package testport

// End-to-end coverage of large-object metadata (M0149-0001h1a slice 1):
// lo_create / lo_unlink write pg_largeobject_metadata (2995) as an MVCC heap,
// so an aborted lo_create or lo_unlink leaves no trace, the committed set
// survives a restart, and has_largeobject_privilege reads it. Slice 2 adds
// the pg_largeobject (2613) data pages: lo_from_bytea / lo_put / lo_get
// follow MVCC and survive a restart, and descriptors are
// transaction-scoped (AtEOXact_LargeObject). Slice 3 adds GRANT / REVOKE
// ON LARGE OBJECT and ALTER LARGE OBJECT … OWNER TO.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestLargeObjectMetadataSurvivesRestart(t *testing.T) {
	c, err := cluster.New("lo-metadata-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, sql := range []string{
		"SELECT lo_create(1001)",
		"SELECT lo_create(1002)",
		"SELECT lo_create(1003)",
		"BEGIN; SELECT lo_create(1004); ROLLBACK",
		"BEGIN; SELECT lo_unlink(1002); ROLLBACK",
		"SELECT lo_unlink(1003)",
		"SELECT lo_from_bytea(1010, decode(repeat('ab', 3000), 'hex'))",
		"SELECT lo_put(1010, 2047, 'XY')",
		"BEGIN; SELECT lo_put(1010, 0, 'QQ'); ROLLBACK",
		// Slice 3: lomacl and lomowner rewrites are transactional rows too.
		"CREATE ROLE lo_r1",
		"GRANT SELECT ON LARGE OBJECT 1001 TO PUBLIC",
		"GRANT UPDATE ON LARGE OBJECT 1001 TO lo_r1 WITH GRANT OPTION",
		"ALTER LARGE OBJECT 1002 OWNER TO lo_r1",
		"BEGIN; ALTER LARGE OBJECT 1002 OWNER TO postgres; ROLLBACK",
		"BEGIN; REVOKE ALL ON LARGE OBJECT 1001 FROM PUBLIC; ROLLBACK",
	} {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// A duplicate OID violates pg_largeobject_metadata_oid_index.
	if err := runSQLSimple(t, c, "SELECT lo_create(1001)"); err == nil {
		t.Fatalf("duplicate lo_create(1001) succeeded")
	}
	const want = "1001,1002,1010"
	check := func(phase string) {
		t.Helper()
		if got := queryScalar(t, c,
			"SELECT string_agg(oid::text, ',' ORDER BY oid) FROM pg_largeobject_metadata"); got != want {
			t.Fatalf("%s: large objects = %q, want %q", phase, got, want)
		}
		if got := queryScalar(t, c,
			"SELECT has_largeobject_privilege(1001, 'UPDATE')::text || '/' || coalesce(has_largeobject_privilege('public', 1002, 'SELECT')::text, 'null') || '/' || coalesce(has_largeobject_privilege(1003, 'SELECT')::text, 'null')"); got != "true/false/null" {
			t.Fatalf("%s: has_largeobject_privilege = %q, want true/false/null", phase, got)
		}
		if got := queryScalar(t, c,
			"SELECT string_agg(oid::text || ':' || lomowner::regrole::text || ':' || coalesce(lomacl::text, 'null'), ' ' ORDER BY oid) FROM pg_largeobject_metadata WHERE oid IN (1001, 1002)"); got != "1001:postgres:{postgres=rw/postgres,=r/postgres,lo_r1=w*/postgres} 1002:lo_r1:null" {
			t.Fatalf("%s: lomowner/lomacl = %q", phase, got)
		}
		// 3000 bytes: page 0 (2048) and page 1 (952); 'XY' spans both.
		if got := queryScalar(t, c,
			"SELECT length(lo_get(1010))::text || '/' || encode(lo_get(1010, 2046, 4), 'hex') || '/' || encode(lo_get(1010, 0, 2), 'hex')"); got != "3000/ab5859ab/abab" {
			t.Fatalf("%s: lo_get = %q, want 3000/ab5859ab/abab", phase, got)
		}
	}
	// A descriptor does not outlive its transaction, and numbering restarts
	// at 0 in the next one.
	if err := runSQLSimple(t, c, "SELECT lo_open(1010, 262144)"); err != nil {
		t.Fatal(err)
	}
	if err := runSQLSimple(t, c, "SELECT loread(0, 1)"); err == nil {
		t.Fatalf("loread on a descriptor from a finished transaction succeeded")
	}
	if got := queryScalar(t, c, "SELECT lo_open(1010, 262144)"); got != "0" {
		t.Fatalf("first descriptor of a new transaction = %q, want 0", got)
	}
	check("before restart")
	if err := c.Restart(cluster.ShutdownFast); err != nil {
		t.Fatal(err)
	}
	check("after restart")
}
