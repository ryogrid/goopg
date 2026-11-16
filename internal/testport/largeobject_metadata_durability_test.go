package testport

// End-to-end coverage of large-object metadata (M0149-0001h1a slice 1):
// lo_create / lo_unlink write pg_largeobject_metadata (2995) as an MVCC heap,
// so an aborted lo_create or lo_unlink leaves no trace, the committed set
// survives a restart, and has_largeobject_privilege reads it.

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
	} {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// A duplicate OID violates pg_largeobject_metadata_oid_index.
	if err := runSQLSimple(t, c, "SELECT lo_create(1001)"); err == nil {
		t.Fatalf("duplicate lo_create(1001) succeeded")
	}
	const want = "1001,1002"
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
	}
	check("before restart")
	if err := c.Restart(cluster.ShutdownFast); err != nil {
		t.Fatal(err)
	}
	check("after restart")
}
