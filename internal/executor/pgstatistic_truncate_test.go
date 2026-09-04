package executor

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// B-02 bounded-width interim: an oversized per-column pg_statistic row
// (e.g. TPC-H partsupp.ps_comment, varchar(199) x up to 101 bounds — wider
// than MaxHeapTupleSize 8160, un-toastable by goopg's catalog heap writer)
// must persist in truncated form instead of being dropped, restoring stats
// for the orders/customer/partsupp comment columns. Truncation is
// prefix-faithful, endpoints are kept, scalar fields are untouched, and rows
// that fit are written byte-identical. No format change (ledger M0125-0029).

// pgStatisticTestHeapTuple rebuilds the exact tuple persistStatsToPGStatistic
// writes (same EncodeRowPG + NullBitmapPG + header construction as
// buildCatalogPGHeapTuple; natts/infomask bits carry no length).
func pgStatisticTestHeapTuple(t *testing.T, cols []catalog.Column, row Row) storage.HeapTuple {
	t.Helper()
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	bitmap := NullBitmapPG(row)
	if len(bitmap) > 0 {
		return storage.NewHeapTupleWithNulls(1, storage.InvalidTransactionID, bitmap, body)
	}
	return storage.NewHeapTuple(1, storage.InvalidTransactionID, body)
}

func pgStatisticTestFreshPage(t *testing.T) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// ps_comment-shaped: 101 histogram bounds of 199 bytes each, the shape that
// exceeded MaxHeapTupleSize and was dropped outright.
func pgStatisticTestWideCommentStats() catalog.ColumnStats {
	bounds := make([]string, 101)
	for i := range bounds {
		bounds[i] = strings.Repeat("c", 190) + strings.Repeat("012345678", i%9+1)[:9]
	}
	return catalog.ColumnStats{
		NDistinct:   80000,
		NullFrac:    0,
		AvgWidth:    60,
		Histogram:   bounds,
		Correlation: 0.1,
	}
}

func TestPGStatisticOversizedRowPersistsTruncated(t *testing.T) {
	cols := pgStatisticColumnsPG18()
	cs := pgStatisticTestWideCommentStats()

	full := buildUserPGStatisticRow(999, 4, cs)
	fullLen, err := pgStatisticRowTupleLen(cols, full)
	if err != nil {
		t.Fatalf("tuple len: %v", err)
	}
	if fullLen <= storage.MaxHeapTupleSize {
		t.Fatalf("premise: full comment-shaped row is %d bytes, fits — test pins nothing", fullLen)
	}
	// The full row is what the old code dropped: PageAddHeapTuple rejects it
	// even on a fresh page.
	if _, err := storage.PageAddHeapTuple(pgStatisticTestFreshPage(t), pgStatisticTestHeapTuple(t, cols, full)); !errors.Is(err, storage.ErrNoSpaceInPage) {
		t.Fatalf("premise: full row PageAdd err=%v, want ErrNoSpaceInPage", err)
	}

	trow, tcs := truncateColumnStatsToFit(999, 4, cols, cs)
	truncLen, err := pgStatisticRowTupleLen(cols, trow)
	if err != nil {
		t.Fatalf("truncated tuple len: %v", err)
	}
	if truncLen > storage.MaxHeapTupleSize {
		t.Errorf("truncated row is %d bytes, still over MaxHeapTupleSize=%d", truncLen, storage.MaxHeapTupleSize)
	}
	// The truncated row persists: the same fresh-page insert accepts it.
	if _, err := storage.PageAddHeapTuple(pgStatisticTestFreshPage(t), pgStatisticTestHeapTuple(t, cols, trow)); err != nil {
		t.Errorf("truncated row PageAdd: %v (must persist where the full row was dropped)", err)
	}

	// Truncation engaged and kept a useful histogram: still >= 2 bounds,
	// every bound width-capped, endpoints kept, each bound a prefix of an
	// original bound (prefix-faithful, UTF-8 valid).
	if len(tcs.Histogram) < 2 {
		t.Errorf("truncated histogram has %d bounds, want >= 2", len(tcs.Histogram))
	}
	if len(tcs.Histogram) > len(cs.Histogram) {
		t.Errorf("truncation grew the histogram (%d -> %d)", len(cs.Histogram), len(tcs.Histogram))
	}
	// Truncation engaged with cheapest loss first: the width cap alone can
	// suffice (101 x 64 B bounds fit where 101 x 199 B did not), so require
	// shrinkage in bytes, via narrower bounds, fewer bounds, or both.
	maxFull, maxTrunc := 0, 0
	for _, b := range cs.Histogram {
		if len(b) > maxFull {
			maxFull = len(b)
		}
	}
	for _, b := range tcs.Histogram {
		if len(b) > maxTrunc {
			maxTrunc = len(b)
		}
	}
	if truncLen >= fullLen {
		t.Errorf("truncation shrank nothing (%d -> %d bytes)", fullLen, truncLen)
	}
	if maxTrunc >= maxFull && len(tcs.Histogram) >= len(cs.Histogram) {
		t.Errorf("neither bound widths (%d -> %d) nor count (%d -> %d) shrank",
			maxFull, maxTrunc, len(cs.Histogram), len(tcs.Histogram))
	}
	if got := tcs.Histogram[0]; got != truncateUTF8Prefix(cs.Histogram[0], pgStatisticMaxBoundBytes) {
		t.Errorf("first bound not kept as capped prefix: %q", got)
	}
	last := cs.Histogram[len(cs.Histogram)-1]
	if got := tcs.Histogram[len(tcs.Histogram)-1]; got != truncateUTF8Prefix(last, pgStatisticMaxBoundBytes) {
		t.Errorf("last bound not kept as capped prefix: %q", got)
	}
	for _, b := range tcs.Histogram {
		if len(b) > pgStatisticMaxBoundBytes {
			t.Errorf("bound %d bytes, over cap %d", len(b), pgStatisticMaxBoundBytes)
		}
		if !utf8.ValidString(b) {
			t.Errorf("bound %q splits a UTF-8 encoding", b)
		}
		found := false
		for _, orig := range cs.Histogram {
			if strings.HasPrefix(orig, b) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bound %q is not a prefix of any original bound", b)
		}
	}

	// Scalar fields are never touched by truncation: nullfrac, distinct,
	// width and correlation survive byte-identical (slots 3/4/5 per
	// buildUserPGStatisticRow, correlation in stanumbers3).
	if !reflect.DeepEqual(trow[3], full[3]) || !reflect.DeepEqual(trow[4], full[4]) || !reflect.DeepEqual(trow[5], full[5]) {
		t.Errorf("scalar slots changed by truncation: got %+v vs full %+v", trow[3:6], full[3:6])
	}
	decoded, err := catalog.DecodePGStatisticPhysicalRow(mustEncodeRowPG(t, cols, trow), NullBitmapPG(trow))
	if err != nil {
		t.Fatalf("decode truncated row: %v", err)
	}
	if decoded.Correlation != float32(cs.Correlation) {
		t.Errorf("correlation=%v, want %v (scalar must survive truncation)", decoded.Correlation, cs.Correlation)
	}
	if len(decoded.HistBounds) != len(tcs.Histogram) {
		t.Errorf("decoded %d bounds, wrote %d", len(decoded.HistBounds), len(tcs.Histogram))
	}
}

func mustEncodeRowPG(t *testing.T, cols []catalog.Column, row Row) []byte {
	t.Helper()
	data, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

func TestPGStatisticFullWidthUnchangedWhenFits(t *testing.T) {
	cols := pgStatisticColumnsPG18()
	cs := catalog.ColumnStats{
		NDistinct:   1234,
		NullFrac:    0.01,
		AvgWidth:    10,
		Histogram:   []string{"1998-01-01", "1998-03-15", "1998-06-30", "1998-12-31"},
		MCV:         []catalog.MCVEntry{{Value: "abc", Frequency: 0.2}, {Value: "def", Frequency: 0.1}},
		Correlation: 0.75,
	}
	full := buildUserPGStatisticRow(42, 1, cs)
	if n, err := pgStatisticRowTupleLen(cols, full); err != nil || n > storage.MaxHeapTupleSize {
		t.Fatalf("premise: narrow row must fit (len=%d, err=%v)", n, err)
	}
	trow, tcs := truncateColumnStatsToFit(42, 1, cols, cs)
	if !reflect.DeepEqual(tcs, cs) {
		t.Errorf("fitting stats were rewritten: %#v vs %#v", tcs, cs)
	}
	if !reflect.DeepEqual(trow, full) {
		t.Errorf("fitting row was rewritten (full-width path must be byte-identical)")
	}
	if a, b := mustEncodeRowPG(t, cols, trow), mustEncodeRowPG(t, cols, full); !reflect.DeepEqual(a, b) {
		t.Errorf("fitting row encoding changed (%d vs %d bytes)", len(a), len(b))
	}
}

func TestPGStatisticBoundHelpers(t *testing.T) {
	t.Run("endpoints kept and ascending", func(t *testing.T) {
		bounds := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
		thinned := thinStatisticBounds(bounds, 5)
		if len(thinned) != 5 {
			t.Fatalf("got %d bounds, want 5", len(thinned))
		}
		if thinned[0] != "a" || thinned[4] != "k" {
			t.Errorf("endpoints not kept: %q", thinned)
		}
		for i := 1; i < len(thinned); i++ {
			if thinned[i] <= thinned[i-1] {
				t.Errorf("not ascending: %q", thinned)
			}
		}
	})
	t.Run("utf8 boundary", func(t *testing.T) {
		s := strings.Repeat("é", 40) // 80 bytes, 2 bytes per rune
		got := truncateUTF8Prefix(s, pgStatisticMaxBoundBytes)
		if !utf8.ValidString(got) {
			t.Errorf("result is not valid UTF-8: %q", got)
		}
		if len(got) > pgStatisticMaxBoundBytes {
			t.Errorf("result %d bytes, over cap", len(got))
		}
		if len(got) != pgStatisticMaxBoundBytes {
			t.Errorf("result %d bytes, want exactly the %d-byte rune-aligned prefix", len(got), pgStatisticMaxBoundBytes)
		}
	})
}
