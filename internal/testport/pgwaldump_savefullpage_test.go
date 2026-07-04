package testport

// Port of postgres/src/bin/pg_waldump/t/002_save_fullpage.pl into a Go test
// (M0110-0002).
//
// 002_save_fullpage.pl validates `pg_waldump --save-fullpage`: it forces
// full-page images (FPIs) into the WAL, then asks pg_waldump to extract every
// saved block to a directory and checks that
//
//   (a) each extracted file is named TLI-LSNh-LSNl.TBLSPC.DB.RELNODE.BLK[_fork]
//       (the regexp below, taken verbatim from upstream), and
//   (b) the LSN encoded in the filename is >= the page's own pd_lsn (the first
//       8 bytes of every heap/index page), because the file is named for the
//       WAL record that carried the image while the page LSN is the state the
//       image captured.
//
// Why this is portable to goopg (unlike the rest of 002/001's server tier):
//
//   * goopg writes PG-compatible WAL (M0101-0001) and a full PostgreSQL
//     RelFileLocator in every block reference — spc=pg_default(1663),
//     db=<database oid>, relNumber=<relation oid> (goopg uses the relation OID
//     as its filenode; pg_relation_filenode() returns the same value). So the
//     --relation TBLSPC/DB/RELNODE filter resolves goopg's own segments.
//   * goopg emits an FPI for the first mutation of a page after a checkpoint
//     (the checkpointer's per-page FPI epoch, internal/wal/checkpointer.go), so
//     `CHECKPOINT; UPDATE ...;` forces at least one heap FPI exactly as the
//     upstream workload does.
//   * pg_waldump (shipped upstream, reused unchanged) already reads goopg WAL —
//     proven by TestPort_WALPgWaldumpCompat (row W-001). This test adds the
//     --save-fullpage extraction path on top.
//
// Deviations from the upstream .pl, all forced by missing goopg SQL surface and
// documented so a later loop can tighten them:
//
//   * pg_switch_wal()/pg_walfile_name() are seeded in pg_proc but not executable
//     in goopg, so instead of dumping a single switched segment we stop the
//     cluster cleanly (flushing WAL) and run --save-fullpage over every
//     pg_wal/ segment, accumulating into one output dir — same end state.
//   * block_size is the compile-time constant 8192 (goopg does not expose
//     current_setting('block_size') as an executable GUC read in this path);
//     goopg's BLCKSZ is 8192.
//   * the relation locator's DB component is NOT read from
//     `pg_database.oid`: that column is a legacy display placeholder
//     ("16384", firstNormalObjectOID) for every non-template database
//     (catalog.go's pgDatabase.VirtualRows — changing it broke pg_dump's
//     subscription round-trip). The real on-disk OID goopg's WAL/storage
//     layer uses (detectCatalogDBOID's global/1262 heap scan at startup) is
//     only observable as the `base/<dbOid>/` directory name, so this test
//     globs for it — the same workaround pgamcheck004_port_test.go already
//     uses (findHeapFile) for the identical gap.
//
// CSV row: WD-003 (`port`/`pass_required=yes`; WD-002 narrowed to just the
// still-deferred 001_basic.pl server tier). This test (TestPort_PgWaldump002SaveFullpage)
// is a self-promoting reproduction: it drives the full --save-fullpage path and
// asserts the extracted-file format + LSN ordering. Two blockers previously kept
// it skipped: (1) xl_prev written as a 1-based LSN, so pg_waldump could not walk
// the record chain — RESOLVED separately (prevRecPtr seeding fix); (2) HOT
// updates (the update path this test's workload always takes on an unindexed
// table) emitted only goopg's native opaque WAL record, never a PG-canonical
// FPI — fixed this loop via emitCanonicalHeapHotUpdate
// (internal/executor/operators_storage.go), which mirrors the existing
// PgCanonicalHeapInplace path used for the datfrozenxid in-place update. The
// 001_basic.pl server tier stays deferred separately (needs
// hash/gin/gist/spgist/brin AMs).

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// saveFullpageFileRE mirrors the $file_re in 002_save_fullpage.pl: the basename
// is TLI(8 hex)-LSNhi(8 hex)-LSNlo(8 hex).TBLSPC.DB.RELNODE.BLK with an optional
// _vm/_init/_fsm/_main fork suffix.
var saveFullpageFileRE = regexp.MustCompile(
	`^[0-9A-F]{8}-([0-9A-F]{8})-([0-9A-F]{8})[.][0-9]+[.][0-9]+[.][0-9]+[.][0-9]+(?:_vm|_init|_fsm|_main)?$`)

// TestPort_PgWaldump002SaveFullpage ports
// postgres/src/bin/pg_waldump/t/002_save_fullpage.pl (M0110-0002).
func TestPort_PgWaldump002SaveFullpage(t *testing.T) {
	waldump := findPGWaldumpBin(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}

	const blockSize = 8192 // goopg BLCKSZ; current_setting('block_size') not executable here

	c := newCluster(t, "wal_savefullpage")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	addr := c.ListenAddr()
	host, port, _ := splitHostPort(addr)
	root := repoRoot(t)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	psqlEnv := []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir}

	runSQL := func(sql string) string {
		t.Helper()
		res, err := util.RunCommand(util.CommandSpec{
			Name:    psqlBin,
			Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-tA", "-c", sql},
			Env:     psqlEnv,
			Timeout: 30e9,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("psql failed (exit=%d): %s\n%s\nSQL: %s", res.ExitCode, res.Stderr, res.Stdout, sql)
		}
		return strings.TrimSpace(res.Stdout)
	}

	// Generate data/WAL with full pages in it. The UPDATE after CHECKPOINT is
	// the first mutation of the heap pages in this FPI epoch, so it carries an
	// FPI — mirrors the upstream workload.
	runSQL("CREATE TABLE test_table AS SELECT generate_series(1,100) a")
	runSQL("CHECKPOINT")
	runSQL("UPDATE test_table SET a = a + 1")

	// Resolve the relation locator TBLSPC/DB/RELNODE. goopg has no
	// pg_database.dattablespace column, but every block reference it writes
	// uses the default tablespace OID (pg_default = 1663, the only value
	// internal/wal accepts on decode), so the tablespace component is fixed.
	const pgDefaultTablespace = 1663
	relNode := runSQL("SELECT pg_relation_filenode('test_table'::regclass)")
	if relNode == "" {
		t.Fatalf("could not resolve relnode for test_table")
	}
	// The DB component is NOT `SELECT oid FROM pg_database ...`: that column is
	// a legacy display placeholder (firstNormalObjectOID, "16384") for every
	// non-template database — see catalog.go's pgDatabase.VirtualRows comment
	// ("changing the displayed oid to match broke pg_dump's subscription
	// round-trip"). The value goopg's WAL/storage layer actually uses is the
	// real on-disk OID from detectCatalogDBOID (global/1262 heap scan at
	// startup), which is only observable on disk as the base/<dbOid>/ directory
	// name. Glob for it, mirroring findHeapFile (pgamcheck004_port_test.go) and
	// pgamcheck004's own "glob across database dirs" comment for the identical
	// gap.
	dbDirMatches, globErr := filepath.Glob(filepath.Join(c.DataDir(), "base", "*", relNode))
	if globErr != nil || len(dbDirMatches) != 1 {
		t.Fatalf("could not resolve on-disk database dir for relnode %s: matches=%v err=%v",
			relNode, dbDirMatches, globErr)
	}
	dbOID := filepath.Base(filepath.Dir(dbDirMatches[0]))
	relation := fmt.Sprintf("%d/%s/%s", pgDefaultTablespace, dbOID, relNode)
	t.Logf("test_table relation locator = %s", relation)

	// Stop cleanly so all WAL (including the FPI) is flushed to disk.
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	walDir := filepath.Join(c.DataDir(), "pg_wal")
	segs := listWALSegments(t, walDir)
	if len(segs) == 0 {
		t.Fatal("no WAL segments found in pg_wal/")
	}

	outDir := filepath.Join(t.TempDir(), "raw")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}

	// goopg now names segments in native PostgreSQL format (TLI=1 prefix,
	// e.g. 000000010000000000000001), so pg_waldump locates them directly with
	// -p <walDir> <segment-name> and infers the timeline/start LSN from the
	// filename — no alias rewriting needed. Run --save-fullpage over every
	// segment, accumulating extracted blocks into outDir. End-of-WAL on the
	// trailing segment is expected and ignored; we only require that at least
	// one FPI for our relation is saved.
	sawPrevLinkError := false
	for _, seg := range segs {
		res, _ := util.RunCommand(util.CommandSpec{
			Name: waldump,
			Args: []string{
				"--quiet",
				"--save-fullpage", outDir,
				"--relation", relation,
				"-p", walDir,
				seg,
			},
			Env:     []string{"LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		// pg_waldump exits non-zero at end-of-WAL ("invalid record length …
		// got 0"); that is expected for the final segment and is not a failure
		// of the extraction itself.
		if res.ExitCode != 0 {
			stderr := strings.TrimSpace(res.Stderr)
			if strings.Contains(stderr, "incorrect prev-link") {
				sawPrevLinkError = true
			}
			t.Logf("segment %s: pg_waldump exit=%d: %s", seg, res.ExitCode, stderr)
		}
	}

	// Verify the extracted files: filename format + LSN ordering.
	matches, err := filepath.Glob(filepath.Join(outDir, "*"))
	if err != nil {
		t.Fatalf("glob outDir: %v", err)
	}
	fileCount := 0
	for _, fullpath := range matches {
		file := filepath.Base(fullpath)
		m := saveFullpageFileRE.FindStringSubmatch(file)
		if m == nil {
			t.Errorf("extracted file %q does not match --save-fullpage name format", file)
			continue
		}
		fileCount++

		hiFn, loFn := m[1], m[2] // LSN in the filename (hex)
		hiBk, loBk := readBlockLSN(t, fullpath, blockSize)

		// The LSN stored on the page must precede (<=) the LSN the file is
		// named for. Compare as the concatenated 16-hex-digit strings, exactly
		// as the upstream `ge` string comparison does.
		fnLSN := hiFn + loFn
		bkLSN := hiBk + loBk
		if fnLSN < bkLSN {
			t.Errorf("file %s LSN %s/%s precedes page LSN %s/%s (should be >=)",
				file, hiFn, loFn, hiBk, loBk)
		}
	}
	// The xl_prev seeding fix (internal/wal/writer.go detectWritePos converts
	// scanLastSegmentEnd's 1-based start-LSN to the 0-based prevRecPtr) lets
	// pg_waldump walk goopg's full on-disk record chain. A prev-link error
	// here is therefore now a REGRESSION of that fix, not the expected blocker.
	if sawPrevLinkError {
		t.Errorf("pg_waldump hit 'incorrect prev-link' walking goopg WAL for "+
			"relation %s — the xl_prev seeding fix has regressed "+
			"(internal/wal/writer.go detectWritePos / scanLastSegmentEnd)", relation)
	}
	if fileCount == 0 {
		// Both prior blockers (xl_prev prev-link, and the HOT-update path never
		// emitting a PG-canonical FPI) are now fixed — see the file-level doc
		// comment. A count of zero here is therefore a REGRESSION of one of
		// those fixes, not an expected/skippable outcome.
		t.Errorf("pg_waldump --save-fullpage extracted no full-page images for "+
			"relation %s — check emitCanonicalHeapHotUpdate "+
			"(internal/executor/operators_storage.go) and the xl_prev seeding "+
			"fix (internal/wal/writer.go) for a regression", relation)
	}
	t.Logf("verified %d extracted full-page image(s)", fileCount)
}

// listWALSegments returns the sorted 24-hex-char segment file names in dir.
func listWALSegments(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var segs []string
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) != 24 || !isHex24(e.Name()) {
			continue
		}
		segs = append(segs, e.Name())
	}
	sort.Strings(segs)
	return segs
}

// isHex24 reports whether s is 24 hexadecimal characters (a PostgreSQL WAL
// segment file name: TLI + log id + segment number, 8 hex digits each).
func isHex24(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// readBlockLSN reads the pd_lsn from the head of a saved full-page image and
// returns it as two upper-case 8-hex-digit strings (hi, lo), mirroring the
// get_block_lsn() helper in 002_save_fullpage.pl. PageHeaderData starts with
// pd_lsn = {xlogid uint32, xrecoff uint32} in little-endian.
func readBlockLSN(t *testing.T, path string, blockSize int) (string, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read block %s: %v", path, err)
	}
	if len(data) < blockSize {
		t.Fatalf("saved block %s shorter than block size (%d < %d)", path, len(data), blockSize)
	}
	// pd_lsn = {xlogid (high bits), xrecoff (low bits)}, in that order.
	hi := binary.LittleEndian.Uint32(data[0:4])
	lo := binary.LittleEndian.Uint32(data[4:8])
	return fmt.Sprintf("%08X", hi), fmt.Sprintf("%08X", lo)
}
