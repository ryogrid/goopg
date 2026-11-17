package testport

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_RelationRowtypeSurvivesRestart pins M0149-0001h1a34 slice 2: a
// table's rowtype (pg_class.reltype, its pg_type row and array type) and a
// view's survive a restart, resolve through regtype, are removed by DROP
// TABLE, and the OID counter never reissues a reserved type OID.
func TestPort_RelationRowtypeSurvivesRestart(t *testing.T) {
	c, err := cluster.New("relation-rowtype-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, sql := range []string{
		"CREATE TABLE rt1 (a int, b text)",
		"CREATE VIEW rv AS SELECT 1 AS x",
		"CREATE TABLE gone (a int)",
		"DROP TABLE gone",
	} {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	check := func(stage string) {
		t.Helper()
		for q, want := range map[string]string{
			"SELECT reltype::int8 - oid::int8 FROM pg_class WHERE relname = 'rt1'":                                 "2",
			"SELECT 'rt1'::regtype::oid = (SELECT reltype FROM pg_class WHERE relname = 'rt1')":                    "true",
			"SELECT 'rt1[]'::regtype::text":                                                                        "rt1[]",
			"SELECT count(*) FROM pg_type t JOIN pg_class c ON t.typrelid = c.oid WHERE c.relname IN ('rt1','rv')": "2",
			"SELECT count(*) FROM pg_type WHERE typname IN ('gone', '_gone')":                                      "0",
		} {
			if got := queryScalar(t, c, q); got != want {
				t.Errorf("%s: %s = %q, want %q", stage, q, got, want)
			}
		}
	}
	check("before restart")
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	check("after restart")
	if err := runSQLSimple(t, c, "CREATE TABLE later (a int)"); err != nil {
		t.Fatalf("CREATE TABLE later: %v", err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_type WHERE oid = (SELECT oid FROM pg_class WHERE relname = 'later')"); got != "0" {
		t.Errorf("a reserved type OID was reissued to a relation after restart")
	}
}
