package executor

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
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

// Detoast sanity bounds reject corrupted or accidental TOAST pointers before
// they can trigger unbounded allocations during reassembly.
const (
	maxDetoastChunks   = 1 << 20
	maxDetoastTotalLen = maxDetoastChunks * ToastMaxChunkSize
)

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
		// Need to toast this column — lazily allocate the output row.
		if newRow == nil {
			newRow = make(Row, len(row))
			copy(newRow, row)
		}
		ptr, err := toastStore(ctx, toastRel, data)
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
func toastStore(ctx *Context, toastRel storage.RelFileNode, data []byte) ([]byte, error) {
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
		if err := writeHeapTupleToRel(ctx, toastRel, tuple); err != nil {
			return nil, err
		}
		seq++
	}
	// 12-byte pointer: toast_oid(4) | total_len(4) | num_chunks(4)
	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], oid)
	binary.BigEndian.PutUint32(ptr[4:8], uint32(len(data)))
	binary.BigEndian.PutUint32(ptr[8:12], uint32(seq))
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
		_, addErr := storage.PageAddHeapTuple(slot.Page(), tuple)
		if addErr == nil {
			ctx.Pool.MarkDirty(slot)
			slot.Unlock()
			ctx.Pool.Unpin(slot)
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
	_ = raw
	slot, blk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	slot.Lock()
	_, err = storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	ctx.Pool.MarkDirty(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	_ = blk
	return nil
}

// DetoastValue reads a TOAST pointer and reassembles the original value
// by scanning the TOAST relation for chunks with the matching OID.
func DetoastValue(ctx *Context, toastRel storage.RelFileNode, pointer []byte) ([]byte, error) {
	if len(pointer) != 12 {
		return nil, fmt.Errorf("invalid TOAST pointer: %d bytes (want 12)", len(pointer))
	}
	oid := binary.BigEndian.Uint32(pointer[0:4])
	totalLen := int(binary.BigEndian.Uint32(pointer[4:8]))
	numChunks := int(binary.BigEndian.Uint32(pointer[8:12]))
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

	nBlocks, err := ctx.Pool.NBlocks(toastRel)
	if err != nil {
		return nil, fmt.Errorf("TOAST detoast NBlocks: %w", err)
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
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
			if !mvcc.TupleVisible(t.Header, ctx.Snap, ctx.Tx.XID, ctx.MultiXact) {
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
			switch row[2].Kind {
			case KindBytes:
				chunks[seq] = row[2].BytesValue()
			case KindString:
				chunks[seq] = []byte(row[2].StringValue())
			}
		}
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
	}
	result := make([]byte, 0, totalLen)
	for _, c := range chunks {
		result = append(result, c...)
	}
	return result, nil
}

// DetoastRow resolves any KindToastPointer datums in row, replacing them
// with the original (detoasted) string/bytes. Returns the row unchanged
// if no detoasting is needed. mainRel is the heap relation the row came
// from; the TOAST relation is derived from it via ToastRelFor.
func DetoastRow(ctx *Context, mainRel storage.RelFileNode, cols []catalog.Column, row Row) (Row, error) {
	if ctx == nil || ctx.Pool == nil || !needsDetoast(row) {
		return row, nil
	}
	toastRel := ToastRelFor(mainRel)
	newRow := make(Row, len(row))
	copy(newRow, row)
	for i, d := range newRow {
		if d.Kind != KindToastPointer {
			continue
		}
		data, err := DetoastValue(ctx, toastRel, d.BytesValue())
		if err != nil {
			return nil, fmt.Errorf("detoast column %s: %w", cols[i].Name, err)
		}
		// Return as the appropriate datum kind for the column type.
		if cols[i].Type.Name == "bytea" {
			newRow[i] = NewBytesDatum(data)
		} else {
			newRow[i] = NewStringDatum(string(data))
		}
	}
	return newRow, nil
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
