package executor

import (
	"strings"
	"testing"
)

// TestPgGetReplicaIdentityIndex pins pg_get_replica_identity_index
// (RelationGetReplicaIndex, M0149-0041c), which a PG subscriber's table sync
// runs on the publisher: the primary key under REPLICA IDENTITY DEFAULT, the
// chosen index under USING INDEX, NULL under FULL and for a table without a
// primary key.
func TestPgGetReplicaIdentityIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE rk (a int PRIMARY KEY, b int NOT NULL)",
		"CREATE UNIQUE INDEX rk_b ON rk (b)",
		"CREATE TABLE nokey (a int)",
	} {
		runSQL(t, ctx, q)
	}
	get := func(q string) string {
		t.Helper()
		return strings.Join(renderRows(runSQL(t, ctx, q)), ";")
	}
	check := func(label, q, want string) {
		t.Helper()
		if got := get(q); got != want {
			t.Errorf("%s: got %q, want %q", label, got, want)
		}
	}
	check("default", "SELECT pg_get_replica_identity_index('rk') = 'rk_pkey'::regclass", "t")
	check("no key", "SELECT pg_get_replica_identity_index('nokey') IS NULL", "t")
	runSQL(t, ctx, "ALTER TABLE rk REPLICA IDENTITY USING INDEX rk_b")
	check("using index", "SELECT pg_get_replica_identity_index('rk') = 'rk_b'::regclass", "t")
	runSQL(t, ctx, "ALTER TABLE rk REPLICA IDENTITY FULL")
	check("full", "SELECT pg_get_replica_identity_index('rk') IS NULL", "t")
}
