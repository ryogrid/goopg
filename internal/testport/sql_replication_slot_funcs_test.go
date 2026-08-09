package testport

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_SQLPhysicalReplicationSlotFuncs covers the SQL-callable
// replication-slot functions upstream defines in
// postgres/src/backend/replication/slotfuncs.c:
//
//	pg_create_physical_replication_slot(name [, immediately_reserve [, temporary]])
//	pg_drop_replication_slot(name)
//
// Both OIDs (3779/3780) were seeded into goopg's pg_proc long before the
// executor grew an arm for them, so `SELECT pg_create_physical_replication_slot('s')`
// resolved in the catalog and then failed with 42883 — which is how the
// M0130-S10 acceptance harness (TestE2E_PGStandbyFullCycle) died at its
// first statement. M-NIGHTLY AI-20260810-011258-003.
//
// The guard asserts the properties that matter for the harness and for
// PG parity: the slot is created, it lands in the SAME registry the
// replication-protocol commands use (a second create raises upstream's
// duplicate_object, and the wire path sees it too), immediately_reserve
// controls whether the lsn column is rendered, and drop is symmetric
// (dropping a missing slot raises undefined_object).
func TestPort_SQLPhysicalReplicationSlotFuncs(t *testing.T) {
	repo := repoRoot(t)
	c, err := cluster.New("sql-replslot-funcs", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	query := func(sqlText string) ([][]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return c.Query(ctx, sqlText)
	}

	// Default form: lsn column is empty because immediately_reserve
	// defaults to false, so upstream renders the record as `(name,)`.
	rows, err := query("SELECT pg_create_physical_replication_slot('sqlslot_a')")
	if err != nil {
		t.Fatalf("create sqlslot_a: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "(sqlslot_a,)" {
		t.Fatalf("create sqlslot_a returned %v, want [[(sqlslot_a,)]]", rows)
	}

	// Creating the same slot again must raise upstream's duplicate_object,
	// not succeed silently — this is the check that proves the function
	// mutates the durable registry rather than a throwaway.
	if _, err := query("SELECT pg_create_physical_replication_slot('sqlslot_a')"); err == nil {
		t.Fatal("second create of sqlslot_a succeeded, want duplicate_object")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second create error = %v, want 'already exists'", err)
	}

	// immediately_reserve=true renders the reserved LSN in upstream's
	// X/X notation instead of an empty column.
	rows, err = query("SELECT pg_create_physical_replication_slot('sqlslot_b', true)")
	if err != nil {
		t.Fatalf("create sqlslot_b: %v", err)
	}
	got := rows[0][0]
	if !strings.HasPrefix(got, "(sqlslot_b,") || strings.HasSuffix(got, ",)") ||
		!strings.Contains(got, "/") {
		t.Fatalf("create sqlslot_b with immediately_reserve returned %q, want (sqlslot_b,<X/X>)", got)
	}

	// Drop is symmetric, and dropping a slot that is gone raises
	// undefined_object.
	if _, err := query("SELECT pg_drop_replication_slot('sqlslot_a')"); err != nil {
		t.Fatalf("drop sqlslot_a: %v", err)
	}
	if _, err := query("SELECT pg_drop_replication_slot('sqlslot_a')"); err == nil {
		t.Fatal("second drop of sqlslot_a succeeded, want undefined_object")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("second drop error = %v, want 'does not exist'", err)
	}

	// A slot created over SQL must survive a restart — it is persisted in
	// pg_replslot/ by the same writer the wire path uses.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := query("SELECT pg_create_physical_replication_slot('sqlslot_b')"); err == nil {
		t.Fatal("sqlslot_b vanished across restart: re-create succeeded")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("post-restart re-create error = %v, want 'already exists'", err)
	}
}
