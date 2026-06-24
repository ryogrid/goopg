package testport

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_TempSchema_MyTempSchemaAndDiscard hard-guards the engine half of
// the temp-schema-cleanup isolation spec (M0118-0009, design 0118-0091):
//
//   - pg_my_temp_schema() returns 0 until the session creates a temporary
//     object, then a stable non-zero OID;
//   - a temporary relation renders in that pg_temp_<id> namespace
//     (pg_class.relnamespace == pg_my_temp_schema());
//   - DISCARD TEMP drops the session's temporary relations while the namespace
//     itself persists, so a follow-up scan of that namespace finds no rows.
//
// This is the byte-identical foundation under permutation 1 of the spec. The
// full spec (TestPort_IsolationTempSchemaCleanup) additionally needs backend
// self-termination + harness connection-death rendering for permutation 2.
func TestSyntax_TempSchema_MyTempSchemaAndDiscard(t *testing.T) {
	c := newCluster(t, "syntax_temp_schema")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	queryInt := func(q string) int64 {
		t.Helper()
		var n int64
		if err := conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	exec := func(q string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// No temp namespace yet.
	if got := queryInt("SELECT pg_my_temp_schema()"); got != 0 {
		t.Fatalf("pg_my_temp_schema() before any temp object = %d, want 0", got)
	}

	exec("CREATE TEMPORARY TABLE just_to_create_temp_schema()")
	exec("DROP TABLE just_to_create_temp_schema")

	// The namespace persists even after the only temp object is dropped.
	tempNs := queryInt("SELECT pg_my_temp_schema()")
	if tempNs == 0 {
		t.Fatal("pg_my_temp_schema() after temp-object create/drop = 0, want non-zero")
	}

	exec("CREATE TEMPORARY TABLE invalidate_catalog_cache()")
	exec("CREATE TEMPORARY TABLE lives_in_temp_ns(id int)")

	// The temp relation renders in the per-session pg_temp namespace.
	relNs := queryInt("SELECT relnamespace FROM pg_class WHERE relname = 'lives_in_temp_ns'")
	if relNs != tempNs {
		t.Fatalf("temp relation relnamespace = %d, want pg_my_temp_schema() = %d", relNs, tempNs)
	}

	// The namespace appears in pg_namespace as pg_temp_<id>.
	var nspname string
	if err := conn.QueryRowContext(ctx,
		"SELECT nspname FROM pg_namespace WHERE oid = $1", tempNs).Scan(&nspname); err != nil {
		t.Fatalf("pg_namespace lookup of temp ns: %v", err)
	}
	if len(nspname) < len("pg_temp_") || nspname[:len("pg_temp_")] != "pg_temp_" {
		t.Fatalf("temp namespace nspname = %q, want pg_temp_<id>", nspname)
	}

	// DISCARD TEMP drops the session's temp relations…
	exec("DISCARD TEMP")
	if got := queryInt(
		"SELECT count(*) FROM pg_class WHERE relnamespace = " +
			"(SELECT pg_my_temp_schema())"); got != 0 {
		t.Fatalf("after DISCARD TEMP, %d relations remain in temp namespace, want 0", got)
	}
	// …but the namespace itself persists.
	if got := queryInt("SELECT pg_my_temp_schema()"); got != tempNs {
		t.Fatalf("pg_my_temp_schema() after DISCARD TEMP = %d, want %d (namespace persists)", got, tempNs)
	}
}
