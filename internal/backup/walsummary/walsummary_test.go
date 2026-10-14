package walsummary

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
)

// testdata/pg18-summaries holds the nine summary files a scratch PG 18.3
// (summarize_wal=on, a 20000-row table, an UPDATE, a DELETE and three
// CHECKPOINTs) wrote to pg_wal/summaries. pg18-summaries.available.txt is
// that server's `SELECT tli, start_lsn, end_lsn FROM
// pg_available_wal_summaries() ORDER BY start_lsn`.
const pgDir = "testdata/pg18-summaries"

func lsnText(l uint64) string { return fmt.Sprintf("%X/%X", uint32(l>>32), uint32(l)) }

// pgDataDir builds a data directory whose pg_wal/summaries holds PG's files
// plus names IsWalSummaryFilename rejects.
func pgDataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	dir := filepath.Join(data, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(pgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(pgDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, junk := range []string{
		"temp.summary",
		"00000001000000000100002800000000010caa50.summary", // lowercase hex
		"0000000100000000010000280000000010CAA50.summary",  // 39 digits
		"00000001000000000100002800000000010CAA50.summary.tmp",
		"00000001000000000100002800000000010CAA50.history",
	} {
		if err := os.WriteFile(filepath.Join(dir, junk), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

// TestListMatchesPgAvailableWalSummaries: the names PG wrote parse, print
// back identically, and List over the directory yields exactly the rows PG's
// pg_available_wal_summaries() returned, ignoring the non-summary names.
func TestListMatchesPgAvailableWalSummaries(t *testing.T) {
	data := pgDataDir(t)
	list, err := List(data, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, f := range list {
		fmt.Fprintf(&got, "%d %s %s\n", f.TLI, lsnText(f.Start), lsnText(f.End))
		if _, err := os.Stat(f.Path(data)); err != nil {
			t.Errorf("Name() %q does not round-trip to PG's file: %v", f.Name(), err)
		}
	}
	want, err := os.ReadFile("testdata/pg18-summaries.available.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Errorf("List =\n%s\nwant PG's pg_available_wal_summaries():\n%s", got.String(), want)
	}
	if l, _ := List(data, 2, 0, 0); len(l) != 0 {
		t.Errorf("List(tli=2) = %v, want none", l)
	}
}

// TestListAndFilterBoundaries pins the two overlap predicates, which differ
// on purpose in walsummary.c: GetWalSummaries drops a file that ends exactly
// at start or starts exactly at end; FilterWalSummaries keeps it.
func TestListAndFilterBoundaries(t *testing.T) {
	data := pgDataDir(t)
	all, _ := List(data, 0, 0, 0)
	first, second := all[0], all[1] // 0/1000028-0/10CAA50, 0/10CAA50-0/174F958

	list, _ := List(data, 1, first.End, second.End)
	if len(list) != 1 || list[0] != second {
		t.Errorf("List(start=%s, end=%s) = %v, want only %v", lsnText(first.End), lsnText(second.End), list, second)
	}
	filtered := Filter(all, 1, first.End, second.End)
	if len(filtered) != 3 || filtered[0] != first || filtered[1] != second || filtered[2] != all[2] {
		t.Errorf("Filter(start=%s, end=%s) = %v, want %v %v %v", lsnText(first.End), lsnText(second.End),
			filtered, first, second, all[2])
	}
	if f := Filter(all, 2, 0, 0); len(f) != 0 {
		t.Errorf("Filter(tli=2) = %v, want none", f)
	}
}

// TestAreComplete ports WalSummariesAreComplete's cases: PG's contiguous
// files cover their whole range; removing one leaves a gap reported at the
// end of the covered prefix; an empty list reports InvalidXLogRecPtr; overlap
// is tolerated.
func TestAreComplete(t *testing.T) {
	all, _ := List(pgDataDir(t), 0, 0, 0)
	start, end := all[0].Start, all[len(all)-1].End
	if ok, _ := AreComplete(all, start, end); !ok {
		t.Error("PG's contiguous summaries are not complete")
	}
	gap := append(append([]File(nil), all[:4]...), all[5:]...)
	if ok, missing := AreComplete(gap, start, end); ok || missing != all[4].Start {
		t.Errorf("with %v removed: complete=%v missing=%s, want false %s", all[4], ok, lsnText(missing), lsnText(all[4].Start))
	}
	if ok, missing := AreComplete(nil, start, end); ok || missing != 0 {
		t.Errorf("empty list: complete=%v missing=%s, want false 0/0", ok, lsnText(missing))
	}
	overlap := []File{{1, 100, 300}, {1, 50, 200}, {1, 250, 400}}
	if ok, _ := AreComplete(overlap, 60, 400); !ok {
		t.Error("overlapping summaries covering the range are not complete")
	}
	if ok, missing := AreComplete(overlap, 10, 400); ok || missing != 10 {
		t.Errorf("range starting before the first summary: complete=%v missing=%d, want false 10", ok, missing)
	}
}

// rebuild replays a serialized table into an in-memory one: each entry's
// limit first, then its blocks in stored order, which reproduces PG's array
// chunks (insertion order) and bitmaps.
func rebuild(t *testing.T, path string) *blkreftable.Table {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := blkreftable.NewReader(bytes.NewReader(b), path)
	if err != nil {
		t.Fatal(err)
	}
	tab := blkreftable.NewTable()
	for {
		key, limit, ok, err := rd.NextRelation()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return tab
		}
		if limit != blkreftable.InvalidBlockNumber {
			tab.SetLimitBlock(key.Locator, key.Fork, limit)
		}
		var blocks []blkreftable.BlockNumber
		for {
			n := len(blocks)
			if blocks, err = rd.Blocks(blocks, 4096); err != nil {
				t.Fatal(err)
			}
			if len(blocks) == n {
				break
			}
		}
		for _, blk := range blocks {
			tab.MarkBlockModified(key.Locator, key.Fork, blk)
		}
	}
}

// TestWriteReproducesPgFiles: Write stores each of PG's tables under PG's
// name, byte for byte, leaves no temp.summary behind, and PG's pg_walsummary
// prints the same thing for goopg's file as for PG's.
func TestWriteReproducesPgFiles(t *testing.T) {
	src := pgDataDir(t)
	files, _ := List(src, 0, 0, 0)
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join("..", "..", "..", "postgres", "local_install", "bin", "pg_walsummary")
	_, binErr := os.Stat(bin)
	for _, f := range files {
		if err := Write(data, f, rebuild(t, f.Path(src))); err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(f.Path(src))
		got, err := os.ReadFile(f.Path(data))
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: goopg's file (%d bytes) differs from PG's (%d bytes)", f.Name(), len(got), len(want))
		}
		if binErr == nil {
			pgOut, err1 := exec.Command(bin, f.Path(src)).CombinedOutput()
			goOut, err2 := exec.Command(bin, f.Path(data)).CombinedOutput()
			if err1 != nil || err2 != nil || !bytes.Equal(pgOut, goOut) {
				t.Errorf("%s: pg_walsummary output differs (%v / %v)", f.Name(), err1, err2)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(data, Dir, tempName)); !os.IsNotExist(err) {
		t.Errorf("temp.summary left behind: %v", err)
	}
	if again, _ := List(data, 0, 0, 0); len(again) != len(files) {
		t.Errorf("List after Write = %d files, want %d", len(again), len(files))
	}
}

// TestOpenAndRemoveIfOlderThan ports OpenWalSummaryFile's missing_ok and
// RemoveWalSummaryIfOlderThan's whole-second mtime comparison.
func TestOpenAndRemoveIfOlderThan(t *testing.T) {
	data := pgDataDir(t)
	all, _ := List(data, 0, 0, 0)
	if fh, err := Open(data, File{TLI: 9, Start: 1, End: 2}, true); fh != nil || err != nil {
		t.Errorf("Open(missing, missingOK) = %v, %v; want nil, nil", fh, err)
	}
	if _, err := Open(data, File{TLI: 9, Start: 1, End: 2}, false); err == nil {
		t.Error("Open(missing) without missingOK succeeded")
	}
	rd, fh, err := Read(data, all[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := rd.NextRelation(); err != nil || !ok {
		t.Errorf("reading PG's first summary: ok=%v err=%v", ok, err)
	}
	fh.Close()

	cutoff := time.Unix(1_700_000_000, 0)
	old, same := all[0], all[1]
	if err := os.Chtimes(old.Path(data), cutoff, cutoff.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	// Within the cutoff's second: time_t comparison keeps it.
	if err := os.Chtimes(same.Path(data), cutoff, cutoff.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []File{old, same, {TLI: 9, Start: 1, End: 2}} {
		if err := RemoveIfOlderThan(data, f, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(old.Path(data)); !os.IsNotExist(err) {
		t.Errorf("a summary older than the cutoff was kept: %v", err)
	}
	if _, err := os.Stat(same.Path(data)); err != nil {
		t.Errorf("a summary in the cutoff's second was removed: %v", err)
	}
}

// TestNameFormat pins the name for LSNs above 4GB and a non-1 timeline:
// five zero-padded uppercase hex fields, TLI then the LSN halves.
func TestNameFormat(t *testing.T) {
	f := File{TLI: 0x2A, Start: 0x1_00000028, End: 0xAB_CDEF0123}
	const want = "0000002A" + "00000001" + "00000028" + "000000AB" + "CDEF0123" + ".summary"
	if got := f.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, ok := ParseFilename(want); !ok || got != f {
		t.Errorf("ParseFilename(%q) = %v %v, want %v", want, got, ok, f)
	}
}

// TestRemoveOld ports MaybeRemoveOldWalSummaries' rule over PG's files: a
// summary is removed only when its WAL is gone (end at or before the oldest
// segment still on disk, or no WAL left for its timeline) AND its mtime
// precedes now - wal_summary_keep_time; keep 0 removes nothing.
func TestRemoveOld(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	keep := 10 * 24 * time.Hour
	setup := func() (string, []File) {
		data := pgDataDir(t)
		all, _ := List(data, 0, 0, 0)
		old, fresh := now.Add(-keep-time.Hour), now.Add(-time.Hour)
		for i, f := range all {
			m := old
			if i == 1 {
				m = fresh
			}
			if err := os.Chtimes(f.Path(data), m, m); err != nil {
				t.Fatal(err)
			}
		}
		return data, all
	}
	// The oldest segment starts at 0/174F958's end: files 0 and 1 have no WAL
	// left; file 1 is too recent; files 2.. still have WAL.
	data, all := setup()
	oldest := func(tli uint32) (uint64, bool) { return all[1].End, tli == 1 }
	removed, err := RemoveOld(data, keep, now, oldest)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != all[0] {
		t.Fatalf("removed %v, want only %v", removed, all[0])
	}
	if left, _ := List(data, 0, 0, 0); len(left) != len(all)-1 {
		t.Errorf("%d summaries left, want %d", len(left), len(all)-1)
	}

	data, all = setup()
	if removed, _ := RemoveOld(data, 0, now, oldest); len(removed) != 0 {
		t.Errorf("wal_summary_keep_time = 0 removed %v", removed)
	}
	// No WAL left on the timeline: every old-enough summary goes.
	removed, err = RemoveOld(data, keep, now, func(uint32) (uint64, bool) { return 0, false })
	if err != nil || len(removed) != len(all)-1 {
		t.Errorf("with no WAL left: removed %d (%v), want %d", len(removed), err, len(all)-1)
	}
}
