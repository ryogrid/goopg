package executor

import (
	"fmt"
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
}

func newIndexOnlyScanOp(p *planner.IndexOnlyScan) *indexOnlyScanOp {
	return &indexOnlyScanOp{plan: p}
}

func (o *indexOnlyScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *indexOnlyScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "IndexOnlyScan requires storage handles"}
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

	// M0118-0001: a SERIALIZABLE index-only scan takes a relation-level SIREAD
	// predicate lock on the heap relation, exactly like the seq-scan and
	// (heap-fetching) index-scan paths. Acquired BEFORE the probe-bound lookups
	// below so it is held even when the scan matches no key — that empty-result
	// case is precisely the phantom the lock must cover (read-write-unique-2/-3
	// probe a non-existent key first, then both INSERT it). Gated to
	// SERIALIZABLE inside ssiRecordRelationRead; temp / matview relations are
	// excluded as PredicateLockingNeededForRelation requires.
	if o.plan.Table == nil || (!o.plan.Table.Temp && !o.plan.Table.IsMatView) {
		ssiRecordRelationRead(ctx, heapRel)
	}

	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := btree.Open(ctx.Pool, idxRel)
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
			hiBytes = appendCompositeUpperPadding(key)
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
			hiBytes = appendCompositeUpperPadding(hiBytes)
		}
	}

	scanFn := func(key []byte, ptr storage.ItemPointer) (bool, error) {
		// Fast path: ALL_VISIBLE → decode from key, zero heap reads.
		if ctx.VM != nil && ctx.VM.AllVisible(heapRel, ptr.Block) {
			row, err := o.decodeRowFromKey(key)
			if err != nil {
				return false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
					Message: fmt.Sprintf("IOS decode: %v", err)}
			}
			o.rows = append(o.rows, row)
			return true, nil
		}

		// Fallback: heap fetch + HOT chain + MVCC.
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		tuple, _, found := followHOTChain(slot.Page(), ptr.Offset, ctx.Snap, ctx.Tx.XID)
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
	return nil
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

// decodeRowFromKey extracts covered column values from a B-tree key.
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
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

// decodeIndexKeyColumn decodes one column from a B-tree key slice and returns
// the Datum plus the number of bytes consumed. Used by the multi-column path.
func decodeIndexKeyColumn(key []byte, col catalog.Column) (Datum, int, error) {
	typeName := col.Type.Name
	// Fixed-width branches must slice `key` to the type's exact byte
	// width before delegating — btree.DecodeInt4 / DecodeInt8 enforce
	// `len(b) == width`, which fails when the multi-column loop passes
	// the still-trailing remainder of a composite key.
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
		return NewTimeDatum(ts), 8, err
	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		if len(key) < 4 {
			return NullDatum, 0, fmt.Errorf("btree: date key truncated, got %d bytes", len(key))
		}
		v, err := btree.DecodeInt4(key[:4])
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewTimeDatum(ts), 4, err
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

	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName):
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
		return NewTimeDatum(ts), nil

	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		v, err := btree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewTimeDatum(ts), nil

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
	var probe []byte
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
		segment, encErr := encodeBTreeKeyForColumn(v, col, ke.Pos())
		if encErr != nil {
			return nil, false, encErr
		}
		probe = append(probe, segment...)
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
	k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.Key.Pos())
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
		k, encE := encodeBTreeKeyForColumn(v, col, o.plan.LowKey.Pos())
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
		k, encE := encodeBTreeKeyForColumn(v, col, o.plan.HighKey.Pos())
		if encE != nil {
			return nil, nil, false, encE
		}
		hi = k
	}
	return lo, hi, true, nil
}
