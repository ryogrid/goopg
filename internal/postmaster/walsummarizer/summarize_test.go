package walsummarizer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/backup/walsummary"
	"github.com/goopg/goopg/internal/storage"
)

func le32(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

// rec builds a decoded record at PG ReadRecPtr pgStart (goopg StartLSN is
// one more) ending at pgEnd.
func rec(pgStart, pgEnd uint64, rmid, info uint8, main []byte, blocks ...xlog.XLogBlockRef) xlog.Record {
	return xlog.Record{StartLSN: pgStart + 1, EndLSN: pgEnd, XLog: &xlog.XLogDecodedRecord{
		Header: xlog.XLogRecord{Rmid: xlog.Rmgr(rmid), Info: info}, MainData: main, Blocks: blocks}}
}

func redo(at uint64, walLevel uint32) xlog.Record {
	return rec(at, at+0x28, rmXlogID, xlogCheckpointRedo, le32(walLevel))
}

func blk(spc, db, rel uint32, fork storage.ForkNumber, b uint32) xlog.XLogBlockRef {
	return xlog.XLogBlockRef{Rel: storage.RelFileNode{TblOid: spc, DBOid: db, RelOid: rel, Fork: fork}, Block: storage.BlockNumber(b)}
}

type entryWant struct {
	loc    blkreftable.RelFileLocator
	fork   int32
	limit  uint32
	blocks []uint32
}

// readSummary decodes a written summary file into its entries.
func readSummary(t *testing.T, dataDir string, f walsummary.File) []entryWant {
	t.Helper()
	rd, fh, err := walsummary.Read(dataDir, f)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var out []entryWant
	for {
		key, limit, ok, err := rd.NextRelation()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		var blocks []blkreftable.BlockNumber
		for {
			n := len(blocks)
			if blocks, err = rd.Blocks(blocks, 64); err != nil {
				t.Fatal(err)
			}
			if len(blocks) == n {
				break
			}
		}
		e := entryWant{loc: key.Locator, fork: key.Fork, limit: limit}
		for _, b := range blocks {
			e.blocks = append(e.blocks, b)
		}
		out = append(out, e)
	}
}

// TestClassifierArms drives each record arm of SummarizeWAL once, between
// two redo points, and checks the summary it writes:
//   - block references, with the built-in tablespaces rebuilt from goopg's
//     folded TblOid 0 and the FSM fork ignored;
//   - XLOG_SMGR_CREATE (limit 0 except for the FSM) and XLOG_SMGR_TRUNCATE
//     (heap and VM limits at the heap block);
//   - XLOG_DBASE_CREATE_FILE_COPY / DROP on the {spc, db, 0} sentinel;
//   - a commit dropping a relation (limit 0 on every fork but the FSM).
//
// The second redo point ends the file at its ReadRecPtr, which is StartLSN-1.
func TestClassifierArms(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	commitWithDrop := append(make([]byte, 8), le32(1<<2, 1, 1663, 5, 777)...) // xact_time, xinfo, nrels, locator
	recs := []xlog.Record{
		redo(0x1000, 2),
		rec(0x1100, 0x1180, 10, 0x00, nil, blk(0, 5, 100, 0, 3), blk(0, 0, 1262, 0, 0), blk(0, 5, 100, 1, 9), blk(16400, 5, 200, 2, 1)),
		rec(0x1200, 0x1240, rmSmgrID, xlogSmgrCreate, append(le32(1663, 5, 300), le32(0)...)),
		rec(0x1300, 0x1340, rmSmgrID, xlogSmgrCreate, append(le32(1663, 5, 300), le32(1)...)),
		rec(0x1400, 0x1440, rmSmgrID, xlogSmgrTruncate, le32(7, 1663, 5, 100, smgrTruncateHeap|smgrTruncateVM)),
		rec(0x1500, 0x1540, rmDbaseID, xlogDbaseCreateFileCopy, le32(16500, 1663, 1, 1663)),
		rec(0x1600, 0x1640, rmDbaseID, xlogDbaseDrop, le32(16501, 2, 1663, 16400)),
		rec(0x1700, 0x1740, rmXactID, xlogXactCommit|0x80, commitWithDrop),
		redo(0x1800, 2),
	}
	files, resume, err := Summarize(data, 1, recs, 0x1000)
	if err != nil {
		t.Fatal(err)
	}
	want := walsummary.File{TLI: 1, Start: 0x1000, End: 0x1800}
	if len(files) != 1 || files[0] != want || resume != 0x1800 {
		t.Fatalf("files %v resume %X, want [%v] 1800", files, resume, want)
	}
	got := fmt.Sprint(readSummary(t, data, files[0]))
	const inv = blkreftable.InvalidBlockNumber
	expect := fmt.Sprint([]entryWant{
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 100}, 0, 7, []uint32{3}},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 100}, 2, 7, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 300}, 0, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 777}, 0, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 777}, 2, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 777}, 3, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 16500}, 0, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 16501}, 0, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1664, DbOid: 0, RelNumber: 1262}, 0, inv, []uint32{0}},
		{blkreftable.RelFileLocator{SpcOid: 16400, DbOid: 5, RelNumber: 200}, 2, inv, []uint32{1}},
		{blkreftable.RelFileLocator{SpcOid: 16400, DbOid: 16501}, 0, 0, nil},
	})
	if got != expect {
		t.Errorf("summary entries\n got %s\nwant %s", got, expect)
	}
}

// TestNativeRecordArms: goopg-native records that modify a block without a
// block reference (design §4.4 items 1-3) are written through the real WAL
// writer, so they carry the (rmid, info) header goopg stamps on disk, and
// read back; the summary marks what PG's block-ref'd equivalent would.
func TestNativeRecordArms(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(data, "pg_wal")
	w, err := xlog.NewWriter(xlog.Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	heap := storage.RelFileNode{DBOid: 5, RelOid: 100}
	shared := storage.RelFileNode{DBOid: 0, RelOid: 1262}
	for _, payload := range [][]byte{
		xlog.EncodeHeapLock(heap, 4, 2, 900, 1),
		xlog.EncodeHeapVacuum(heap, 6, []uint16{1, 2}),
		// A set marks the VM page and the heap page; a clear marks nothing.
		xlog.EncodeHeapVisible(xlog.HeapVisiblePayload{Rel: heap, HeapBlk: 32672 + 9, Flags: xlog.HeapVisibleSetAllVisible | xlog.HeapVisibleSetAllFrozen}),
		xlog.EncodeHeapVisible(xlog.HeapVisiblePayload{Rel: heap, HeapBlk: 11}),
		xlog.EncodeHeapVisible(xlog.HeapVisiblePayload{Rel: heap, HeapBlk: 12, Flags: xlog.HeapVisibleClearFrozenOnly}),
		xlog.EncodeHeapVisible(xlog.HeapVisiblePayload{Rel: shared, HeapBlk: 0, Flags: xlog.HeapVisibleSetAllVisible}),
		xlog.EncodeHeapVisible(xlog.HeapVisiblePayload{Rel: storage.RelFileNode{DBOid: 5, RelOid: 300}, Flags: xlog.HeapVisibleDropRelation}),
	} {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	native, err := xlog.ReadAll(walDir, xlog.DefaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(native) != 7 {
		t.Fatalf("read %d records, want 7", len(native))
	}
	for _, r := range native {
		if r.XLog == nil || len(r.XLog.Blocks) != 0 {
			t.Fatalf("native record at %X: XLog %v, want a header and no block refs", r.StartLSN, r.XLog)
		}
	}
	end := native[len(native)-1].EndLSN + 0x100
	recs := append(append([]xlog.Record{redo(0, 2)}, native...), redo(end, 2))
	files, _, err := Summarize(data, 1, recs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files %v, want one", files)
	}
	got := fmt.Sprint(readSummary(t, data, files[0]))
	const inv = blkreftable.InvalidBlockNumber
	expect := fmt.Sprint([]entryWant{
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 100}, 0, inv, []uint32{4, 6, 32672 + 9}},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 100}, 2, inv, []uint32{1}},
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 300}, 2, 0, nil},
		{blkreftable.RelFileLocator{SpcOid: 1664, DbOid: 0, RelNumber: 1262}, 0, inv, []uint32{0}},
		{blkreftable.RelFileLocator{SpcOid: 1664, DbOid: 0, RelNumber: 1262}, 2, inv, []uint32{0}},
	})
	if got != expect {
		t.Errorf("summary entries\n got %s\nwant %s", got, expect)
	}

	// A real PG record of the same (rmid, info) has a block reference and is
	// classified by it alone, even when its main data starts with a byte
	// equal to a native kind.
	pgLock := rec(0x100, 0x140, uint8(native[0].XLog.Header.Rmid), native[0].XLog.Header.Info,
		append([]byte{xlog.RecordKindHeapLock}, make([]byte, 7)...), blk(0, 5, 100, 0, 8))
	data2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data2, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	files, _, err = Summarize(data2, 1, []xlog.Record{redo(0, 2), pgLock, redo(0x200, 2)}, 0)
	if err != nil || len(files) != 1 {
		t.Fatalf("PG heap lock: files %v, err %v", files, err)
	}
	if got, want := fmt.Sprint(readSummary(t, data2, files[0])), fmt.Sprint([]entryWant{
		{blkreftable.RelFileLocator{SpcOid: 1663, DbOid: 5, RelNumber: 100}, 0, inv, []uint32{8}},
	}); got != want {
		t.Errorf("PG heap lock\n got %s\nwant %s", got, want)
	}
}

// TestFastForwardAndOpenRange: WAL after a redo point written at
// wal_level=minimal is skipped without a file, a summary starts again at
// the next redo point, and records after the last redo point stay
// unsummarized (the live summarizer waits for more WAL instead).
func TestFastForwardAndOpenRange(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	recs := []xlog.Record{
		redo(0x1000, walLevelMinimal),
		rec(0x1100, 0x1180, 10, 0, nil, blk(0, 5, 100, 0, 1)),
		redo(0x2000, 2),
		rec(0x2100, 0x2180, 10, 0, nil, blk(0, 5, 100, 0, 2)),
		redo(0x3000, 2),
		rec(0x3100, 0x3180, 10, 0, nil, blk(0, 5, 100, 0, 3)),
	}
	files, resume, err := Summarize(data, 1, recs, 0x1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != (walsummary.File{TLI: 1, Start: 0x2000, End: 0x3000}) || resume != 0x3000 {
		t.Fatalf("files %v resume %X, want one file 0x2000-0x3000 and resume 0x3000", files, resume)
	}
	if got := readSummary(t, data, files[0]); len(got) != 1 || fmt.Sprint(got[0].blocks) != "[2]" {
		t.Errorf("summary = %v, want block 2 only", got)
	}
}

// TestSummarizeMatchesPostgres is design §5's Oracle A. A private PG 18.3
// from postgres/local_install runs with summarize_wal=on over a workload that
// reaches every classifier arm: a user tablespace, inserts, updates, deletes,
// a VACUUM that truncates (SMGR_TRUNCATE), a dropped table (commit with
// relfilelocators), a rolled-back CREATE TABLE (abort with relfilelocators),
// and CREATE / DROP DATABASE ... STRATEGY FILE_COPY (DBASE records).
// goopg decodes that server's WAL and summarizes it from PG's first summary;
// every summary PG wrote must come out with the same name and the same
// bytes. PG's summarizer stops before the shutdown checkpoint, so goopg may
// write one more file, which must start where PG's last one ends.
func TestSummarizeMatchesPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a PostgreSQL 18.3 server")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "..", "postgres", "local_install", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "initdb")); err != nil {
		t.Skipf("PostgreSQL 18.3 not installed: %v", err)
	}
	root := t.TempDir()
	pgdata, sock, ts := filepath.Join(root, "data"), filepath.Join(root, "sock"), filepath.Join(root, "ts")
	for _, d := range []string{sock, ts} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(filepath.Join(bin, name), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	run("initdb", "-D", pgdata, "-U", "postgres", "--no-sync")
	conf := fmt.Sprintf(`
listen_addresses = ''
unix_socket_directories = '%s'
summarize_wal = on
wal_compression = off
full_page_writes = off
wal_keep_size = 1GB
max_wal_size = 4GB
checkpoint_timeout = 1h
autovacuum = off
fsync = off
`, sock)
	f, err := os.OpenFile(filepath.Join(pgdata, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(conf)
	f.Close()
	run("pg_ctl", "-D", pgdata, "-l", filepath.Join(root, "log"), "-w", "start")
	stopped := false
	defer func() {
		if !stopped {
			exec.Command(filepath.Join(bin, "pg_ctl"), "-D", pgdata, "-m", "immediate", "-w", "stop").Run()
		}
	}()
	psql := func(sql string) string {
		return strings.TrimSpace(run("psql", "-h", sock, "-U", "postgres", "-XAtq", "-v", "ON_ERROR_STOP=1", "-c", sql, "postgres"))
	}
	for _, sql := range []string{
		"CREATE TABLESPACE ts LOCATION '" + ts + "'",
		"CREATE TABLE t(a int, b text)",
		"CREATE TABLE tt(a int) TABLESPACE ts",
		"INSERT INTO t SELECT g, repeat('x',60) FROM generate_series(1,1200) g",
		"INSERT INTO tt SELECT g FROM generate_series(1,600) g",
		"CHECKPOINT",
		"UPDATE t SET a = a+1 WHERE a % 5 = 0",
		"UPDATE tt SET a = -a WHERE a % 3 = 0",
		"DELETE FROM t WHERE a > 600",
		"VACUUM t",
		"CHECKPOINT",
		"CREATE TABLE d1(a int)", "INSERT INTO d1 VALUES (1)", "DROP TABLE d1",
		"BEGIN; CREATE TABLE r1(a int); INSERT INTO r1 VALUES (1); ROLLBACK",
		"CHECKPOINT",
		"CREATE DATABASE dfc STRATEGY FILE_COPY",
		"DROP DATABASE dfc",
		"CHECKPOINT",
		"INSERT INTO tt SELECT g FROM generate_series(1,200) g",
		"CHECKPOINT",
	} {
		psql(sql)
	}
	// Wait until PG has summarized up to the last checkpoint's redo point.
	redoLSN := psql("SELECT redo_lsn - '0/0' FROM pg_control_checkpoint()")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if psql("SELECT summarized_lsn - '0/0' >= "+redoLSN+" FROM pg_get_wal_summarizer_state()") == "t" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PG's summarizer did not reach the last redo point")
		}
		time.Sleep(100 * time.Millisecond)
	}
	run("pg_ctl", "-D", pgdata, "-m", "fast", "-w", "stop")
	stopped = true

	pgFiles, err := walsummary.List(pgdata, 0, 0, 0)
	if err != nil || len(pgFiles) < 8 {
		t.Fatalf("PG wrote %d summaries (%v); the workload should give at least 8", len(pgFiles), err)
	}
	recs, err := xlog.ReadAll(filepath.Join(pgdata, "pg_wal"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	files, _, err := Summarize(out, 1, recs, pgFiles[0].Start)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < len(pgFiles) {
		t.Fatalf("goopg wrote %d summaries, PG %d", len(files), len(pgFiles))
	}
	for i, pf := range pgFiles {
		if files[i] != pf {
			t.Errorf("summary %d: goopg %s, PG %s", i, files[i].Name(), pf.Name())
			continue
		}
		a, _ := os.ReadFile(files[i].Path(out))
		b, _ := os.ReadFile(pf.Path(pgdata))
		if !bytes.Equal(a, b) {
			t.Errorf("%s: goopg's %d bytes differ from PG's %d", pf.Name(), len(a), len(b))
		}
	}
	for _, extra := range files[len(pgFiles):] {
		if extra.Start != pgFiles[len(pgFiles)-1].End {
			t.Errorf("extra summary %s does not continue PG's last one", extra.Name())
		}
	}
	t.Logf("%d summaries byte-identical to PG 18.3 (redo %s)", len(pgFiles), strconv.Quote(redoLSN))

	// The live Summarizer over the same WAL, stopped partway and restarted
	// from ResumePoint (GetOldestUnsummarizedLSN's exact start), must end
	// with the same files and no gap.
	live := t.TempDir()
	if err := os.MkdirAll(filepath.Join(live, walsummary.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	runLive := func(recs []xlog.Record, start uint64) State {
		s := NewSummarizer(live, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Run(ctx, &sliceSource{recs: recs, cancel: cancel}, start, true); err != nil {
			t.Fatal(err)
		}
		return s.State()
	}
	first := runLive(recs[:len(recs)/2], pgFiles[0].Start)
	resume, ok, err := ResumePoint(live, 1)
	if err != nil || !ok || resume != first.SummarizedLSN {
		t.Fatalf("ResumePoint = %X %v %v, want the first run's summarized LSN %X", resume, ok, err, first.SummarizedLSN)
	}
	second := runLive(recs, resume)
	liveFiles, _ := walsummary.List(live, 0, 0, 0)
	if len(liveFiles) != len(files) {
		t.Fatalf("live summarizer wrote %d files across a restart, offline %d", len(liveFiles), len(files))
	}
	if ok, missing := walsummary.AreComplete(liveFiles, pgFiles[0].Start, files[len(files)-1].End); !ok {
		t.Errorf("live summaries have a gap at %X", missing)
	}
	for i, f := range liveFiles {
		a, _ := os.ReadFile(f.Path(live))
		b, _ := os.ReadFile(files[i].Path(out))
		if f != files[i] || !bytes.Equal(a, b) {
			t.Errorf("live summary %s differs from offline %s", f.Name(), files[i].Name())
		}
	}
	if second.Running || second.SummarizedLSN != files[len(files)-1].End || second.PendingLSN < second.SummarizedLSN {
		t.Errorf("final state %+v: want stopped, summarized to %X", second, files[len(files)-1].End)
	}
}

// sliceSource replays records, then cancels its context to stop Run, as a
// server shutdown would.
type sliceSource struct {
	recs   []xlog.Record
	i      int
	cancel context.CancelFunc
}

func (s *sliceSource) Next(ctx context.Context) (xlog.Record, error) {
	if s.i >= len(s.recs) {
		s.cancel()
		<-ctx.Done()
		return xlog.Record{}, ctx.Err()
	}
	s.i++
	return s.recs[s.i-1], nil
}

// TestOldestSegmentLSN: XLogGetOldestSegno over pg_wal, per timeline,
// ignoring non-segment names.
func TestOldestSegmentLSN(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"000000010000000000000003", "000000010000000000000005", "000000020000000000000002",
		"000000010000000000000001.partial", "archive_status", "00000002.history"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if lsn, ok := oldestSegmentLSN(dir, 1, 16<<20); !ok || lsn != 3*(16<<20) {
		t.Errorf("tli 1: %X %v, want 0/3000000", lsn, ok)
	}
	if lsn, ok := oldestSegmentLSN(dir, 2, 16<<20); !ok || lsn != 2*(16<<20) {
		t.Errorf("tli 2: %X %v, want 0/2000000", lsn, ok)
	}
	if _, ok := oldestSegmentLSN(dir, 3, 16<<20); ok {
		t.Error("tli 3 has no segment")
	}
}

// TestIOCountersCountEachPageOnce: a record stream counts every 8kB WAL
// page it enters once, including pages a long record spans.
func TestIOCountersCountEachPageOnce(t *testing.T) {
	var c IOCounters
	last := int64(-1)
	c.countPages(0x28, 0x60, &last)     // page 0
	c.countPages(0x60, 0x1F00, &last)   // still page 0
	c.countPages(0x1F00, 0x2100, &last) // crosses into page 1
	c.countPages(0x2100, 0x8010, &last) // spans pages 1..4
	if r, b := c.Reads.Load(), c.ReadBytes.Load(); r != 5 || b != 5*8192 {
		t.Errorf("reads %d bytes %d, want 5 and %d", r, b, 5*8192)
	}
}
