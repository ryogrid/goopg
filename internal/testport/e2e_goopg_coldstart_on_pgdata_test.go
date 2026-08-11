package testport

// M0131-S3 — reverse cold start: a cluster directory that real PostgreSQL
// 18.3 created and wrote, served by goopg.
//
// Design: docs/design/0131-0003-reverse-coldstart-e2e.md. This replaces the
// *simulation* in internal/initdb/reverse_path_test.go
// (TestReversePathColdStartOpensWithoutCache), which runs goopg's own Init,
// deletes the catalog cache to force the cold-start arm, and never involves a
// PG binary or reads a user row. Here the bytes under test are PG-authored:
// upstream `initdb` builds the directory, an upstream backend writes the
// catalog and heap pages, and goopg then opens it with no conversion step.
//
// Preconditions this test depends on, both landed:
//   - M0131-S1: goopg's GUC registry accepts a PG-18-initdb postgresql.conf,
//     so step 4 can start with ZERO edits to the conf (guard 3).
//   - M0131-S2: LoadOrCreateSystemID reads pg_control first, so goopg adopts
//     the directory's identity instead of inventing a second one (guard 4).

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
	"github.com/goopg/goopg/internal/wal"
)

func TestE2E_GoopgColdStartOnPGDataDir(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping reverse cold-start e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	baseDir := t.TempDir()
	pgDir := filepath.Join(baseDir, "pgdata")

	// S3.1 — the source directory is built by the real initdb. User
	// "postgres" so the PG-authored pg_authid row matches the superuser the
	// goopg harness connects as (internal/testutil/cluster/cluster.go
	// defaults User/Database to postgres); pgcluster would otherwise default
	// to $USER.
	pg, err := pgcluster.New("m0131-s3-pg", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     pgDir,
		User:        "postgres",
		WalLevel:    "replica",
		StartupWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	pgStopped := false
	defer func() {
		if !pgStopped {
			_ = pg.Stop()
		}
	}()
	if err := pg.Start(); err != nil {
		t.Fatalf("pg.Start: %v", err)
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readyCancel()
	if err := pg.WaitReady(readyCtx, 30*time.Second); err != nil {
		t.Fatalf("pg.WaitReady: %v", err)
	}

	// S3.2 — PG-side workload. Everything stays in database `postgres`:
	// detectCatalogDBOID (internal/initdb/open.go) locates the postgres OID by
	// scanning global/1262 and the main pass reads base/<that OID> while
	// registering into DefaultDBOid, an asymmetry goopg's own directories
	// paper over with mirrorTouchedCatalogsToPostgresDB — which a PG-authored
	// directory has never run.
	//
	// Deliberately EXCLUDED (S3.6): VACUUM FULL / CLUSTER / REINDEX on a
	// system catalog. goopg writes pg_filenode.map but has no reader for it
	// (writers in internal/initdb/initdb.go; deferral-ledger row #388 names
	// this as its re-arm trigger), so it addresses catalog relfiles by OID.
	// That holds on a fresh initdb, where base/5/1247, 1249 and 1259 are
	// literally named by OID, and stops holding the moment a mapped catalog is
	// rewritten under a new relfilenode.
	pg.Exec(t, "CREATE SCHEMA s3app")
	pg.Exec(t, `CREATE TABLE public.s3_items (
		id    integer PRIMARY KEY,
		label text    NOT NULL,
		qty   integer NOT NULL
	)`)
	pg.Exec(t, "CREATE INDEX s3_items_label_idx ON public.s3_items (label)")
	pg.Exec(t, "CREATE TABLE s3app.s3_notes (id integer, note text)")
	pg.Exec(t, `INSERT INTO public.s3_items (id, label, qty)
		SELECT g, 'label-' || g, g * 10 FROM generate_series(1, 20) g`)
	pg.Exec(t, "UPDATE public.s3_items SET qty = qty + 1 WHERE id % 3 = 0")
	pg.Exec(t, "DELETE FROM public.s3_items WHERE id > 15")
	pg.Exec(t, "INSERT INTO s3app.s3_notes (id, note) VALUES (1, 'in a non-public schema')")

	// Capture PG's own answers so the goopg assertions compare against the
	// authoring engine rather than against hand-computed constants.
	wantCount := pg.QueryScalar(t, "SELECT count(*) FROM public.s3_items")
	wantSumQty := pg.QueryScalar(t, "SELECT sum(qty) FROM public.s3_items")
	wantRow7 := pg.QueryScalar(t, "SELECT label || '/' || qty FROM public.s3_items WHERE id = 7")
	wantRow9 := pg.QueryScalar(t, "SELECT label || '/' || qty FROM public.s3_items WHERE id = 9")
	wantNote := pg.QueryScalar(t, "SELECT note FROM s3app.s3_notes WHERE id = 1")
	if wantCount != "15" {
		t.Fatalf("workload sanity: PG reports %s rows in s3_items, want 15", wantCount)
	}

	// S3.3 — fast shutdown. Stop() sends SIGINT, which runs a shutdown
	// checkpoint and leaves pg_control in DB_SHUTDOWNED. This is a
	// PRECONDITION, not a nicety: the reverse path is only defined for a clean
	// source (docs/design/0130-0002-pg-class-heap-persistence.md §"WAL replay
	// constraint"). An unclean directory carries post-checkpoint records whose
	// PG-native resource managers replayDecodedXLogRecord does not handle, and
	// goopg would fail with unsupportedDecodedXLogRecord instead of the honest
	// "this input is out of scope".
	if err := pg.Stop(); err != nil {
		t.Fatalf("pg.Stop: %v", err)
	}
	pgStopped = true

	cd, err := control.ReadControlFile(pgDir)
	if err != nil {
		t.Fatalf("ReadControlFile after PG stop: %v", err)
	}
	if cd == nil {
		t.Fatalf("ReadControlFile after PG stop: no control file at %s", pgDir)
	}
	if cd.State != control.DBStateShutdowned {
		t.Fatalf("pg_control.State = %d after fast shutdown, want DB_SHUTDOWNED (%d)",
			cd.State, control.DBStateShutdowned)
	}
	if cd.SystemIdentifier == 0 {
		t.Fatalf("pg_control.SystemIdentifier is 0 on a PG-authored directory")
	}

	// S3.6 (guard 6) — probe, do not assume. normalizePGWALSegmentNames is the
	// basebackup lane's helper (e2e_failover_pg_to_goopg_test.go); it renames
	// each pg_wal segment through wal.ParseXLogFileName → wal.XLogFileName,
	// which reads as an identity for any well-formed 24-character name. If it
	// is NOT an identity here that is a finding about goopg's segment naming,
	// and it gets a ledger row rather than a silent helper call — so this test
	// asserts the no-op instead of performing the rename.
	assertPGWALSegmentNamesAlreadyNormal(t, pgDir)

	// Guard 3 — goopg starts with zero edits to postgresql.conf and no
	// standby.signal. Any edit that turns out to be needed is an S1 defect.
	confPath := filepath.Join(pgDir, "postgresql.conf")
	confBefore, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pgDir, "standby.signal")); err == nil {
		t.Fatalf("standby.signal present before goopg start — this is the cold-start lane, not the standby lane")
	}

	// Step 4 — goopg on the same directory. cluster.New only builds a handle;
	// Init() is what would run `goopg init` and append `fsync = off`, so NOT
	// calling it is the whole point: PG's postgresql.conf is handed over
	// untouched.
	g, err := cluster.New("m0131-s3-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      pgDir,
		StartupWait:  60 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}
	defer func() { _ = g.Stop(cluster.ShutdownImmediate) }()
	if err := g.Start(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg.Start on a PG-authored data directory: %v\n--- goopg log ---\n%s", err, logTail)
	}

	confAfter, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("re-read postgresql.conf: %v", err)
	}
	if string(confAfter) != string(confBefore) {
		t.Fatalf("goopg rewrote postgresql.conf; the cold-start path must not touch it")
	}

	// Guard 4 (S2) — goopg adopted pg_control's identity rather than inventing
	// one. LoadOrCreateSystemID writes the resolved value into the
	// goopg-private global/system_identifier flat file, so the flat file is the
	// observable record of what goopg decided. (There is no SQL surface for it:
	// pg_control_system() is seeded in pg_proc but has no executor handler.)
	gotSysID := readGoopgSystemIDFile(t, pgDir)
	if gotSysID != cd.SystemIdentifier {
		t.Fatalf("goopg system identifier = %d, want pg_control's %d", gotSysID, cd.SystemIdentifier)
	}

	// Step 5 — the actual measurement: PG-written rows read back through
	// goopg's physical decoders.
	//
	// Guard 5 — assertions are bounded to what those decoders recover.
	// DecodePGClassPhysicalRow (internal/catalog/codec.go) reads exactly ten
	// pg_class columns — oid, relname, relnamespace, relkind, relnatts,
	// relfilenode, reltablespace, relpersistence, relisshared, relispopulated
	// — and drops the other 23 of FormData_pg_class: reltype, reloftype,
	// relowner, relam, the four statistics fields, reltoastrelid, relhasindex,
	// relchecks, the relhas*/relrowsecurity flags, relforcerowsecurity,
	// relreplident, relispartition, relrewrite, relfrozenxid, relminmxid,
	// relacl, relpartbound (reloptions is the out-of-band exception,
	// re-decoded through executor.DecodeRowIntoMctxPGTuple). So: relation
	// identity, columns and row CONTENTS are fair game; ownership, ACLs,
	// triggers, RLS, replica identity and partitioning are not asserted here
	// because goopg cannot see them.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if got := coldStartScalar(t, ctx, g, "SELECT count(*) FROM public.s3_items"); got != wantCount {
		t.Fatalf("goopg count(*) on PG-written heap = %q, PG said %q", got, wantCount)
	}
	if got := coldStartScalar(t, ctx, g, "SELECT sum(qty) FROM public.s3_items"); got != wantSumQty {
		t.Fatalf("goopg sum(qty) = %q, PG said %q (UPDATE/DELETE visibility)", got, wantSumQty)
	}
	// id=7 was untouched by the UPDATE, so its index entry still points at the
	// only version of the row. Below the DELETE's id > 15 cut.
	if got := coldStartScalar(t, ctx, g, "SELECT label || '/' || qty FROM public.s3_items WHERE id = 7"); got != wantRow7 {
		t.Fatalf("goopg row id=7 = %q, PG said %q", got, wantRow7)
	}
	// id=9 was HOT-UPDATEd by PG (9 % 3 == 0; neither indexed column changed,
	// so PG kept the new version on the same page and added NO index entry).
	// Reading it through the heap works — the bytes and the MVCC decision are
	// right:
	if got := coldStartScalar(t, ctx, g, "SELECT label || '/' || qty FROM public.s3_items WHERE id + 0 = 9"); got != wantRow9 {
		t.Fatalf("goopg row id=9 (UPDATEd, seq scan) = %q, PG said %q", got, wantRow9)
	}

	// …and since M0131-S11 so does reading it through EITHER index. This block
	// was the S3 finding, inverted now that the gap is closed: PG stores
	// HEAP_HOT_UPDATED (0x4000) and HEAP_ONLY_TUPLE (0x8000) in **t_infomask2**
	// (postgres/src/include/access/htup_details.h:550, :568), goopg kept the
	// same bit VALUES in t_infomask, and followHOTChain therefore read
	// HeapHotUpdated == 0 on a PG-authored chain root, called it a chain end
	// and returned nothing — while the seq scan above, which never consults
	// the flag, was correct. S11 moved the bits; these two queries walk the
	// PG-authored HOT chain through both the primary-key and the label index.
	for _, q := range []string{
		"SELECT label || '/' || qty FROM public.s3_items WHERE id = 9",
		"SELECT label || '/' || qty FROM public.s3_items WHERE label = 'label-9'",
	} {
		if got := coldStartScalar(t, ctx, g, q); got != wantRow9 {
			t.Fatalf("index scan %q on the PG-authored HOT chain = %q, PG said %q — "+
				"M0131-S11 moved HEAP_HOT_UPDATED/HEAP_ONLY_TUPLE into t_infomask2; "+
				"a miss here means followHOTChain regressed to reading t_infomask "+
				"(see docs/design/0131-0011-hot-flags-infomask2.md)", q, got, wantRow9)
		}
	}
	// The un-updated row still resolves through the same index, so any miss
	// above would be specific to HOT chains and not a wholesale PG-btree read
	// failure.
	if got := coldStartScalar(t, ctx, g, "SELECT count(*) FROM public.s3_items WHERE id = 7"); got != "1" {
		t.Fatalf("index scan for the un-UPDATEd id=7 returned %q, want 1 — "+
			"goopg cannot read the PG-authored btree at all, which is a wider failure than the HOT-chain gap", got)
	}
	if got := coldStartScalar(t, ctx, g, "SELECT count(*) FROM public.s3_items WHERE id > 15"); got != "0" {
		t.Fatalf("goopg still sees %q DELETEd rows (id > 15)", got)
	}
	// reloadUserSchemasFromHeap runs before the table load precisely so
	// relnamespace reverse-maps to a schema name; a non-public schema is the
	// only thing that actually exercises it on PG-authored pg_namespace rows.
	if got := coldStartScalar(t, ctx, g, "SELECT note FROM s3app.s3_notes WHERE id = 1"); got != wantNote {
		t.Fatalf("goopg read of the non-public schema = %q, PG said %q", got, wantNote)
	}
}

// assertPGWALSegmentNamesAlreadyNormal is the non-mutating twin of
// normalizePGWALSegmentNames: it fails instead of renaming. See guard 6 in
// docs/design/0131-0003-reverse-coldstart-e2e.md — if a PG-authored pg_wal
// needs the rename, that is a finding to record, not a step to perform.
func assertPGWALSegmentNamesAlreadyNormal(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "pg_wal"))
	if err != nil {
		t.Fatalf("read pg_wal: %v", err)
	}
	parsed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		tli, segno, ok := wal.ParseXLogFileName(entry.Name(), 16<<20)
		if !ok {
			continue
		}
		parsed++
		if got := wal.XLogFileName(tli, segno, 16<<20); got != entry.Name() {
			t.Fatalf("PG-authored WAL segment %q round-trips to %q — goopg's segment naming disagrees with PG's; ledger this before working around it",
				entry.Name(), got)
		}
	}
	if parsed == 0 {
		t.Fatalf("no parsable WAL segment found in %s/pg_wal", dataDir)
	}
}

// readGoopgSystemIDFile reads the 8-byte little-endian
// global/system_identifier flat file goopg writes from the resolved cluster
// identity (internal/initdb/initdb.go, LoadOrCreateSystemID).
func readGoopgSystemIDFile(t *testing.T, dataDir string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "global", "system_identifier"))
	if err != nil {
		t.Fatalf("read global/system_identifier: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("global/system_identifier: length %d, want 8", len(data))
	}
	return binary.LittleEndian.Uint64(data)
}

// coldStartScalar runs a one-cell SELECT through the goopg cluster's lib/pq
// client and returns the text value.
func coldStartScalar(t *testing.T, ctx context.Context, c *cluster.Cluster, sqlText string) string {
	t.Helper()
	rows, err := c.Query(ctx, sqlText)
	if err != nil {
		logTail, _ := os.ReadFile(c.LogPath())
		t.Fatalf("goopg query %q: %v\n--- goopg log ---\n%s", sqlText, err, tailLines(string(logTail), 40))
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("goopg query %q returned %d rows, want exactly one one-column row", sqlText, len(rows))
	}
	return rows[0][0]
}

// tailLines returns the last n lines of s, for bounded failure output.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("… (%d earlier lines elided)\n%s",
		len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}
