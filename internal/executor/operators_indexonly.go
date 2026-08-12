package executor

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// indexOnlyScanOp implements IndexOnlyScan (M0046-0004): when the Visibility
// Map marks a heap page ALL_VISIBLE, the operator decodes projected column
// values from the B-tree key bytes and returns them without fetching the heap.
// For pages not yet marked ALL_VISIBLE it falls back to a full heap fetch
// so MVCC visibility is always respected.
type indexOnlyScanOp struct {
	plan *planner.IndexOnlyScan
	ctx  *Context
	rows []Row
	idx  int
	// M0092-0007: embedded slot reused across every Next() call
	// so we don't allocate a fresh MaterializedSlot per emission.
	slot MaterializedSlot
	// heapFetchCount, when non-nil, is incremented once per index entry
	// whose heap page was NOT ALL_VISIBLE and therefore required a heap
	// fetch — the EXPLAIN ANALYZE "Heap Fetches" tally (design 0118-0102).
	// Set by maybeInstrument only under EXPLAIN ANALYZE; nil otherwise.
	heapFetchCount *int64
	// touchedBlocks records the heap blocks visited on the non-ALL_VISIBLE
	// fallback path; for a TEMPORARY relation they are opportunistically pruned
	// after the scan so a subsequent index-only scan reflects PG's prune-on-read
	// (horizons.spec, M0118-0009). nil keys are never inserted.
	touchedBlocks map[storage.BlockNumber]struct{}
}

// setHeapFetchCounter implements the heapFetchCounter interface so EXPLAIN
// ANALYZE can surface this scan's heap-fetch count (design 0118-0102).
func (o *indexOnlyScanOp) setHeapFetchCounter(c *int64) { o.heapFetchCount = c }

func newIndexOnlyScanOp(p *planner.IndexOnlyScan) *indexOnlyScanOp {
	return &indexOnlyScanOp{plan: p}
}

func (o *indexOnlyScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *indexOnlyScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "IndexOnlyScan requires storage handles"}
	}
	if o.plan.Table != nil && !dmlPrivilegePermittedAs(ctx, o.plan.Table, "SELECT", selectPrivilegeCheckRole(ctx, o.plan.PrivilegeCheckRoleSet, o.plan.PrivilegeCheckRole)) {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	o.ctx = ctx
	o.rows = nil
	o.idx = 0

	heapRel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(heapRel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireScanReadLockTxn(heapRel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	// PostgreSQL locks every index of the scanned relation in AccessShare, not
	// only the one this scan reads (get_relation_info opens them all). M0118-0008
	// (partition-drop-index-locking).
	if err := ctx.acquireScanIndexReadLocksTxn(o.plan.Table); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}

	// M0118-0009 (design 0118-0099): a hash index supports only equality, and PG
	// takes a PAGE-grain predicate lock on the probed bucket (PredicateLockPage in
	// hash.c) rather than a relation-grain lock — so a concurrent INSERT into a
	// DIFFERENT bucket forms no rw-conflict (predicate-hash "reduced false
	// positives"). For a hash index we therefore SKIP the relation-grain SIREAD
	// here and instead take the bucket-grain lock once the probe key is encoded
	// (below); the per-tuple heap reads also switch to conflict-out-only so they
	// do not coarsen into a heap-page lock that would re-introduce the false
	// positive. DeclaredHash marks an index created `USING hash` (Method stays
	// "btree" since goopg builds it on the B-tree substrate).
	isHashIdx := o.plan.Index != nil && o.plan.Index.DeclaredHash
	// M0118-0001: a SERIALIZABLE index-only scan takes a relation-level SIREAD
	// predicate lock on the heap relation, exactly like the seq-scan and
	// (heap-fetching) index-scan paths. Acquired BEFORE the probe-bound lookups
	// below so it is held even when the scan matches no key — that empty-result
	// case is precisely the phantom the lock must cover (read-write-unique-2/-3
	// probe a non-existent key first, then both INSERT it). Gated to
	// SERIALIZABLE inside ssiRecordRelationRead; temp / matview relations are
	// excluded as PredicateLockingNeededForRelation requires.
	if !isHashIdx && (o.plan.Table == nil || (!o.plan.Table.Temp && !o.plan.Table.IsMatView)) {
		ssiRecordRelationRead(ctx, heapRel)
	}

	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := openIndexBTree(ctx, o.plan.Index, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}

	var loBytes, hiBytes []byte
	switch {
	case len(o.plan.Keys) > 0:
		// Full multi-column equality probe (M0054-0006 composite),
		// preserved through IOS promotion by M0116-0003. The planner
		// guarantees len(Keys) == len(Index.Columns), so the encoded
		// probe addresses one B-tree leaf entry exactly — no suffix
		// padding required.
		key, ok, err := o.lookupKeys()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
	case o.plan.Key != nil:
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
		if len(o.plan.Index.Columns) > 1 {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, key)
		}
	default:
		lo, hi, ok, err := o.lookupRangeBounds()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = lo
		hiBytes = hi
		if len(o.plan.Index.Columns) > 1 && hiBytes != nil {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, hiBytes)
		}
	}

	// Hash-index equality probe: take the bucket-grain SIREAD on the index
	// (design 0118-0099) in place of the relation-grain lock skipped above.
	// loBytes is the encoded probe key (== hiBytes for the single-column
	// equality a hash index supports). No-op outside SERIALIZABLE.
	if isHashIdx && len(loBytes) > 0 {
		ssiRecordHashBucketRead(ctx, heapRel.DBOid, o.plan.Index.OID, loBytes)
	}

	// Some key encodings cannot be inverted (interval's key is the lossy
	// comparison span — btree_interval_key.go), so the decode-from-key fast
	// path below has nothing to decode. Reading the heap instead is always
	// correct, just slower — and it is what the ALL_VISIBLE flag is an
	// optimization over — whereas letting decodeRowFromKey error would fail the
	// whole query. Decided once per scan: it is a property of the index's
	// column types, not of the row.
	keyDecodable := o.indexKeyIsDecodable()

	scanFn := func(key []byte, ptr storage.ItemPointer) (bool, error) {
		// Fast path: ALL_VISIBLE → decode from key, zero heap reads.
		if keyDecodable && ctx.VM != nil && ctx.VM.AllVisible(heapRel, ptr.Block) {
			row, err := o.decodeRowFromKey(key)
			if err != nil {
				return false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
					Message: fmt.Sprintf("IOS decode: %v", err)}
			}
			o.rows = append(o.rows, row)
			return true, nil
		}

		// Fallback: heap fetch + HOT chain + MVCC. The page was not ALL_VISIBLE.
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Peek the index entry's root line pointer first. If it was reclaimed
		// (LP_UNUSED / LP_DEAD) by a prior prune — the goopg analog of an index
		// entry PG's kill_prior_tuple would have marked LP_DEAD — this entry
		// resolves to no heap tuple: it costs no heap fetch and yields no row.
		// Skipping it here is what drops "Heap Fetches" to 0 on a re-scan after a
		// temp relation's deleted rows are pruned (horizons.spec, M0118-0009).
		if rootID, idErr := storage.PageGetItemID(slot.Page(), ptr.Offset); idErr == nil &&
			(rootID.Flags == storage.ItemIDUnused || rootID.Flags == storage.ItemIDDead) {
			slot.RUnlock()
			ctx.Pool.Unpin(slot)
			return true, nil
		}
		// A genuine heap fetch — count it for EXPLAIN ANALYZE "Heap Fetches"
		// (design 0118-0102), mirroring upstream's ioss_HeapFetches++ which fires
		// per visited entry regardless of the eventual visibility verdict.
		if o.heapFetchCount != nil {
			*o.heapFetchCount++
		}
		if o.plan.Table != nil && o.plan.Table.Temp {
			if o.touchedBlocks == nil {
				o.touchedBlocks = make(map[storage.BlockNumber]struct{})
			}
			o.touchedBlocks[ptr.Block] = struct{}{}
		}
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, ctx.Snap, ctx.Tx.XID, ctx.MultiXact,
		ctx.CmdID, ctx.comboStore())
		// M0118-0001: SSI phantom conflict-out for an index-only-scanned tuple
		// present at this TID but invisible because a concurrent transaction
		// inserted it — the IOS analog of the seq-scan invisible-tuple path. The
		// VM bit is cleared by a concurrent in-flight insert, so this fallback
		// (non-ALL_VISIBLE) branch is exactly where that phantom surfaces.
		var invisXmin storage.TransactionID
		if !found && ssiActive(ctx) {
			if raw, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset); terr == nil {
				invisXmin = raw.Header.Xmin
			}
		}
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if !found {
			if invisXmin != storage.InvalidTransactionID {
				if serr := ssiRecordInvisibleTupleRead(ctx, heapRel, invisXmin); serr != nil {
					return false, serr
				}
			}
			return true, nil
		}
		// M0118-0001: SSI read-path per-tuple conflict-out on the HOT-resolved
		// live version. The ALL_VISIBLE fast path needs no heap tuple, but this
		// non-ALL_VISIBLE fallback already fetched it, so record the rw-edge
		// against the tuple's xmin (a concurrent inserter) AND xmax (a concurrent
		// deleter/updater) — the latter is the referential-integrity write-skew
		// edge where this reader sees a row an in-flight SERIALIZABLE peer has
		// DELETEd. Mirrors the heap-fetching index-scan path
		// (operators_index.go); the helper short-circuits for RC/RR and the
		// tuple-grain predicate lock it would take is pruned by the relation-grain
		// SIREAD already held (ssiRecordRelationRead above). The page RLock/pin is
		// released above, so a non-nil error (reader closed a dangerous structure
		// to an already-committed writer) propagates as a mid-statement 40001.
		if isHashIdx {
			// Hash bucket page lock already covers the phantom (above); a heap
			// tuple SIREAD here would coarsen to a heap-page lock and re-introduce
			// the different-bucket false positive. Keep conflict-out only so the
			// write-before-read same-bucket edge still forms (design 0118-0099).
			if serr := ssiConflictOutTupleRead(ctx, tuple.Header.Xmin, tuple.Header.Xmax); serr != nil {
				return false, serr
			}
		} else if serr := ssiRecordTupleRead(ctx, heapRel, ptr.Block, actualSlot, tuple.Header.Xmin, tuple.Header.Xmax); serr != nil {
			return false, serr
		}
		row, err := o.decodeRowFromHeap(tuple)
		if err != nil {
			return false, err
		}
		o.rows = append(o.rows, row)
		return true, nil
	}

	if err := tree.RangeScan(loBytes, hiBytes, scanFn); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}

	// Prune-on-read for TEMPORARY relations (horizons.spec, M0118-0009). PG
	// opportunistically prunes a heap page whenever a scan visits it; for a temp
	// relation that prune uses the session-local horizon (GlobalVisTempRels), so
	// rows the owning backend deleted in a committed statement are reclaimed even
	// while another session holds an older snapshot — that session cannot see the
	// temp table at all. Permanent relations are deliberately NOT pruned here:
	// their reclamation flows through VACUUM at the global horizon. We do NOT set
	// the VM ALL_VISIBLE bit, so a subsequent scan stays on this heap-checking
	// fallback path and skips the now-LP_UNUSED entries (see the root-line-pointer
	// peek above) instead of trusting the index key, which would resurrect the
	// deleted rows.
	o.pruneTouchedTempPages(ctx, heapRel)
	return nil
}

// pruneTouchedTempPages opportunistically reclaims dead tuples on the heap
// blocks this index-only scan fetched, for a TEMPORARY relation only, using the
// session-local horizon. Mirrors vacuumCore's reclamation kernel
// (storage.PageVacuumPrune + the LogHeapPruneOpt WAL hook / MarkDirty fallback)
// but takes no relation lock and never touches the Visibility Map. Best-effort:
// any error leaves the page untouched rather than failing the read.
func (o *indexOnlyScanOp) pruneTouchedTempPages(ctx *Context, heapRel storage.RelFileNode) {
	if len(o.touchedBlocks) == 0 || ctx.Pool == nil || ctx.TxnMgr == nil {
		return
	}
	if o.plan.Table == nil || !o.plan.Table.Temp {
		return
	}
	horizon := ctx.TxnMgr.OldestXminForProc(int32(ctx.Tx.Handle) - 1)
	if horizon == storage.InvalidTransactionID {
		return
	}
	logPrune := ctx.Pool.LogHeapPruneOpt()
	for blk := range o.touchedBlocks {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: blk})
		if err != nil {
			continue
		}
		page := slot.Page()
		if storage.IsNew(page) {
			ctx.Pool.Unpin(slot)
			continue
		}
		slot.Lock()
		pr, _, perr := storage.PageVacuumPrune(page, horizon)
		if perr != nil || (len(pr.Redirects) == 0 && len(pr.Unused) == 0) {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			continue
		}
		if logPrune != nil {
			blkCopy := blk
			_ = ctx.Pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
				return logPrune(heapRel, blkCopy, pr.Redirects, pr.Unused)
			})
		} else {
			ctx.Pool.MarkDirty(slot)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}
}

func (o *indexOnlyScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	// M0092-0007: stack-aliased slot reused across Next() calls.
	o.slot.schema = o.Schema()
	o.slot.row = r
	return &o.slot, nil
}

func (o *indexOnlyScanOp) Close() error {
	o.rows = nil
	return nil
}

// indexKeyIsDecodable reports whether decodeRowFromKey can invert this index's
// key. False for an index any of whose key columns has a deliberately
// non-invertible encoding — `interval`, whose key is upstream's
// interval_cmp_value span (btree_interval_key.go), and any ARRAY whose element
// type the key layer cannot render back (btree_key_decodable.go, which owns the
// whole answer). The whole index is declined rather than the single column
// because the composite walk decodes columns in order and cannot skip one whose
// byte width it does not know.
//
// Not consulted on the pgIndexKeyDesc (PG tuple-image) path: there the key
// carries per-attribute datums, so nothing is lost — but such an index does not
// take the fast path through this predicate either, since decodeRowFromKey
// routes on the descriptor first.
func (o *indexOnlyScanOp) indexKeyIsDecodable() bool {
	if o.plan.Index == nil || o.ctx == nil || o.ctx.Catalog == nil {
		return true
	}
	for _, colName := range o.plan.Index.Columns {
		col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !ok {
			continue // the decode path reports this one itself
		}
		if !indexKeyColumnIsDecodable(*col) {
			return false
		}
	}
	return true
}

// decodeRowFromKey extracts covered column values from a B-tree key.
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
	// Tuple format (M0130-S11.4 slice 3b-2c-ii-B2-c, the flip): the key is one
	// IndexTuple image, not a concatenation of order-preserving segments, so
	// neither the single-column fast lane nor the running-offset loop below can
	// read it — they would consume a type's width out of a null bitmap and an
	// alignment hole. `pgIndexTupleKeyDatums` is the inverse of the encoder that
	// wrote it; the projection onto Covered is shared with the blob path, since
	// only the DECODE differs between the two formats, never which columns the
	// scan is answering from.
	if desc := o.ctx.pgIndexKeyDesc(o.plan.Index); desc != nil {
		keyCols := pgIndexKeyColumns(o.plan.Index)
		vals, err := pgIndexTupleKeyDatums(desc, keyCols, key)
		if err != nil {
			return nil, err
		}
		decoded := make(map[string]Datum, len(keyCols))
		for i, col := range keyCols {
			decoded[col.Name] = vals[i]
		}
		return o.projectCovered(decoded)
	}
	if len(o.plan.Index.Columns) == 1 && len(o.plan.Covered) == 1 {
		d, err := decodeBTreeKeyToDatum(key, o.plan.Covered[0])
		if err != nil {
			return nil, err
		}
		return Row{d}, nil
	}
	// Multi-column: decode all key columns in declaration order, then project.
	decoded := make(map[string]Datum, len(o.plan.Index.Columns))
	off := 0
	for _, colName := range o.plan.Index.Columns {
		col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !ok {
			return nil, fmt.Errorf("IOS: index column %q not in catalog", colName)
		}
		d, n, err := decodeIndexKeyColumn(key[off:], *col)
		if err != nil {
			return nil, fmt.Errorf("IOS key col %q: %w", colName, err)
		}
		decoded[colName] = d
		off += n
	}
	return o.projectCovered(decoded)
}

// projectCovered picks the scan's output columns out of the decoded key
// attributes. Shared by both key formats — see decodeRowFromKey.
func (o *indexOnlyScanOp) projectCovered(decoded map[string]Datum) (Row, error) {
	row := make(Row, len(o.plan.Covered))
	for i, col := range o.plan.Covered {
		d, ok := decoded[col.Name]
		if !ok {
			return nil, fmt.Errorf("IOS: covered column %q not decoded", col.Name)
		}
		row[i] = d
	}
	return row, nil
}

// numericDatumFromBig wraps a decoded NUMERIC mantissa/scale pair in a Datum,
// taking the int64 fast lane when the mantissa fits and the big.Int overflow
// lane otherwise — the same split the codec makes (see datum.go). M0119-0006.
func numericDatumFromBig(m *big.Int, scale int16) Datum {
	if m.IsInt64() {
		return NewNumericInt64Datum(m.Int64(), scale)
	}
	return NewNumericBigDatum(m, scale)
}

// decodeIndexKeyColumn decodes one column from a B-tree key slice and returns
// the Datum plus the number of bytes consumed. Used by the multi-column path.
func decodeIndexKeyColumn(key []byte, col catalog.Column) (Datum, int, error) {
	typeName := col.Type.Name
	// Fixed-width branches must slice `key` to the type's exact byte
	// width before delegating — btree.DecodeInt4 / DecodeInt8 enforce
	// `len(b) == width`, which fails when the multi-column loop passes
	// the still-trailing remainder of a composite key.
	// ARRAY column: Type.Name is the ELEMENT type, so every predicate below (and
	// in decodeScalarBTreeKey) answers for the element and would consume the
	// element's width out of a longer array segment. Route arrays first, the
	// mirror of encodeBTreeKeyForColumn's own array-first routing. M0119-0006.
	if col.Type.IsArray {
		return decodeArrayBTreeKey(key, col)
	}
	// int2 / oid / bool / bytea / time (btree_scalar_keys.go). Routed BEFORE the
	// switch below so neither of these types can reach the `default:` arm, which
	// reads any 8 leading bytes as an enum float8 without ever erroring.
	if d, n, handled, err := decodeScalarBTreeKey(key, typeName); handled {
		return d, n, err
	}
	switch {
	case isInt4Type(typeName):
		if len(key) < 4 {
			return NullDatum, 0, fmt.Errorf("btree: int4 key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeInt4(key[:4])
		return Datum{Kind: KindInt, Int: int64(v)}, 4, err
	case isInt8Type(typeName):
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: int8 key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeInt8(key[:8])
		return Datum{Kind: KindInt, Int: v}, 8, err
	case isFloat8Type(typeName):
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: float8 key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeFloat8(key[:8])
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), 8, err
	case isTimestampType(typeName) || isTimestamptzType(typeName):
		// timestamp and timestamptz share the int64-micros key form. M0118-0001.
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: timestamp key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeTimestamp(key[:8])
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		// The key form is shared but the TYPE is not: an index-only scan answers
		// the column from the key, so this datum reaches the user in place of the
		// heap's, and must carry the same subtype the heap decode assigns
		// (decodeValuePG, codec.go). M0119-0006 (41st slice).
		if isTimestamptzType(typeName) {
			return NewTimestampTZDatum(ts), 8, err
		}
		return NewTimeDatum(ts), 8, err
	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		if len(key) < 4 {
			return NullDatum, 0, fmt.Errorf("btree: date key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeInt4(key[:4])
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewDateDatum(ts), 4, err
	case isNumericType(typeName):
		// NUMERIC keys are variable-length but self-delimiting, so the
		// composite walk can consume exactly this column. Value-preserving,
		// not byte-preserving: EncodeNumericKey strips trailing mantissa
		// zeros, so 1.50 decodes as (15, scale 1). M0119-0006.
		m, scale, n, err := btree.DecodeNumericKey(key)
		if err != nil {
			return NullDatum, 0, err
		}
		return numericDatumFromBig(m, scale), n, nil
	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName),
		strings.ToLower(typeName) == "uuid":
		raw, n, err := btree.DecodeVarcharLen(key)
		return NewStringDatum(string(raw)), n, err
	default:
		// For unknown types (e.g. user-defined enums encoded as float8), attempt float8 decode.
		// Enum values are encoded via btree.EncodeFloat8(sortOrder) in encodeBTreeKeyForColumn.
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("IOS: unsupported key type %q (too short for float8)", typeName)
		}
		f, err := btree.DecodeFloat8(key[:8])
		if err != nil {
			return NullDatum, 0, fmt.Errorf("IOS: unsupported key type %q", typeName)
		}
		// Return as KindEnum with sort order; label unknown at this stage.
		return NewEnumDatum(f, ""), 8, nil
	}
}

// decodeRowFromHeap projects only the covered columns from a full heap tuple.
func (o *indexOnlyScanOp) decodeRowFromHeap(t storage.HeapTuple) (Row, error) {
	fullRow, err := DecodeHeapTupleRow(o.plan.Table.Columns, t, nil)
	if err != nil {
		return nil, err
	}
	row := make(Row, len(o.plan.Covered))
	for i, col := range o.plan.Covered {
		for j, tc := range o.plan.Table.Columns {
			if tc.Name == col.Name && j < len(fullRow) {
				row[i] = fullRow[j]
				break
			}
		}
	}
	// Convert KindString enum values to KindEnum (sort order) for correct
	// comparison in Filter predicates. M0097-0022.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		for i, col := range o.plan.Covered {
			if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
				if row[i].Kind == KindString {
					label := row[i].StringValue()
					for _, ev := range et.Values {
						if ev.Label == label {
							row[i] = NewEnumDatum(ev.SortOrder, label)
							break
						}
					}
				}
			}
		}
	}
	return row, nil
}

// decodeBTreeKeyToDatum inverts the B-tree key encoding for a single column
// back to an executor Datum.
func decodeBTreeKeyToDatum(key []byte, col catalog.Column) (Datum, error) {
	typeName := col.Type.Name
	// Sibling of decodeIndexKeyColumn's array routing (same ordering rationale:
	// an array's Type.Name is its ELEMENT type name, so it must not reach any
	// scalar predicate). Strict on the width: a single-column key is the whole
	// key, so bytes trailing the array's end marker mean this is not the
	// encoding we think it is. M0119-0006.
	if col.Type.IsArray {
		d, n, err := decodeArrayBTreeKey(key, col)
		if err != nil {
			return NullDatum, err
		}
		if n != len(key) {
			return NullDatum, fmt.Errorf("btree: array key for column %q has %d trailing bytes", col.Name, len(key)-n)
		}
		return d, nil
	}
	// Sibling of decodeIndexKeyColumn's routing — both must invert
	// encodeScalarBTreeKey, or an int2/oid/bool/bytea/time key column decodes
	// one way in a single-column IOS and another way in a composite one.
	if d, _, handled, err := decodeScalarBTreeKey(key, typeName); handled {
		return d, err
	}
	switch {
	case isInt4Type(typeName):
		v, err := btree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: int64(v)}, nil

	case isInt8Type(typeName):
		v, err := btree.DecodeInt8(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: v}, nil

	case isFloat8Type(typeName):
		v, err := btree.DecodeFloat8(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), nil

	case isNumericType(typeName):
		// Sibling of the multi-column branch in decodeIndexKeyColumn — both
		// must invert EncodeNumericKey, or a NUMERIC key column decodes one
		// way in a single-column IOS and another way in a composite one.
		// M0119-0006.
		m, scale, _, err := btree.DecodeNumericKey(key)
		if err != nil {
			return NullDatum, err
		}
		return numericDatumFromBig(m, scale), nil

	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName),
		strings.EqualFold(typeName, "uuid"):
		// uuid rides EncodeVarchar (its canonical lowercase-hex text compares as
		// uuid_cmp's memcmp does), and its sibling decodeIndexKeyColumn has
		// always listed it here. Without this arm uuid reached the `default:`
		// enum guess below, which reads the first 8 ASCII bytes as a float8 sort
		// order and NEVER errors: a single-column index-only scan over a uuid
		// column answered from the key returned an empty enum Datum instead of
		// the uuid. Latent today only because a uuid index takes the PG
		// tuple-image key path (pgIndexTupleKeys), which is exactly why the
		// blob sibling drifted. M0119-0006, Hard-won Rule #2.
		b, err := btree.DecodeVarchar(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(string(b)), nil

	case isTimestampType(typeName) || isTimestamptzType(typeName):
		// timestamp and timestamptz share the int64-micros key form. M0118-0001.
		v, err := btree.DecodeTimestamp(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		// Sibling of decodeIndexKeyColumn's arm above — both must tag, or the
		// same column decodes one way in a single-column index-only scan and
		// another way in a composite one. M0119-0006 (41st slice).
		if isTimestamptzType(typeName) {
			return NewTimestampTZDatum(ts), nil
		}
		return NewTimeDatum(ts), nil

	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		v, err := btree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewDateDatum(ts), nil

	default:
		// Unknown type: attempt float8 decode for user-defined enums. M0097-0022.
		if len(key) < 8 {
			return NullDatum, fmt.Errorf("index-only scan: unsupported key type %q for key decode", typeName)
		}
		f, err2 := btree.DecodeFloat8(key[:8])
		if err2 != nil {
			return NullDatum, fmt.Errorf("index-only scan: unsupported key type %q for key decode", typeName)
		}
		return NewEnumDatum(f, ""), nil
	}
}

// lookupKeys evaluates Keys[i] in declared order and concatenates the
// per-column B-tree encodings, mirroring indexScanOp.lookupKeys. Carries
// the M0054-0006 composite probe through IOS promotion (M0116-0003).
// Any NULL component short-circuits to ok=false — equality on NULL is
// unknown, so the probe correctly produces zero rows.
func (o *indexOnlyScanOp) lookupKeys() ([]byte, bool, error) {
	if len(o.plan.Keys) != len(o.plan.Index.Columns) {
		return nil, false, &ExecError{
			Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexOnlyScanOp.lookupKeys: planner supplied %d keys for index %q with %d columns",
				len(o.plan.Keys), o.plan.Index.Name, len(o.plan.Index.Columns)),
		}
	}
	parts := make([]indexProbeKeyPart, 0, len(o.plan.Keys))
	for i, ke := range o.plan.Keys {
		v, err := evalExpr(ke, nil, o.ctx)
		if err != nil {
			return nil, false, err
		}
		if v.IsNull() {
			return nil, false, nil
		}
		colName := o.plan.Index.Columns[i]
		col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !found {
			return nil, false, &ExecError{
				Code: "XX000", Pos: o.plan.Pos(),
				Message: fmt.Sprintf("indexed column %q not found on table %q", colName, o.plan.Table.Name),
			}
		}
		parts = append(parts, indexProbeKeyPart{col: col, val: v, pos: ke.Pos()})
	}
	probe, encErr := o.ctx.indexProbeKey(o.plan.Index, parts)
	if encErr != nil {
		return nil, false, encErr
	}
	return probe, true, nil
}

// lookupKey evaluates the equality probe key expression.
// Note: encodeBTreeKeyForColumn returns *ExecError (not error), so we
// must guard against the nil-pointer-in-interface issue explicitly.
func (o *indexOnlyScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExpr(o.plan.Key, nil, o.ctx)
	if err != nil {
		return nil, false, err
	}
	if v.IsNull() {
		return nil, false, nil
	}
	col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !ok {
		return nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	k, encErr := o.ctx.indexProbeKey(o.plan.Index, []indexProbeKeyPart{{col: col, val: v, pos: o.plan.Key.Pos()}})
	if encErr != nil {
		return nil, false, encErr
	}
	return k, true, nil
}

func (o *indexOnlyScanOp) lookupRangeBounds() (lo, hi []byte, ok bool, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	if o.plan.LowKey != nil {
		v, evalE := evalExpr(o.plan.LowKey, nil, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := o.ctx.indexProbeKey(o.plan.Index, []indexProbeKeyPart{{col: col, val: v, pos: o.plan.LowKey.Pos()}})
		if encE != nil {
			return nil, nil, false, encE
		}
		lo = k
	}
	if o.plan.HighKey != nil {
		v, evalE := evalExpr(o.plan.HighKey, nil, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := o.ctx.indexProbeKey(o.plan.Index, []indexProbeKeyPart{{col: col, val: v, pos: o.plan.HighKey.Pos()}})
		if encE != nil {
			return nil, nil, false, encE
		}
		hi = k
	}
	return lo, hi, true, nil
}
