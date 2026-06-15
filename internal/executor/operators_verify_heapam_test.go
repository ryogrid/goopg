package executor

// End-to-end tests for the verify_heapam(regclass, ...) SRF adapter (slice S3 of
// docs/design/0110-0008). These drive the full parse -> plan -> execute stack so
// they exercise the catalog/storage/mvcc plumbing this operator owns; the
// corruption-detection logic itself is unit-tested in internal/amcheck. The
// load-bearing assertion is the no-false-positive case: a healthy user table must
// report zero corruptions through the whole pipeline (false positives on healthy
// relations are this project's most expensive failure mode).

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// setItemIDRaw overwrites the packed ItemId at the 1-based slot, mirroring the
// pack format in storage/heap.go: offset(15) | flags<<15 | length<<17. Local to
// this test so the corruption injection does not depend on amcheck test helpers.
func setItemIDRaw(p storage.Page, slot uint16, off uint16, flags storage.ItemIDFlags, length uint16) {
	itemOff := storage.SizeOfPageHeaderData + (int(slot)-1)*4
	raw := uint32(off&0x7FFF) | (uint32(flags&0x3) << 15) | (uint32(length&0x7FFF) << 17)
	binary.LittleEndian.PutUint32(p[itemOff:itemOff+4], raw)
}

func verifyHeapamSetup(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, cleanup := newVMFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE vh (a int, b text)",
		"INSERT INTO vh VALUES (1, 'one')",
		"INSERT INTO vh VALUES (2, 'two')",
		"INSERT INTO vh VALUES (3, 'three')",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)
	return ctx, cleanup
}

// TestVerifyHeapam_CleanTableNoReports is the no-false-positive gate: a healthy
// table returns zero rows through the full SQL surface.
func TestVerifyHeapam_CleanTableNoReports(t *testing.T) {
	ctx, cleanup := verifyHeapamSetup(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT blkno, offnum, attnum, msg FROM verify_heapam('vh')",
		// OID form (regclass passed as an integer literal).
		// startblock/endblock covering the whole relation.
		"SELECT blkno, offnum, attnum, msg FROM verify_heapam('vh', false, false, 'none', 0, 0)",
	} {
		rows := runQuery(t, ctx, sql)
		if len(rows) != 0 {
			t.Errorf("%s: clean table reported %d corruptions: %+v", sql, len(rows), rows)
		}
	}
}

// TestVerifyHeapam_DetectsInjectedCorruption corrupts a line pointer on block 0
// and confirms the SRF surfaces the engine's finding as an output row.
func TestVerifyHeapam_DetectsInjectedCorruption(t *testing.T) {
	ctx, cleanup := verifyHeapamSetup(t)
	defer cleanup()

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "vh"})
	if !ok {
		t.Fatal("table vh not found")
	}
	rel := ctx.Catalog.RelFileNode(tbl)

	// Nudge slot 1's line-pointer offset by 1 so it is no longer maximally
	// aligned — the simplest deterministic page-structural corruption.
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("Pin block 0: %v", err)
	}
	item, err := storage.PageGetItemID(s.Page(), 1)
	if err != nil {
		ctx.Pool.Unpin(s)
		t.Fatalf("PageGetItemID: %v", err)
	}
	setItemIDRaw(s.Page(), 1, item.Offset+1, storage.ItemIDNormal, item.Length)
	ctx.Pool.MarkDirty(s)
	ctx.Pool.Unpin(s)

	rows := runQuery(t, ctx, "SELECT blkno, offnum, attnum, msg FROM verify_heapam('vh')")
	if len(rows) == 0 {
		t.Fatal("injected corruption produced no rows")
	}
	r := rows[0]
	if got := r[0].Int; got != 0 {
		t.Errorf("blkno = %d, want 0", got)
	}
	if got := r[1].Int; got != 1 {
		t.Errorf("offnum = %d, want 1 (corrupted slot)", got)
	}
	if !r[2].IsNull() {
		t.Errorf("attnum = %v, want NULL for a page-structural finding", r[2])
	}
	want := "line pointer to page offset " + itoaVH(int(item.Offset)+1) + " is not maximally aligned"
	if got := r[3].StringValue(); got != want {
		t.Errorf("msg = %q, want %q", got, want)
	}
}

// TestVerifyHeapam_StartBlockOutOfRange confirms an out-of-range block argument
// surfaces as an error (upstream invalid-parameter-value ereport).
func TestVerifyHeapam_StartBlockOutOfRange(t *testing.T) {
	ctx, cleanup := verifyHeapamSetup(t)
	defer cleanup()

	_, err := runQueryWithErr(ctx, "SELECT * FROM verify_heapam('vh', false, false, 'none', 999, 999)")
	if err == nil {
		t.Fatal("out-of-range startblock did not error")
	}
}

func itoaVH(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
