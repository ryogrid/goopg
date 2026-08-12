package testport

// M0131-S27 (torn-contrecord variant) — a goopg-authored WAL stream whose tail
// ends INSIDE a multi-page record, recovered by real PostgreSQL 18.3.
//
// Design: docs/design/0131-0017-crash-interchange-e2e.md §"Torn contrecord".
// Sibling of TestE2E_PGCrashStartOnGoopgDataDir, which covers the ordinary
// crash tail (the stream ends at a record boundary or inside a record's FIRST
// page, so upstream reports a benign end-of-WAL and stops). This test covers the
// one shape that reaches a different upstream code path entirely: the last
// record's header is complete and says the record continues onto the next page,
// but that continuation never made it to disk. Upstream then takes
// xlogreader.c's `assembled` error exit (:930-945), records
// abortedRecPtr/missingContrecPtr, and — crash recovery ONLY, because
// xlogrecovery.c:3188 suppresses it whenever ArchiveRecoveryRequested — writes
// an XLOG_OVERWRITE_CONTRECORD at the missing continuation's page, stamping that
// page XLP_FIRST_IS_OVERWRITE_CONTRECORD (xlog.c:7517,
// CreateOverwriteContrecordRecord). The standby / pg_basebackup lanes
// structurally cannot reach it, so this is the only place goopg can learn
// whether its page geometry lets upstream resume there at all.
//
// WHY THE CUT IS MADE IN THE TEST, NOT BY TIMING THE KILL. The prior slice
// deferred this variant as "needs the kill timed against WAL page boundaries".
// It does not, and timing it would only make the test flaky: goopg's ring drain
// is BYTE-granular, not record-granular (`state.drainBufferBytes`,
// internal/wal/writer.go — it writes `n` bytes from the ring head to make room,
// with no record alignment), so a SIGKILL legitimately leaves the file ending at
// an arbitrary offset, page boundaries included. Racing a kill against that
// offset would produce the shape under test only occasionally. So the kill is
// real (cluster.Kill, SIGKILL of the process group) and the lost ring remainder
// is then applied deterministically: the test finds the last valid page in the
// goopg-written stream that carries XLP_FIRST_IS_CONTRECORD and zeroes from that
// page to the end of its segment — i.e. exactly the bytes the ring still held.
// Nothing is fabricated: every surviving byte was written by goopg, and zeroed
// tail pages are what goopg's own preallocation leaves behind.
//
// Honesty note (carried from the sibling): SIGKILL kills processes, not the page
// cache, so neither test produces a torn DATA page. This is torn-WAL-tail
// coverage only and must never be cited as FPI / full_page_writes coverage.
//
// Not asserted here (deferral ledger 2026-08-12, M0131-S27): upstream's
// `successfully skipped missing contrecord at %X/%X` LOG line. That line fires
// when a recovery pass REPLAYS the XLOG_OVERWRITE_CONTRECORD after having read
// the aborted record (xlogrecovery.c:2115), and crash recovery ends with an
// end-of-recovery checkpoint whose redo point is already past the overwrite
// record — so a second crash never re-reads the aborted record. Reaching that
// line needs an archive/PITR replay of this stream, which is a separate slice.
// What this test asserts instead is the on-disk consequence that line reports:
// the overwrite record itself, at the missing continuation's page, with the
// page flag upstream sets for downstream readers.

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
	"github.com/goopg/goopg/internal/wal"
)

func TestE2E_PGCrashStartOnGoopgTornContrecord(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping torn-contrecord crash-start e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}

	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	goopgDir := filepath.Join(t.TempDir(), "goopgdata")

	g, err := cluster.New("m0131-s27c-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      goopgDir,
		PSQLPath:     filepath.Join(binDir, "psql"),
		StartupWait:  60 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}
	if err := g.Init(); err != nil {
		t.Fatalf("goopg init: %v", err)
	}
	goopgDead := false
	defer func() {
		if !goopgDead {
			_ = g.Stop(cluster.ShutdownImmediate)
		}
	}()
	if err := g.Start(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg.Start: %v\n--- goopg log ---\n%s", err, tailLines(string(logTail), 40))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Step 1 — pre-checkpoint committed work: reachable from the redo point by
	// page contents alone.
	for _, stmt := range []string{
		`CREATE TABLE public.s27c_items (
			id    integer PRIMARY KEY,
			label text    NOT NULL,
			qty   integer NOT NULL
		)`,
		"CREATE INDEX s27c_items_label_idx ON public.s27c_items (label)",
		`INSERT INTO public.s27c_items (id, label, qty)
			SELECT g, 'label-' || lpad(g::text, 5, '0'), g * 10 FROM generate_series(1, 400) g`,
		// The filler table exists before the checkpoint so its own DDL is never
		// in the discarded region — only its rows are.
		"CREATE TABLE public.s27c_filler (id integer, blob text)",
	} {
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg pre-checkpoint workload %q: %v\n--- goopg log ---\n%s",
				stmt, err, tailLines(string(logTail), 40))
		}
	}
	if err := g.Checkpoint(); err != nil {
		logTail, _ := os.ReadFile(g.LogPath())
		t.Fatalf("goopg checkpoint: %v\n--- goopg log ---\n%s", err, tailLines(string(logTail), 40))
	}

	// Step 2 — post-checkpoint committed work. These rows exist ONLY in the
	// replayed tail, and they are the ones that must survive the torn
	// contrecord: the cut lands in step 3's filler, strictly later in the
	// stream, so an assertion failure here means upstream abandoned replay
	// EARLIER than the aborted record — the M0131-S30 loss shape.
	for _, stmt := range []string{
		`INSERT INTO public.s27c_items (id, label, qty)
			SELECT g, 'label-' || lpad(g::text, 5, '0'), g * 10 FROM generate_series(401, 700) g`,
		"UPDATE public.s27c_items SET qty = qty + 7 WHERE id BETWEEN 500 AND 550",
		"DELETE FROM public.s27c_items WHERE id BETWEEN 690 AND 700",
	} {
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg post-checkpoint workload %q: %v\n--- goopg log ---\n%s",
				stmt, err, tailLines(string(logTail), 40))
		}
	}

	// Step 3 — capture goopg's own answers BEFORE the filler, so the PG-side
	// comparison is against the authoring engine and covers only rows that are
	// guaranteed to precede the cut.
	want := map[string]string{}
	for _, q := range []string{
		"SELECT count(*) FROM public.s27c_items",
		"SELECT sum(qty) FROM public.s27c_items",
		"SELECT label || '/' || qty FROM public.s27c_items WHERE id = 9",
		"SELECT label || '/' || qty FROM public.s27c_items WHERE id = 520",
		"SELECT count(*) FROM public.s27c_items WHERE id BETWEEN 690 AND 700",
	} {
		want[q] = coldStartScalar(t, ctx, g, q)
	}
	// 400 + 300 − 11 = 689. A silently empty workload would make every
	// comparison below trivially true.
	if got := want["SELECT count(*) FROM public.s27c_items"]; got != "689" {
		t.Fatalf("workload sanity: goopg reports %s rows in s27c_items, want 689", got)
	}

	// Step 4 — the filler: wide rows, so records are large and page boundaries
	// fall inside them frequently. Several statements rather than one giant
	// transaction, so the stream's last megabyte is dense with contrecord pages
	// to choose a cut from.
	for i := 0; i < 6; i++ {
		stmt := fmt.Sprintf(`INSERT INTO public.s27c_filler (id, blob)
			SELECT g, repeat('%c', 1400) FROM generate_series(%d, %d) g`,
			'a'+rune(i), i*700+1, i*700+700)
		if err := runSQLSimple(t, g, stmt); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("goopg filler workload #%d: %v\n--- goopg log ---\n%s",
				i, err, tailLines(string(logTail), 40))
		}
	}

	// Step 5 — the crash. SIGKILL of the process group off the cmd handle; no
	// PID-file read, never `pkill -f goopg` (Hard-won Rule #3).
	if err := g.Kill(); err != nil {
		t.Fatalf("goopg Kill: %v", err)
	}
	goopgDead = true

	cd, err := control.ReadControlFile(goopgDir)
	if err != nil || cd == nil {
		t.Fatalf("ReadControlFile after crash: %v (nil=%v)", err, cd == nil)
	}
	if cd.State != control.DBStateInProduction {
		t.Fatalf("pg_control.State = %d (%s) after SIGKILL, want DB_IN_PRODUCTION (%d)",
			cd.State, control.DBStateName(cd.State), control.DBStateInProduction)
	}

	// Step 6 — apply the lost ring remainder: cut the stream at the last
	// contrecord page, so the file now ends inside a multi-page record.
	cut := tornContrecordCut(t, filepath.Join(goopgDir, "pg_wal"))
	t.Logf("torn-contrecord cut: %s @ page offset %d (xlp_pageaddr=%d, xlp_rem_len=%d); "+
		"%d valid page(s) of filler WAL discarded",
		filepath.Base(cut.segPath), cut.off, cut.pageAddr, cut.remLen, cut.pagesDiscarded)

	// Step 7 — stale postmaster.pid handover (sibling test documents why; ledger
	// row 2026-08-12, M0131-S27). Asserted PRESENT first so a future goopg-side
	// cleanup fails this test instead of silently passing.
	pidPath := filepath.Join(goopgDir, "postmaster.pid")
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("postmaster.pid absent after SIGKILL (%v) — goopg has grown a stale-lock-file "+
			"cleanup; this handover step is obsolete and must be removed", err)
	}
	if err := os.Remove(pidPath); err != nil {
		t.Fatalf("remove stale postmaster.pid: %v", err)
	}

	// Step 8 — real PG 18.3 on the directory with the torn contrecord.
	pg, err := pgcluster.OpenExisting("m0131-s27c-pg", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     goopgDir,
		User:        "postgres",
		StartupWait: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting: %v", err)
	}
	defer func() { _ = pg.Stop() }()
	pgLog := pgLogPathFor(goopgDir)
	if err := pg.Start(); err != nil {
		logTail, _ := os.ReadFile(pgLog)
		t.Fatalf("postgres -D <goopg dir with torn contrecord>: %v\n--- PG log ---\n%s",
			err, tailLines(string(logTail), 60))
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer readyCancel()
	if err := pg.WaitReady(readyCtx, 90*time.Second); err != nil {
		logTail, _ := os.ReadFile(pgLog)
		t.Fatalf("pg.WaitReady after recovering a torn contrecord authored by goopg: %v\n--- PG log ---\n%s",
			err, tailLines(string(logTail), 80))
	}

	// Step 9 — recovery ran, nothing PANICked, and the end-of-WAL complaint is
	// terminal. Same contract as the ordinary crash tail: reaching the aborted
	// record must not turn a benign end-of-WAL into a hard failure.
	assertPGCrashRecoveryLog(t, pgLog)

	// Step 10 — every committed row that precedes the cut, including the
	// post-checkpoint ones that exist only in the replayed tail.
	for q, exp := range want {
		if got := s4Scalar(t, pg, pgLog, q); got != exp {
			t.Fatalf("after recovering a torn contrecord the hosted PG answered %q, goopg (pre-crash) "+
				"said %q\nquery: %s\nrows missing here mean replay stopped BEFORE the aborted record, "+
				"not at it", got, exp, q)
		}
	}

	// Step 11 — the mechanism assertion: upstream wrote its
	// XLOG_OVERWRITE_CONTRECORD at the missing continuation's page and stamped
	// that page XLP_FIRST_IS_OVERWRITE_CONTRECORD for downstream readers
	// (xlog.c:7517). This is what proves upstream took the aborted-contrecord
	// path over goopg's stream — the page flag cannot appear for any other
	// reason — and that goopg's page addressing let it resume exactly there.
	hdr := readWALPageHeader(t, cut.segPath, cut.off)
	if hdr.Magic != wal.XLOGPageMagic {
		t.Fatalf("page at %s+%d has magic 0x%04x after PG recovery, want 0x%04x — upstream never "+
			"resumed writing at the missing continuation's page, so it did not treat goopg's tail as "+
			"an aborted contrecord (it stopped earlier, discarding committed WAL silently)",
			filepath.Base(cut.segPath), cut.off, hdr.Magic, wal.XLOGPageMagic)
	}
	if hdr.PageAddr != cut.pageAddr {
		t.Fatalf("page at %s+%d has xlp_pageaddr=%d after PG recovery, want %d (the value goopg "+
			"stamped there) — upstream's end-of-log accounting disagrees with goopg's page addressing",
			filepath.Base(cut.segPath), cut.off, hdr.PageAddr, cut.pageAddr)
	}
	if hdr.Info&wal.XLPFirstIsOverwriteContRecord == 0 {
		t.Fatalf("page at %s+%d is xlp_info=0x%04x after PG recovery: no "+
			"XLP_FIRST_IS_OVERWRITE_CONTRECORD. Upstream sets that bit only from "+
			"CreateOverwriteContrecordRecord, i.e. only when recovery ended inside a multi-page "+
			"record; its absence means goopg's tail did not present as an aborted contrecord",
			filepath.Base(cut.segPath), cut.off, hdr.Info)
	}
	if hdr.Info&wal.XLPFirstIsContRecord != 0 {
		t.Fatalf("page at %s+%d carries BOTH XLP_FIRST_IS_CONTRECORD and "+
			"XLP_FIRST_IS_OVERWRITE_CONTRECORD (xlp_info=0x%04x) — upstream replaces the flag, it "+
			"does not add to it", filepath.Base(cut.segPath), cut.off, hdr.Info)
	}

	// Step 12 — and the record upstream put there really is the overwrite
	// record, per the oracle's own decoder.
	assertWaldumpHasOverwriteContrecord(t, binDir, goopgDir, cut)

	// An index-qualified read over a goopg-authored btree whose last leaf
	// inserts were replayed rather than checkpointed.
	forced := "SET enable_seqscan = off; SELECT id FROM public.s27c_items WHERE label = 'label-00520'"
	out, err := pgQueryScalarAllowError(pg, forced)
	if err != nil {
		t.Fatalf("hosted PG index read after torn-contrecord recovery failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "520" {
		t.Fatalf("index-qualified read of a post-checkpoint row returned %q (full output %q), want \"520\"", got, out)
	}

	assertNoFatalInPGLog(t, pgLog)
}

// tornCut describes the page the WAL stream was cut at.
type tornCut struct {
	segPath        string // segment file the cut is in
	off            int64  // byte offset of the cut page within that segment
	pageAddr       uint64 // xlp_pageaddr goopg stamped on that page
	remLen         uint32 // xlp_rem_len — bytes of the straddling record left to read
	pagesDiscarded int    // valid pages zeroed by the cut
}

// tornContrecordCut finds the last valid page in the goopg-written stream that
// carries XLP_FIRST_IS_CONTRECORD and zeroes from that page to the end of its
// segment, plus every later segment in full. The resulting file ends inside a
// multi-page record: the straddling record's header (on the preceding page) is
// intact and says the record continues, but the continuation is gone.
//
// That is the state a SIGKILL leaves whenever the ring's undrained remainder
// happened to start at a page boundary — see the file header on why it is
// applied here instead of raced for.
//
// Page 0 of a segment is never chosen: cutting there would take the segment's
// long header (sysid / seg_size cross-check) with it, which is a DIFFERENT
// upstream failure (invalid info bits in the long header) and not the shape
// under test.
func tornContrecordCut(t *testing.T, walDir string) tornCut {
	t.Helper()

	segs := walSegmentFiles(t, walDir)
	if len(segs) == 0 {
		t.Fatalf("no WAL segment files in %s", walDir)
	}

	var (
		best      tornCut
		found     bool
		bestSegIx int
		lastValid = map[string]int64{} // segment -> offset of its last valid page
	)
	prevPageAddr := uint64(0)
scan:
	for ix, seg := range segs {
		fi, err := os.Stat(seg)
		if err != nil {
			t.Fatalf("stat %s: %v", seg, err)
		}
		segSize := fi.Size()
		f, err := os.Open(seg)
		if err != nil {
			t.Fatalf("open %s: %v", seg, err)
		}
		for off := int64(0); off+wal.SizeOfXLogLongPHD <= segSize; off += wal.XLOGBlockSize {
			buf := make([]byte, wal.SizeOfXLogLongPHD)
			if _, err := f.ReadAt(buf, off); err != nil {
				break
			}
			hdr, err := wal.DecodeXLogPageHeader(buf)
			// A zeroed or unparseable page header is the end of the written
			// stream (goopg preallocates zero-filled segments); stale contents
			// of a recycled segment are rejected by the pageaddr check below.
			if err != nil || hdr.Magic != wal.XLOGPageMagic {
				_ = f.Close()
				break scan
			}
			if hdr.PageAddr <= prevPageAddr {
				_ = f.Close()
				break scan
			}
			prevPageAddr = hdr.PageAddr
			lastValid[seg] = off
			if hdr.Info&wal.XLPFirstIsContRecord != 0 && off > 0 {
				best = tornCut{segPath: seg, off: off, pageAddr: hdr.PageAddr, remLen: hdr.RemLen}
				bestSegIx = ix
				found = true
			}
		}
		_ = f.Close()
	}
	if !found {
		t.Fatalf("no page carrying XLP_FIRST_IS_CONTRECORD in the goopg-written stream under %s — "+
			"the filler workload did not produce a record spanning a page boundary, so there is no "+
			"torn contrecord to make", walDir)
	}

	// Zero from the chosen page to the end of its segment, and every later
	// segment in full. Writing zeros (rather than truncating) keeps the
	// preallocated 16 MiB geometry upstream expects.
	fi, err := os.Stat(best.segPath)
	if err != nil {
		t.Fatalf("stat %s: %v", best.segPath, err)
	}
	if lv, ok := lastValid[best.segPath]; ok {
		best.pagesDiscarded = int((lv - best.off) / wal.XLOGBlockSize)
	}
	zeroRange(t, best.segPath, best.off, fi.Size()-best.off)
	for _, seg := range segs[bestSegIx+1:] {
		sfi, err := os.Stat(seg)
		if err != nil {
			continue
		}
		if lv, ok := lastValid[seg]; ok {
			best.pagesDiscarded += int(lv/wal.XLOGBlockSize) + 1
		}
		zeroRange(t, seg, 0, sfi.Size())
	}
	return best
}

// walSegmentFiles lists pg_wal's 24-hex-character segment files in stream order.
func walSegmentFiles(t *testing.T, walDir string) []string {
	t.Helper()
	ents, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("read %s: %v", walDir, err)
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || len(name) != 24 {
			continue
		}
		if strings.ContainsAny(name, ".") {
			continue
		}
		out = append(out, filepath.Join(walDir, name))
	}
	sort.Strings(out)
	return out
}

func zeroRange(t *testing.T, path string, off, n int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s for cut: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	zeros := make([]byte, 1<<20)
	for n > 0 {
		chunk := int64(len(zeros))
		if chunk > n {
			chunk = n
		}
		if _, err := f.WriteAt(zeros[:chunk], off); err != nil {
			t.Fatalf("zero %s @%d: %v", path, off, err)
		}
		off += chunk
		n -= chunk
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync %s: %v", path, err)
	}
}

func readWALPageHeader(t *testing.T, segPath string, off int64) wal.XLogPageHeader {
	t.Helper()
	f, err := os.Open(segPath)
	if err != nil {
		t.Fatalf("open %s: %v", segPath, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, wal.SizeOfXLogLongPHD)
	if _, err := f.ReadAt(buf, off); err != nil {
		t.Fatalf("read page header %s@%d: %v", segPath, off, err)
	}
	// A zeroed page decodes as an invalid header; return it as-is so the caller
	// can report the magic it actually found.
	hdr, err := wal.DecodeXLogPageHeader(buf)
	if err != nil {
		return wal.XLogPageHeader{Magic: binary.LittleEndian.Uint16(buf[0:2])}
	}
	return hdr
}

// assertWaldumpHasOverwriteContrecord cross-checks step 11's page flag with the
// oracle's own decoder: pg_waldump over the cut page must report an
// XLOG/OVERWRITE_CONTRECORD record there. The page flag says "upstream marked
// this page"; this says "the record upstream wrote is the one it documents".
func assertWaldumpHasOverwriteContrecord(t *testing.T, binDir, dataDir string, cut tornCut) {
	t.Helper()
	waldump := filepath.Join(binDir, "pg_waldump")
	if _, err := os.Stat(waldump); err != nil {
		t.Skipf("pg_waldump not present at %s: %v", waldump, err)
	}
	walDir := filepath.Join(dataDir, "pg_wal")
	// pg_waldump wants an LSN in %X/%08X form; xlp_pageaddr IS the page's LSN.
	// Two attempts: from the cut page (cheap, and exactly the region of
	// interest), then the whole segment — a --start that is not a record start
	// makes upstream's reader search forward, which can walk past the record we
	// are looking for. Both are expected to exit non-zero: they end on the
	// preallocated tail.
	attempts := [][]string{
		{"-p", walDir, "--start", fmt.Sprintf("%X/%08X", cut.pageAddr>>32, cut.pageAddr&0xFFFFFFFF)},
		{"-p", walDir, filepath.Base(cut.segPath)},
	}
	var dumps []string
	for _, args := range attempts {
		out, err := exec.Command(waldump, args...).CombinedOutput()
		if strings.Contains(string(out), "OVERWRITE_CONTRECORD") {
			return
		}
		dumps = append(dumps, fmt.Sprintf("$ pg_waldump %s (err=%v)\n%s",
			strings.Join(args, " "), err, tailLines(string(out), 20)))
	}
	t.Fatalf("pg_waldump reports no OVERWRITE_CONTRECORD record at or after the cut page — the page "+
		"flag says upstream marked this page as replacing a missing contrecord, so the record itself "+
		"must be here:\n%s", strings.Join(dumps, "\n"))
}
