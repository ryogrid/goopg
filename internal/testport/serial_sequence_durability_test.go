package testport

// End-to-end coverage of sequence / SERIAL restart persistence.
//
// goopg's sequence registry is in-memory only (executor seqRegistry), and a
// SERIAL column's pg_attribute heap row stores the PG-canonical base integer
// atttypid, so before this change BOTH the implicit sequence and the column's
// serial-ness vanished on restart: post-restart INSERTs omitting the serial
// column left it NULL (a NOT NULL violation when constrained). Surfaced by
// WordPress-on-goopg — wp_usermeta.umeta_id INSERTs failed after the first
// server restart, and 29 wp_options rows were silently written with NULL
// option_id.
//
// The fix mirrors the CREATE SCHEMA / CREATE FUNCTION WAL-record mechanism:
// full-state RecordKindSequenceState snapshots at DDL/setval time plus
// every-32-nextval pre-logging (upstream SEQ_LOG_VALS,
// postgres/src/backend/commands/sequence.c), replayed by
// replaySequenceDDLRecords after loadUserTablesFromHeap. Values after a
// restart may jump by up to 32 (PG-identical gap semantics), so the
// assertions check monotonic continuation, not exact ids.

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_SerialSequenceSurvivesRestart creates a table with a BIGSERIAL
// primary key, inserts rows, restarts the cluster, and asserts (a) inserts
// keep auto-generating ids strictly above the pre-restart maximum, and (b) an
// explicit CREATE SEQUENCE also survives, while a dropped sequence stays gone.
func TestPort_SerialSequenceSurvivesRestart(t *testing.T) {
	c, err := cluster.New("serial-seq-durability", cluster.Options{
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

	if err := runSQLSimple(t, c,
		"CREATE TABLE sdur (id bigserial NOT NULL, k text)"); err != nil {
		t.Fatalf("CREATE TABLE sdur: %v", err)
	}
	if err := runSQLSimple(t, c,
		"INSERT INTO sdur (k) VALUES ('a'), ('b'), ('c')"); err != nil {
		t.Fatalf("pre-restart INSERT: %v", err)
	}
	preMaxStr := queryScalar(t, c, "SELECT max(id) FROM sdur")
	preMax, err := strconv.ParseInt(preMaxStr, 10, 64)
	if err != nil || preMax < 3 {
		t.Fatalf("pre-restart max(id) = %q, want >= 3 (serial auto-gen broken before restart)", preMaxStr)
	}

	// An explicit sequence with non-default options must survive too.
	if err := runSQLSimple(t, c,
		"CREATE SEQUENCE sdur_explicit START WITH 100 INCREMENT BY 5"); err != nil {
		t.Fatalf("CREATE SEQUENCE sdur_explicit: %v", err)
	}
	if got := queryScalar(t, c, "SELECT nextval('sdur_explicit')"); got != "100" {
		t.Fatalf("pre-restart nextval(sdur_explicit) = %q, want 100", got)
	}

	// A dropped sequence must stay gone after replay (drop record wins).
	if err := runSQLSimple(t, c, "CREATE SEQUENCE sdur_dropped"); err != nil {
		t.Fatalf("CREATE SEQUENCE sdur_dropped: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP SEQUENCE sdur_dropped"); err != nil {
		t.Fatalf("DROP SEQUENCE sdur_dropped: %v", err)
	}

	// Clean stop -> restart. The sequence registry and the column's
	// serial-ness are rebuilt from the WAL by replaySequenceDDLRecords.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	// (a) The serial column keeps auto-generating: the INSERT omits id, and
	// the generated value continues strictly above the pre-restart max
	// (a gap of up to 32 is PG-identical pre-logging behavior).
	if err := runSQLSimple(t, c, "INSERT INTO sdur (k) VALUES ('post-restart')"); err != nil {
		t.Fatalf("post-restart INSERT (serial auto-gen did not survive): %v", err)
	}
	postStr := queryScalar(t, c,
		"SELECT id FROM sdur WHERE k = 'post-restart'")
	postID, err := strconv.ParseInt(postStr, 10, 64)
	if err != nil {
		t.Fatalf("post-restart id = %q, want an integer (serial column left NULL?)", postStr)
	}
	if postID <= preMax {
		t.Fatalf("post-restart id = %d, want > pre-restart max %d "+
			"(sequence counter was not restored — duplicate ids would follow)", postID, preMax)
	}
	if postID > preMax+33 {
		t.Fatalf("post-restart id = %d jumped more than the 32-value pre-log gap above %d", postID, preMax)
	}

	// (b) The explicit sequence resumed with its options intact: last call
	// returned 100; the next value continues the increment-5 series above it,
	// within the 32-value pre-log horizon (100 + 5..5*33).
	nvStr := queryScalar(t, c, "SELECT nextval('sdur_explicit')")
	nv, err := strconv.ParseInt(nvStr, 10, 64)
	if err != nil {
		t.Fatalf("post-restart nextval(sdur_explicit) = %q, want an integer", nvStr)
	}
	if nv <= 100 || (nv-100)%5 != 0 {
		t.Fatalf("post-restart nextval(sdur_explicit) = %d, want > 100 on the +5 series "+
			"(sequence definition not restored)", nv)
	}

	// (c) The dropped sequence stays gone.
	if err := runSQLSimple(t, c, "SELECT nextval('sdur_dropped')"); err == nil {
		t.Fatalf("nextval(sdur_dropped) succeeded post-restart, want error " +
			"(DROP SEQUENCE was not durable — a stale state record was replayed)")
	}

	// (d) Column DEFAULTs survive the restart too (B5 Slice B: pg_attrdef heap
	// rows): an INSERT omitting the defaulted columns must fill them, not NULL.
	// Regression: WordPress's `comment_count bigint NOT NULL DEFAULT 0`
	// raised a NOT NULL violation on every post-restart post creation.
	if err := runSQLSimple(t, c,
		"CREATE TABLE ddur (id bigserial NOT NULL, k text UNIQUE, cnt bigint NOT NULL DEFAULT 0, status text DEFAULT 'open')"); err != nil {
		t.Fatalf("CREATE TABLE ddur: %v", err)
	}
	// (e) The upsert path applies serial auto-gen + defaults like the plain
	// insert path (previously INSERT ... ON CONFLICT left NULL bigserial ids
	// — WordPress wrote 34 NULL wp_options.option_id rows).
	if err := runSQLSimple(t, c,
		"INSERT INTO ddur (k) VALUES ('via-upsert') ON CONFLICT (k) DO UPDATE SET cnt = ddur.cnt + 1"); err != nil {
		t.Fatalf("upsert INSERT: %v", err)
	}
	if got := queryScalar(t, c, "SELECT id FROM ddur WHERE k = 'via-upsert'"); got != "1" {
		t.Fatalf("upsert-inserted id = %q, want 1 (ON CONFLICT path skipped serial auto-gen)", got)
	}
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster (defaults): %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster (defaults): %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO ddur (k) VALUES ('post-restart')"); err != nil {
		t.Fatalf("post-restart INSERT omitting defaulted columns: %v "+
			"(column DEFAULTs did not survive the restart)", err)
	}
	if got := queryScalar(t, c, "SELECT cnt FROM ddur WHERE k = 'post-restart'"); got != "0" {
		t.Fatalf("post-restart cnt = %q, want 0 (DEFAULT 0 lost)", got)
	}
	if got := queryScalar(t, c, "SELECT status FROM ddur WHERE k = 'post-restart'"); got != "open" {
		t.Fatalf("post-restart status = %q, want open (DEFAULT 'open' lost)", got)
	}
}
