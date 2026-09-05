package executor

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/common/pglz"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// ToastThreshold is the value length above which a column is stored
// out-of-line in the TOAST relation. Mirrors PostgreSQL's
// TOAST_TUPLE_THRESHOLD (≈ 2 KB). Values smaller than this limit are
// stored inline in the main heap page.
const ToastThreshold = 2000

// ToastMaxChunkSize is the maximum payload size per TOAST chunk row.
// Each chunk carries at most this many bytes of the original value.
const ToastMaxChunkSize = 1996

// toastCompressedFlag marks, in the high bit of a TOAST pointer's
// num_chunks word, that the out-of-line bytes are a PGLZ-compressed inline
// varlena (VARATT_IS_4B_C, built by pglz.BuildCompressedVarlena) rather
// than the raw value, and must be run through pglz.DecodeInlineCompressed
// on detoast. Free to steal because num_chunks is bounded by
// maxDetoastChunks (1<<20) and never approaches 2^31.
const toastCompressedFlag = uint32(1) << 31

// Detoast sanity bounds reject corrupted or accidental TOAST pointers before
// they can trigger unbounded allocations during reassembly.
const (
	maxDetoastChunks   = 1 << 20
	maxDetoastTotalLen = maxDetoastChunks * ToastMaxChunkSize
)

// detoastValueCalls counts DetoastValue invocations process-wide (EX1-03).
// The cost model is honest only when measured: each resolved pointer costs
// one TOAST-relation sequential scan (no chunk index), so the win is purely
// fewer resolutions (proportional to referenced columns), never faster
// resolution. The witness matrix asserts these counts, not just time.
// Tests reset via ResetDetoastValueCalls and read via DetoastValueCalls.
var detoastValueCalls atomic.Int64

// DetoastValueCalls returns the process-wide DetoastValue invocation count.
func DetoastValueCalls() int64 {
	return detoastValueCalls.Load()
}

// ResetDetoastValueCalls zeroes the DetoastValue invocation count (tests).
func ResetDetoastValueCalls() {
	detoastValueCalls.Store(0)
}

// toastOIDCounter is a process-global counter for assigning unique OIDs
// to TOAST values. It is not itself persisted — it resets to 0 on every
// process start — but the main heap (and therefore live TOAST pointers
// referencing values written before the restart) very much does survive a
// restart. Without reseeding, the counter would reissue chunk_id 1, 2, 3…
// after every restart, colliding with whatever chunk_ids are still
// physically resident in the same TOAST relation from before the restart
// and splicing unrelated rows' bytes together on detoast (root cause of
// the WordPress wp_options neighbor-row corruption, deferral ledger
// 2026-07-02; see SeedToastOIDCounter, called once at startup after the
// catalog is loaded). An atomic int64 avoids the serialisation overhead of
// a mutex for the common concurrent-insert workload.
var toastOIDCounter atomic.Int64

func toastNextOID() uint32 {
	return uint32(toastOIDCounter.Add(1))
}

// AdvanceToastOIDCounterPast bumps the process-global TOAST OID counter so
// the next-assigned OID is guaranteed to exceed used. A no-op if the
// counter is already past used. Safe to call concurrently.
func AdvanceToastOIDCounterPast(used uint32) {
	for {
		cur := toastOIDCounter.Load()
		if cur >= int64(used) {
			return
		}
		if toastOIDCounter.CompareAndSwap(cur, int64(used)) {
			return
		}
	}
}

// MaxToastChunkIDInRel scans every physically-present tuple in the TOAST
// relation toastRel — regardless of MVCC visibility, since even an
// invisible-but-still-resident row's chunk_id must never be reissued — and
// returns the highest chunk_id (TOAST OID) found. Returns (0, false, nil)
// if toastRel has no on-disk file yet (the owning table has never TOASTed
// a value), so callers can skip the scan entirely without risking the
// smgr "NBlocks recreates a removed file" pitfall.
func MaxToastChunkIDInRel(pool *storage.Pool, toastRel storage.RelFileNode) (uint32, bool, error) {
	if pool == nil || !pool.Exists(toastRel) {
		return 0, false, nil
	}
	nBlocks, err := pool.NBlocks(toastRel)
	if err != nil {
		return 0, false, err
	}
	var max uint32
	var found bool
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: toastRel, Block: blk})
		if err != nil {
			return 0, false, err
		}
		slot.RLock()
		page := slot.Page()
		if storage.IsNew(page) {
			slot.RUnlock()
			pool.Unpin(slot)
			continue
		}
		count, _ := storage.PageLinePointerCount(page)
		for s := uint16(1); s <= uint16(count); s++ {
			t, err := storage.PageGetHeapTuple(page, s)
			if err != nil {
				continue
			}
			row, err := DecodeHeapTupleRow(toastCols, t, nil)
			if err != nil || len(row) < 1 || row[0].Kind != KindInt {
				continue
			}
			id := uint32(row[0].Int)
			if !found || id > max {
				max = id
				found = true
			}
		}
		slot.RUnlock()
		pool.Unpin(slot)
	}
	return max, found, nil
}

// SeedToastOIDCounter scans the TOAST relations for every main-table
// RelFileNode passed in and advances the process-global toastOIDCounter
// past the highest chunk_id found in any of them. Must be called once at
// startup, after the catalog has been fully populated from the on-disk
// pg_class/pg_attribute heap (loadUserTablesFromHeap) — otherwise a
// table's TOAST relation may not resolve to the right RelFileNode. Callers
// pass one entry per user table (e.g. cat.RelFileNode(tbl) for each table
// in cat.AllTables()); scanning a table that never TOASTed anything is
// cheap (MaxToastChunkIDInRel short-circuits on Pool.Exists).
func SeedToastOIDCounter(pool *storage.Pool, mainRels []storage.RelFileNode) error {
	for _, mainRel := range mainRels {
		toastRel := ToastRelFor(mainRel)
		max, found, err := MaxToastChunkIDInRel(pool, toastRel)
		if err != nil {
			return fmt.Errorf("seed TOAST OID counter for rel %d: %w", mainRel.RelOid, err)
		}
		if found {
			AdvanceToastOIDCounterPast(max)
		}
	}
	return nil
}

// toastRelOIDOffset is added to the main-heap RelOid to derive the TOAST
// table's RelOid. Keeps the TOAST file separate from the main heap without
// requiring a full catalog entry.
const toastRelOIDOffset = uint32(100_000_000)

// toastCols is the fixed column schema for every TOAST table.
// chunk_id  int4 — identifies the TOAST value (its OID)
// chunk_seq int4 — sequence number within the value, starting at 0
// chunk_data bytea — the raw payload bytes for this chunk
var toastCols = []catalog.Column{
	{Name: "chunk_id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	{Name: "chunk_seq", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	{Name: "chunk_data", Type: catalog.Type{Name: "bytea"}, Ordinal: 2},
}

// ToastRelFor returns the RelFileNode of the TOAST table for mainRel.
func ToastRelFor(mainRel storage.RelFileNode) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  mainRel.DBOid,
		RelOid: mainRel.RelOid + toastRelOIDOffset,
		Fork:   storage.MainFork,
	}
}

// isToastableType returns true when a column type may store arbitrarily-long
// data and therefore benefits from out-of-line storage.
func isToastableType(typeName string) bool {
	switch typeName {
	case "text", "varchar", "character varying",
		"char", "character", "bpchar",
		"bytea", "unknown", "json", "jsonb", "xml":
		return true
	}
	return false
}

// needsDetoast reports whether any datum in row is an unresolved TOAST pointer.
func needsDetoast(row Row) bool {
	for _, d := range row {
		if d.Kind == KindToastPointer {
			return true
		}
	}
	return false
}

// needsDetoastPrefix is needsDetoast over row[0:n], for callers holding a
// partially-deformed row whose tail is stale (seqScanOp's prefilter path).
// Scanning the whole row there would test the previous tuple's columns.
func needsDetoastPrefix(row Row, n int) bool {
	if n > len(row) {
		n = len(row)
	}
	for i := 0; i < n; i++ {
		if row[i].Kind == KindToastPointer {
			return true
		}
	}
	return false
}

// ToastLargeColumnsIfNeeded scans row for columns whose encoded length
// exceeds ToastThreshold and stores them in the TOAST relation, replacing
// the oversized datum with a KindToastPointer datum.
//
// Returns the (possibly modified) row. A new slice is only allocated when
// at least one column is toasted; otherwise row is returned as-is.
func ToastLargeColumnsIfNeeded(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) (Row, error) {
	if ctx == nil || ctx.Pool == nil {
		return row, nil
	}
	toastRel := ToastRelFor(rel)
	var newRow Row
	for i, col := range cols {
		if !isToastableType(col.Type.Name) {
			continue
		}
		d := row[i]
		if d.IsNull() || d.Kind == KindToastPointer {
			continue
		}
		var data []byte
		switch d.Kind {
		case KindString:
			data = []byte(d.StringValue())
		case KindBytes:
			data = d.BytesValue()
		default:
			continue
		}
		if len(data) <= ToastThreshold {
			continue
		}
		// PG's toast_tuple_try_compression: before pushing an oversized
		// value out-of-line, try to PGLZ-compress it, unless the column's
		// STORAGE is EXTERNAL (which forbids compression). Keep the
		// compressed form only when it is strictly smaller than the raw
		// bytes, mirroring toast_compress_datum's "VARSIZE(tmp) <
		// VARSIZE(value)" acceptance test — an incompressible value is
		// stored raw exactly as before.
		storeData, compressed := data, false
		if col.Storage != "external" {
			if comp := pglz.Compress(data); comp != nil {
				blob := pglz.BuildCompressedVarlena(comp, len(data))
				if len(blob) < len(data) {
					storeData, compressed = blob, true
				}
			}
		}
		// Need to toast this column — lazily allocate the output row.
		if newRow == nil {
			newRow = make(Row, len(row))
			copy(newRow, row)
		}
		ptr, err := toastStore(ctx, toastRel, storeData, compressed)
		if err != nil {
			return nil, fmt.Errorf("TOAST %s: %w", col.Name, err)
		}
		newRow[i] = NewToastPointerDatum(ptr)
	}
	if newRow != nil {
		return newRow, nil
	}
	return row, nil
}

// toastStore slices data into ToastMaxChunkSize chunks, writes each chunk
// as a tuple in the TOAST relation, and returns the 12-byte pointer that
// will be embedded in the main heap tuple.
func toastStore(ctx *Context, toastRel storage.RelFileNode, data []byte, compressed bool) ([]byte, error) {
	oid := toastNextOID()
	var seq int32
	for off := 0; off < len(data); off += ToastMaxChunkSize {
		end := off + ToastMaxChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunkData := append([]byte(nil), data[off:end]...)
		chunkRow := Row{
			{Kind: KindInt, Int: int64(oid)},
			{Kind: KindInt, Int: int64(seq)},
			NewBytesDatum(chunkData),
		}
		// PG-native physical format (M0111-0002): TOAST chunks are a normal
		// heap table, so they follow the single unified row format. The three
		// columns (oid, seq, bytea) are never NULL, so no bitmap is needed;
		// natts marks the tuple PG-physical for the header-driven decoder.
		body, err := EncodeRowPG(toastCols, chunkRow)
		if err != nil {
			return nil, err
		}
		tuple := storage.NewHeapTuple(ctx.Tx.XID, storage.InvalidTransactionID, body)
		tuple.Header.SetNatts(len(toastCols))
		tuple.Header.Infomask |= storage.HeapXmaxInvalid
			// M0129-S8.3c: stamp the inserting command id (cmin).
			tuple.Header.SetCmin(ctx.GetCurrentCommandId(true))
			if err := writeHeapTupleToRel(ctx, toastRel, tuple); err != nil {
			return nil, err
		}
		seq++
	}
	// 12-byte pointer: toast_oid(4) | total_len(4) | num_chunks(4).
	// total_len/num_chunks describe the bytes physically stored in the
	// TOAST relation (the compressed varlena blob when compressed==true);
	// the high bit of the num_chunks word carries the compressed flag so
	// DetoastValue knows to decode the reassembled blob.
	chunkWord := uint32(seq)
	if compressed {
		chunkWord |= toastCompressedFlag
	}
	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], oid)
	binary.BigEndian.PutUint32(ptr[4:8], uint32(len(data)))
	binary.BigEndian.PutUint32(ptr[8:12], chunkWord)
	return ptr, nil
}

// writeHeapTupleToRel appends tuple to the last (or new) page of rel.
// Lightweight version of writeHeapRowReturning that skips TOAST recursion
// and FSM/VM updates (TOAST tables are not tracked in those maps).
func writeHeapTupleToRel(ctx *Context, rel storage.RelFileNode, tuple storage.HeapTuple) error {
	raw, err := tuple.MarshalBinary()
	if err != nil {
		return err
	}
	// Mirror markHeapInsertDirty/operators_storage.go's per-insert WAL
	// discipline (not a bare ctx.Pool.MarkDirty): MarkDirty's FPI-on-
	// first-dirty-only behaviour means the 2nd-4th TOAST chunk written
	// into an already-dirty page in the same checkpoint epoch would
	// otherwise produce zero WAL output, losing those chunks on an
	// unclean crash before the next checkpoint even though the pointer
	// in the (WAL-protected) main tuple survives. See deferral-ledger
	// row appended by root-0022.
	logHeap := ctx.Pool.LogHeapInsert()
	tryAppend := func(blk storage.BlockNumber) (bool, error) {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, err
		}
		slot.Lock()
		if storage.IsNew(slot.Page()) {
			if err := storage.InitPage(slot.Page()); err != nil {
				slot.Unlock()
				ctx.Pool.Unpin(slot)
				return false, err
			}
		}
		lineSlot, addErr := storage.PageAddHeapTuple(slot.Page(), tuple)
		if addErr == nil {
			derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, raw)
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			if derr != nil {
				return false, derr
			}
			return true, nil
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		if addErr.Error() == storage.ErrNoSpaceInPage.Error() {
			return false, nil
		}
		return false, addErr
	}
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	if nBlocks > 0 {
		ok, err := tryAppend(nBlocks - 1)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	slot, blk, err := ctx.Pool.PinNewWithXID(rel, ctx.Tx.XID)
	if err != nil {
		return err
	}
	slot.Lock()
	lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, raw)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return derr
}

// DetoastValue reads a TOAST pointer and reassembles the original value
// by scanning the TOAST relation for chunks with the matching OID.
func DetoastValue(ctx *Context, toastRel storage.RelFileNode, pointer []byte) ([]byte, error) {
	detoastValueCalls.Add(1)
	if len(pointer) != 12 {
		return nil, fmt.Errorf("invalid TOAST pointer: %d bytes (want 12)", len(pointer))
	}
	oid := binary.BigEndian.Uint32(pointer[0:4])
	totalLen := int(binary.BigEndian.Uint32(pointer[4:8]))
	chunkWord := binary.BigEndian.Uint32(pointer[8:12])
	compressed := chunkWord&toastCompressedFlag != 0
	numChunks := int(chunkWord &^ toastCompressedFlag)
	if totalLen < 0 || totalLen > maxDetoastTotalLen {
		return nil, fmt.Errorf("invalid TOAST pointer: implausible total length %d", totalLen)
	}
	if numChunks <= 0 {
		if totalLen == 0 {
			return []byte{}, nil
		}
		return nil, fmt.Errorf("invalid TOAST pointer: total length %d with non-positive chunk count %d", totalLen, numChunks)
	}
	if numChunks > maxDetoastChunks {
		return nil, fmt.Errorf("invalid TOAST pointer: implausible chunk count %d", numChunks)
	}
	if totalLen > numChunks*ToastMaxChunkSize {
		return nil, fmt.Errorf("invalid TOAST pointer: total length %d exceeds %d chunks", totalLen, numChunks)
	}
	chunks := make([][]byte, numChunks)
	// review/260831 ES-17: the scan stops as soon as this value's chunks have
	// all been seen. goopg's TOAST relation has no chunk index yet (upstream
	// reads chunks through pg_toast_<rel>_index on (chunk_id, chunk_seq)), so
	// this is still a sequential scan — but it no longer keeps reading the
	// whole relation after the value has been reassembled.
	found := 0

	nBlocks, err := ctx.Pool.NBlocks(toastRel)
	if err != nil {
		return nil, fmt.Errorf("TOAST detoast NBlocks: %w", err)
	}
	for blk := storage.BlockNumber(0); blk < nBlocks && found < numChunks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: toastRel, Block: blk})
		if err != nil {
			return nil, err
		}
		slot.RLock()
		page := slot.Page()
		if storage.IsNew(page) {
			slot.RUnlock()
			ctx.Pool.Unpin(slot)
			continue
		}
		count, _ := storage.PageLinePointerCount(page)
		for s := uint16(1); s <= uint16(count); s++ {
			t, err := storage.PageGetHeapTuple(page, s)
			if err != nil {
				continue
			}
			if !transam.TupleVisible(t.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, err := DecodeHeapTupleRow(toastCols, t, nil)
			if err != nil || len(row) < 3 {
				continue
			}
			if row[0].Kind != KindInt || uint32(row[0].Int) != oid {
				continue
			}
			seq := int(row[1].Int)
			if seq < 0 || seq >= numChunks {
				continue
			}
			// decodeValue's default case returns KindString for varlen
			// types (even bytea). Accept both kinds so chunk data
			// stored as either bytes or string is correctly captured.
			var chunk []byte
			switch row[2].Kind {
			case KindBytes:
				chunk = row[2].BytesValue()
			case KindString:
				chunk = []byte(row[2].StringValue())
			default:
				continue
			}
			// A chunk_seq is unique per chunk_id, so a second sighting would be
			// a duplicate (or an older, invisible version already filtered
			// above); count each sequence once.
			if chunks[seq] == nil {
				found++
			}
			chunks[seq] = chunk
			if found == numChunks {
				break
			}
		}
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
	}
	result := make([]byte, 0, totalLen)
	for _, c := range chunks {
		result = append(result, c...)
	}
	if compressed {
		// The reassembled bytes are the inline-compressed varlena built by
		// pglz.BuildCompressedVarlena at store time; decode back to the raw
		// value (the length is self-described in the varlena's va_tcinfo).
		orig, _, err := pglz.DecodeInlineCompressed(result)
		if err != nil {
			return nil, fmt.Errorf("detoast decompress oid %d: %w", oid, err)
		}
		return orig, nil
	}
	return result, nil
}

// detoastedDatumForType restores the datum kind for reassembled TOAST
// bytes, mirroring the store path (ToastLargeColumnsIfNeeded accepts only
// KindString/KindBytes payloads): bytea columns come back as KindBytes,
// every other toastable type as KindString.
func detoastedDatumForType(col catalog.Column, data []byte) Datum {
	if col.Type.Name == "bytea" {
		return NewBytesDatum(data)
	}
	return NewStringDatum(string(data))
}

// DetoastAttr resolves a single KindToastPointer attribute, replacing it
// with the original (detoasted) string/bytes (EX1-03b). A non-pointer datum
// is returned unchanged; sibling attributes are never touched — the caller
// owns the row copy discipline. mainRel is the heap relation the datum came
// from; the TOAST relation is derived from it via ToastRelFor.
func DetoastAttr(ctx *Context, mainRel storage.RelFileNode, col catalog.Column, d Datum) (Datum, error) {
	if d.Kind != KindToastPointer {
		return d, nil
	}
	if ctx == nil || ctx.Pool == nil {
		return d, nil
	}
	data, err := DetoastValue(ctx, ToastRelFor(mainRel), d.BytesValue())
	if err != nil {
		return Datum{}, fmt.Errorf("detoast column %s: %w", col.Name, err)
	}
	return detoastedDatumForType(col, data), nil
}

// DetoastRow resolves any KindToastPointer datums in row, replacing them
// with the original (detoasted) string/bytes. Returns the row unchanged
// if no detoasting is needed. mainRel is the heap relation the row came
// from; the TOAST relation is derived from it via ToastRelFor.
func DetoastRow(ctx *Context, mainRel storage.RelFileNode, cols []catalog.Column, row Row) (Row, error) {
	return DetoastRowBound(ctx, mainRel, cols, row, len(row))
}

// DetoastRowBound resolves KindToastPointer datums at positions i < bound
// only (EX1-03a). Narrowed detoast is sound IFF the reference walk is
// complete — the scan's deform/survivor bound already excludes exactly the
// columns no consumer reads ("walk-completeness is the whole safety story";
// there is no independent guard). Positions at/past bound keep whatever the
// caller left there (stale or poisoned); callers must therefore pair this
// with prefix-scoped needsDetoastPrefix scanning, never whole-row
// needsDetoast — a stale tail pointer would otherwise false-positive the
// skip-undetoastable path and skip a LIVE tuple. A bound at/above the row
// width behaves exactly as DetoastRow. Whole-row DetoastRow survives only
// where no bound exists (DML/EPQ/COPY paths — untouched).
func DetoastRowBound(ctx *Context, mainRel storage.RelFileNode, cols []catalog.Column, row Row, bound int) (Row, error) {
	if bound > len(row) {
		bound = len(row)
	}
	if bound < 0 {
		bound = 0
	}
	if ctx == nil || ctx.Pool == nil || !needsDetoastPrefix(row, bound) {
		return row, nil
	}
	toastRel := ToastRelFor(mainRel)
	newRow := make(Row, len(row))
	copy(newRow, row)
	for i := 0; i < bound; i++ {
		d := newRow[i]
		if d.Kind != KindToastPointer {
			continue
		}
		data, err := DetoastValue(ctx, toastRel, d.BytesValue())
		if err != nil {
			colName := ""
			if i < len(cols) {
				colName = cols[i].Name
			}
			return nil, fmt.Errorf("detoast column %s: %w", colName, err)
		}
		if i < len(cols) {
			newRow[i] = detoastedDatumForType(cols[i], data)
		} else {
			newRow[i] = NewStringDatum(string(data))
		}
	}
	return newRow, nil
}

// updateSetRefCols collects the scanned-row column ordinals read by the
// UPDATE SET-clause expressions (EX1-03b). It mirrors deformScanRefs'
// positively-understood set (plain column reads, constants, and the
// transparent comparison/arithmetic/boolean/cast/is-null wrappers): any
// other shape — FuncCall, subqueries, LIKE...ESCAPE patterns, whole-row
// refs — declines (fullRow=true) and the caller falls back to whole-row
// detoast. A missed arm costs performance, never correctness; an
// unattributed read would be silent corruption, so the default is decline.
// Out-of-range ordinals decline too: evalExpr would not read the scanned
// row for them, but attributing them to a sibling would be wrong.
// A (non-nil-empty, false) result means no SET expression reads any
// scanned column, so the eval row needs no detoasting at all.
func updateSetRefCols(set []optimizer.Expr, ncols int) (refs []int, fullRow bool) {
	seen := make(map[int]bool)
	out := []int{}
	for _, e := range set {
		if e == nil {
			continue
		}
		if !collectToastRefCols(e, &out, seen, ncols) {
			return nil, true
		}
	}
	return out, false
}

func collectToastRefCols(e optimizer.Expr, out *[]int, seen map[int]bool, ncols int) bool {
	switch x := e.(type) {
	case nil:
		// Diverges from deformScanRefs (which declines nil): a nil SET
		// arm reads nothing, so attributing it as ref-free is exact.
		return true
	case *optimizer.ColumnRef:
		if x.Index < 0 || x.Index >= ncols {
			return false
		}
		if !seen[x.Index] {
			seen[x.Index] = true
			*out = append(*out, x.Index)
		}
		return true
	case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
		*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
		*optimizer.BooleanConst:
		return true
	case *optimizer.BinaryOp:
		return collectToastRefCols(x.Left, out, seen, ncols) &&
			collectToastRefCols(x.Right, out, seen, ncols)
	case *optimizer.UnaryOp:
		return collectToastRefCols(x.Operand, out, seen, ncols)
	case *optimizer.CastExpr:
		return collectToastRefCols(x.Operand, out, seen, ncols)
	case *optimizer.IsNullExpr:
		return collectToastRefCols(x.Operand, out, seen, ncols)
	case *optimizer.IsBoolExpr:
		return collectToastRefCols(x.Operand, out, seen, ncols)
	case *optimizer.IsDistinctFromExpr:
		return collectToastRefCols(x.Left, out, seen, ncols) &&
			collectToastRefCols(x.Right, out, seen, ncols)
	default:
		return false
	}
}

// detoastUpdateEvalRow builds the UPDATE SET-clause evaluation row
// (EX1-03b): when the SET expressions' column reads are fully attributed,
// only those attributes are resolved via DetoastAttr and siblings keep
// their raw TOAST pointer — mirroring PG's behaviour of leaving an
// unchanged TOASTed datum alone instead of needlessly re-toasting it on
// write. When fullRow is set (some SET shape declined) it falls back to
// whole-row DetoastRow, the exact pre-EX1-03 behaviour. With no attributed
// reads at all the row is returned untouched: no SET expression observes
// it, so there is nothing to resolve.
func detoastUpdateEvalRow(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row, refs []int, fullRow bool) (Row, error) {
	if len(refs) == 0 && !fullRow {
		return row, nil
	}
	// Full-width invariant: scanMatching hands this a full-width row, so
	// whole-row needsDetoast is exact here. Never call this with a
	// narrowed row — pair narrowed rows with needsDetoastPrefix.
	if !needsDetoast(row) {
		return row, nil
	}
	if fullRow {
		return DetoastRow(ctx, rel, cols, row)
	}
	out := make(Row, len(row))
	copy(out, row)
	for _, ci := range refs {
		if ci < 0 || ci >= len(out) || ci >= len(cols) {
			// Unreachable: updateSetRefCols declines out-of-range
			// ordinals to fullRow. Loud (not silent-skip): an
			// unattributed read would be silent corruption.
			return nil, fmt.Errorf("detoast UPDATE eval row: ref %d out of range (row %d, cols %d)", ci, len(out), len(cols))
		}
		if out[ci].Kind != KindToastPointer {
			continue
		}
		dd, err := DetoastAttr(ctx, rel, cols[ci], out[ci])
		if err != nil {
			return nil, err
		}
		out[ci] = dd
	}
	return out, nil
}

// sortChunks orders a slice of (seq, data) pairs by seq for deterministic
// reassembly. Used when chunks arrive out of order.
func sortChunks(seqs []int, chunks [][]byte) {
	type pair struct {
		seq  int
		data []byte
	}
	pairs := make([]pair, len(seqs))
	for i := range seqs {
		pairs[i] = pair{seqs[i], chunks[i]}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].seq < pairs[j].seq })
	for i, p := range pairs {
		seqs[i] = p.seq
		chunks[i] = p.data
	}
}

var _ = sortChunks // suppress unused warning; chunks already arrive in order
