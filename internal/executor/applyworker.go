// Subscriber-side apply worker for the M0008 logical-decoding
// pipeline. Consumes a stream of decoded pgoutput messages
// (`*wal.DecodedMessage`) and applies each event to local
// storage. One ApplyWorker per active subscription.
//
// See docs/design/0008-0004-apply-worker-and-tablesync.md.
//
// What this file delivers:
//
//   - Per-event apply path: Begin opens a local txn; Relation
//     caches the remote relation descriptor and resolves a local
//     `*catalog.Table` once; Insert encodes the row and writes
//     it via the same storage path the executor's INSERT uses
//     (`writeHeapRow`); Delete scans the relation and stamps
//     xmax on the matching row (M0094-0002); Update finds the
//     old row and replaces with the new (M0094-0002); Commit
//     closes the txn and returns the remote commit LSN.
//
// What's deferred:
//
//   - TCP transport that produces the message stream — tests
//     drive `ApplyMessage` directly today; the next M0008 loop
//     wires this through libpq START_REPLICATION.
//   - Tablesync (`pg_subscription_rel.srsubstate`).

package executor

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// ApplyWorker drives one subscription's apply loop. Construct
// per active subscription; not goroutine-safe. The streaming
// driver feeds it `*wal.DecodedMessage` events one at a time.
type ApplyWorker struct {
	cat    catalog.Catalog
	pool   *storage.Pool
	txnMgr *mvcc.Manager

	// relations caches `R` messages keyed by remote rel OID,
	// resolved to the local catalog.Table once on first
	// reference. UPDATE / DELETE find their target table here.
	relations map[uint32]*applyRel

	// currentTx is the in-progress local transaction opened on
	// `B` and closed on `C` / abort. Apply work happens inside
	// it. When inXact is false, change events outside an open
	// xact are an apply error.
	currentTx mvcc.Transaction
	inXact    bool

	// Tablesync gating. When pubsub and subName are both set,
	// applyInsert skips changes for relations that aren't at
	// state 'r' yet (tablesync still in progress), and
	// applyCommit promotes 's' → 'r' for relations whose
	// recorded sync-end LSN has been crossed by this commit.
	// Mirrors upstream worker.c's process_syncing_tables_for_apply.
	// Both nil/empty preserves the legacy "apply everything"
	// path that existing tests depend on.
	pubsub  *catalog.PubSub
	subName string

	// stat is the pg_stat_subscription handle for this worker.
	// When non-nil, every ApplyMessage records the receipt
	// timestamp + the message's reported end LSN, and every
	// commit advances the worker's received_lsn to the commit
	// LSN. Nil disables observability — the legacy path used by
	// existing tests.
	stat *wal.Subscriber

	// logger receives structured replication-event lines (apply
	// commits, apply errors, tablesync state promotions). nil
	// falls back to slog.Default(); tests pass a discard logger
	// to silence output. See repllog.go for the event vocabulary.
	logger *slog.Logger
}

// applyRel pairs a remote Relation message with its resolved
// local table. Local Table is nil when the remote rel has no
// matching local table — the apply worker rejects subsequent
// changes on it instead of silently dropping data.
type applyRel struct {
	remote *wal.DecodedRelation
	local  *catalog.Table
}

// NewApplyWorker wires an apply worker to local storage handles.
func NewApplyWorker(cat catalog.Catalog, pool *storage.Pool, txnMgr *mvcc.Manager) *ApplyWorker {
	return &ApplyWorker{
		cat:       cat,
		pool:      pool,
		txnMgr:    txnMgr,
		relations: map[uint32]*applyRel{},
	}
}

// SetSubscriptionContext binds the worker to a subscription's
// tablesync state in the supplied PubSub registry. With both
// arguments non-zero, the apply path consults
// pg_subscription_rel before applying each change and updates it
// after each commit; passing nil/"" disables gating (the legacy
// path used by tests that don't model tablesync). Idempotent;
// safe to call before the first message arrives.
func (w *ApplyWorker) SetSubscriptionContext(ps *catalog.PubSub, subName string) {
	w.pubsub = ps
	w.subName = subName
}

// SetStatHandle attaches a pg_stat_subscription handle to this
// worker. When set, every ApplyMessage records receipt
// timestamp + reported end LSN via MarkMessage, and every
// commit advances received_lsn via AdvanceReceivedLSN. Nil
// disables observability. Caller owns the handle's lifecycle
// (Register before, Unregister after the apply loop). Idempotent.
func (w *ApplyWorker) SetStatHandle(sub *wal.Subscriber) {
	w.stat = sub
}

// SetLogger attaches a structured replication-event logger.
// nil falls back to slog.Default(). Idempotent.
func (w *ApplyWorker) SetLogger(l *slog.Logger) {
	w.logger = l
}

// log returns the configured logger or slog.Default() when
// none was set. Centralised so call sites don't repeat the
// nil-check.
func (w *ApplyWorker) log() *slog.Logger {
	if w.logger != nil {
		return w.logger
	}
	return slog.Default()
}

// ApplyMessage handles one decoded pgoutput event. The second
// return value is non-zero only for `C` (commit) messages — it
// carries the remote commit LSN so the caller can advance the
// slot's `confirmed_flush_lsn`. Errors abort the apply and
// rollback the in-progress txn (if any).
func (w *ApplyWorker) ApplyMessage(m *wal.DecodedMessage) (uint64, error) {
	if m == nil {
		return 0, errors.New("applyworker: nil message")
	}
	if w.stat != nil {
		// Record this frame's arrival timestamp. EndLSN comes from
		// the publisher's reported end-of-WAL on B/C messages
		// (zero on R/I/D, which leaves latest_end_lsn untouched).
		w.stat.MarkMessage(time.Now(), m.EndLSN)
	}
	var (
		commitLSN uint64
		err       error
	)
	switch m.Kind {
	case 'B':
		err = w.applyBegin(m)
	case 'R':
		err = w.applyRelation(m)
	case 'I':
		err = w.applyInsert(m)
	case 'D':
		err = w.applyDelete(m)
	case 'U':
		err = w.applyUpdate(m)
	case 'C':
		commitLSN = m.CommitLSN
		err = w.applyCommit(m)
	default:
		err = fmt.Errorf("applyworker: unsupported pgoutput kind %q", m.Kind)
	}
	if err != nil {
		w.log().Error("logical apply: per-message failure",
			"event", wal.EventApplyError,
			"sub", w.subName, "kind", string(m.Kind),
			"rel_oid", m.RelOID, "lsn", m.EndLSN, "err", err)
	}
	return commitLSN, err
}

func (w *ApplyWorker) applyBegin(m *wal.DecodedMessage) error {
	if w.inXact {
		// Defensive: a B inside an open xact means the previous
		// transaction was never finalised. Roll it back to keep
		// state consistent before opening the new one.
		_ = w.txnMgr.Rollback(w.currentTx)
		w.inXact = false
	}
	tx, err := w.txnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		return fmt.Errorf("applyworker: begin txn (xid=%d): %w", m.XID, err)
	}
	w.currentTx = tx
	w.inXact = true
	return nil
}

func (w *ApplyWorker) applyRelation(m *wal.DecodedMessage) error {
	if m.Relation == nil {
		return errors.New("applyworker: R message missing relation body")
	}
	rel := m.Relation
	// Resolve the local table by (schema, name). Schema may be
	// empty in v0's pgoutput emit path; treat empty as "default
	// schema" (LookupTable handles unqualified lookup itself).
	local, _ := w.cat.LookupTable(parser.ObjectName{Schema: rel.Schema, Name: rel.Name})
	w.relations[rel.OID] = &applyRel{remote: rel, local: local}
	return nil
}

func (w *ApplyWorker) applyInsert(m *wal.DecodedMessage) error {
	if !w.inXact {
		return errors.New("applyworker: INSERT outside transaction")
	}
	r, ok := w.relations[m.RelOID]
	if !ok {
		return fmt.Errorf("applyworker: INSERT for unknown rel_oid %d (no R message seen)", m.RelOID)
	}
	if r.local == nil {
		return fmt.Errorf("applyworker: INSERT for relation %s.%s has no local table",
			r.remote.Schema, r.remote.Name)
	}
	// Tablesync gate: while the relation is still at state 'i'
	// or 'd' (initial COPY in progress) or 's' (snapshot drained
	// but apply hasn't crossed the sync-end LSN), the row is
	// either already coming through tablesync or we'd be
	// double-applying it. Skip silently — the row will reach
	// the local table via the tablesync worker. State 'r'
	// (ready) is the only state where streaming applies.
	if w.pubsub != nil && w.subName != "" {
		// Resolve the local rel's OID from the catalog so the
		// gate keys off the same identifier the tablesync
		// transport used to seed the row.
		localOID := w.cat.RelFileNode(r.local).RelOid
		if sr, tracked := w.pubsub.LookupSubscriptionRel(w.subName, localOID); tracked &&
			sr.State != catalog.SubRelStateReady {
			return nil
		}
	}
	row, err := decodePgoutputTupleAsRow(r.remote.Columns, r.local.Columns, m.NewTuple)
	if err != nil {
		return fmt.Errorf("applyworker: decode insert tuple for %q: %w", r.local.Name, err)
	}

	// writeHeapRow expects a Context carrying Pool / Tx. Build
	// a minimal one — the apply worker doesn't have a session
	// or planner state.
	ctx := NewContext()
	ctx.Pool = w.pool
	ctx.Catalog = w.cat
	ctx.TxnMgr = w.txnMgr
	ctx.Tx = w.currentTx

	rel := w.cat.RelFileNode(r.local)
	ptr, err := writeHeapRowReturning(ctx, rel, r.local.Columns, row)
	if err != nil {
		return fmt.Errorf("applyworker: writeHeapRow %q: %w", r.local.Name, err)
	}
	// Maintain unique/primary-key indexes so fresh-session queries
	// using equality predicates on indexed columns find the row.
	// Without this, the dispatcher's IndexScan returns 0 rows even
	// though SeqScan still sees the tuple — the M0103-0007 rung-1
	// fresh-session visibility gap surfaced in
	// `TestPubSubClusterSmokePGToGoopgFreshSessionVisibility`.
	maintainUniqueIndexesForInsert(ctx, r.local, r.local.Columns, row, ptr)
	return nil
}

func (w *ApplyWorker) applyDelete(m *wal.DecodedMessage) error {
	if !w.inXact {
		return errors.New("applyworker: DELETE outside transaction")
	}
	r, ok := w.relations[m.RelOID]
	if !ok {
		return fmt.Errorf("applyworker: DELETE for unknown rel_oid %d (no R message seen)", m.RelOID)
	}
	if r.local == nil {
		return fmt.Errorf("applyworker: DELETE for relation %s.%s has no local table",
			r.remote.Schema, r.remote.Name)
	}
	// Empty old-tuple means we have no key to find the row — skip.
	if len(m.OldTuple) == 0 {
		return nil
	}
	keyRow, err := decodePgoutputTupleAsRow(r.remote.Columns, r.local.Columns, m.OldTuple)
	if err != nil {
		return fmt.Errorf("applyworker: decode delete old-tuple for %q: %w", r.local.Name, err)
	}
	ctx := w.applyContext()
	rel := w.cat.RelFileNode(r.local)
	return applyDeleteByKey(ctx, rel, r.local.Columns, keyRow)
}

func (w *ApplyWorker) applyUpdate(m *wal.DecodedMessage) error {
	if !w.inXact {
		return errors.New("applyworker: UPDATE outside transaction")
	}
	r, ok := w.relations[m.RelOID]
	if !ok {
		return fmt.Errorf("applyworker: UPDATE for unknown rel_oid %d (no R message seen)", m.RelOID)
	}
	if r.local == nil {
		return fmt.Errorf("applyworker: UPDATE for relation %s.%s has no local table",
			r.remote.Schema, r.remote.Name)
	}
	if len(m.OldTuple) == 0 {
		return nil
	}
	oldKeyRow, err := decodePgoutputTupleAsRow(r.remote.Columns, r.local.Columns, m.OldTuple)
	if err != nil {
		return fmt.Errorf("applyworker: decode update old-tuple for %q: %w", r.local.Name, err)
	}
	newRow, err := decodePgoutputTupleAsRow(r.remote.Columns, r.local.Columns, m.NewTuple)
	if err != nil {
		return fmt.Errorf("applyworker: decode update new-tuple for %q: %w", r.local.Name, err)
	}
	ctx := w.applyContext()
	rel := w.cat.RelFileNode(r.local)
	return applyUpdateByKey(ctx, rel, r.local, r.local.Columns, oldKeyRow, newRow)
}

// applyContext builds a minimal executor Context for apply-worker
// storage operations. Pool, Catalog, TxnMgr, Tx, and Snap are set.
// The XID is materialised eagerly so xmax stamps are never written
// as InvalidTransactionID (0) — which would be invisible to future
// readers because Xmax=0 means "no deleter".
func (w *ApplyWorker) applyContext() *Context {
	ctx := NewContext()
	ctx.Pool = w.pool
	ctx.Catalog = w.cat
	ctx.TxnMgr = w.txnMgr
	ctx.Tx = w.currentTx
	if err := ctx.MaterializeWriterXID(); err == nil && ctx.Tx.XID != w.currentTx.XID {
		// Sync the newly materialized XID back to the apply worker's
		// transaction so subsequent Commit calls use the right XID state.
		w.currentTx.XID = ctx.Tx.XID
	}
	snap, _ := w.txnMgr.SnapshotFor(w.currentTx)
	ctx.Snap = snap
	return ctx
}

// applyDeleteByKey scans rel for a visible tuple whose decoded columns
// match keyRow (equality on all non-null key columns) and stamps xmax
// on each match. Designed for the apply worker's DELETE path.
func applyDeleteByKey(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, keyRow Row) error {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return err
		}
		type match struct{ slot uint16 }
		var matches []match
		scanRow := make(Row, len(cols))
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tup, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
				continue
			}
			if err := DecodeRowInto(scanRow, cols, tup.Data); err != nil {
				continue
			}
			if rowMatchesKey(scanRow, keyRow) {
				matches = append(matches, match{slot: slot})
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		for _, m := range matches {
			sw, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
			if err != nil {
				return err
			}
			sw.Lock()
			oldTup, _ := storage.PageGetHeapTuple(sw.Page(), m.slot)
			var oldBytes []byte
			oldBytes, _ = oldTup.MarshalBinary()
			if err := storage.PageSetHeapTupleXmax(sw.Page(), m.slot, ctx.Tx.XID); err == nil {
				_ = markHeapDeleteDirty(ctx.Pool, sw, rel, blk, m.slot, ctx.Tx.XID, oldBytes)
			}
			sw.Unlock()
			ctx.Pool.Unpin(sw)
		}
	}
	return nil
}

// applyUpdateByKey finds the row matching oldKeyRow in rel, deletes it,
// then inserts newRow — implementing a logical UPDATE for the apply worker.
func applyUpdateByKey(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, oldKeyRow, newRow Row) error {
	if err := applyDeleteByKey(ctx, rel, cols, oldKeyRow); err != nil {
		return err
	}
	ptr, err := writeHeapRowReturning(ctx, rel, cols, newRow)
	if err != nil {
		return err
	}
	// Re-add to unique/primary-key indexes so post-UPDATE
	// equality probes find the new row (matches applyInsert).
	if tbl != nil {
		maintainUniqueIndexesForInsert(ctx, tbl, cols, newRow, ptr)
	}
	return nil
}

// rowMatchesKey returns true when every non-null datum in keyRow equals
// the corresponding datum in row. Null datums in keyRow are skipped
// (treated as "don't care").
func rowMatchesKey(row, keyRow Row) bool {
	for i, k := range keyRow {
		if i >= len(row) {
			return false
		}
		if k.IsNull() {
			continue
		}
		if !applyDatumEqual(row[i], k) {
			return false
		}
	}
	return true
}

// applyDatumEqual compares two Datums for equality by kind and value.
// Used only by the apply worker's key-matching scan.
func applyDatumEqual(a, b Datum) bool {
	if a.Kind != b.Kind {
		if (a.Kind == KindString || a.Kind == KindStringArena) &&
			(b.Kind == KindString || b.Kind == KindStringArena) {
			return a.StringValue() == b.StringValue()
		}
		return false
	}
	switch a.Kind {
	case KindNull:
		return true
	case KindInt:
		return a.Int == b.Int
	case KindBool:
		return a.BoolValue() == b.BoolValue()
	case KindString, KindStringArena:
		return a.StringValue() == b.StringValue()
	}
	return false
}

func (w *ApplyWorker) applyCommit(m *wal.DecodedMessage) error {
	if w.inXact {
		if err := w.txnMgr.Commit(w.currentTx); err != nil {
			return fmt.Errorf("applyworker: commit (commit_lsn=%d): %w", m.CommitLSN, err)
		}
		w.inXact = false
		w.currentTx = mvcc.Transaction{}
	}
	// (Idle commit with no Begin observed — v0 pgoutput can emit
	// commit-only sequences when every change was filtered.
	// Falls through to the tablesync promotion check below.)
	w.promoteSyncedRels(m.CommitLSN)
	if w.stat != nil {
		w.stat.AdvanceReceivedLSN(m.CommitLSN)
	}
	w.log().Info("logical apply: commit",
		"event", wal.EventApplyCommit,
		"sub", w.subName, "xid", m.XID, "lsn", m.CommitLSN)
	return nil
}

// promoteSyncedRels advances every subscription_rel in state 's'
// whose recorded sync-end LSN has been crossed by this commit
// to state 'r'. Mirrors the apply-worker side of upstream
// worker.c's process_syncing_tables_for_apply: once the apply
// stream has replayed past the snapshot LSN that tablesync
// captured, it is safe to merge the relation into the streaming
// path without double-applying. v0's tablesync transport records
// LSN=0 (no per-snapshot handoff yet), so the first observed
// commit promotes any 's' rel — conservative but correct given
// the COPY happens on the same publisher/slot timeline as the
// streaming apply.
func (w *ApplyWorker) promoteSyncedRels(commitLSN uint64) {
	if w.pubsub == nil || w.subName == "" {
		return
	}
	for _, sr := range w.pubsub.SubscriptionRels(w.subName) {
		if sr.State != catalog.SubRelStateSyncDone {
			continue
		}
		if commitLSN < sr.LSN {
			continue
		}
		if _, err := w.pubsub.AdvanceSubscriptionRel(w.subName, sr.RelOID,
			catalog.SubRelStateReady, commitLSN); err == nil {
			w.log().Info("logical apply: tablesync rel promoted",
				"event", wal.EventTablesyncStateChange,
				"sub", w.subName, "rel_oid", sr.RelOID,
				"from", catalog.SubRelStateSyncDone,
				"to", catalog.SubRelStateReady,
				"lsn", commitLSN)
		}
	}
}

// decodePgoutputTupleAsRow parses each pgoutput text-format
// column value to a Datum compatible with the local table's
// column type. Status 'n' becomes NullDatum; status 't' is
// parsed per type ('u' / unchanged TOAST is rejected — v0's
// encoder doesn't emit it).
func decodePgoutputTupleAsRow(remoteCols []wal.DecodedAttr, localCols []catalog.Column, tup []wal.DecodedColumn) (Row, error) {
	if len(tup) != len(remoteCols) {
		return nil, fmt.Errorf("tuple has %d cols, R message described %d", len(tup), len(remoteCols))
	}
	if len(localCols) < len(remoteCols) {
		return nil, fmt.Errorf("local table has %d cols, remote sent %d", len(localCols), len(remoteCols))
	}
	row := make(Row, len(localCols))
	for i := range row {
		row[i] = NullDatum
	}
	for i, col := range tup {
		local := localCols[i]
		switch col.Status {
		case 'n':
			row[i] = NullDatum
		case 't':
			d, err := parsePgoutputText(local.Type, col.Bytes)
			if err != nil {
				return nil, fmt.Errorf("col %q: %w", local.Name, err)
			}
			row[i] = d
		case 'u':
			return nil, fmt.Errorf("col %q: 'u' (unchanged TOAST) status not supported", local.Name)
		default:
			return nil, fmt.Errorf("col %q: unknown status %q", local.Name, col.Status)
		}
	}
	return row, nil
}

// parsePgoutputText converts upstream's canonical text format
// for one column value back to a Datum. Mirrors the inverse of
// pgoDecodeValue in the encoder — same v0 type set.
func parsePgoutputText(t catalog.Type, data []byte) (Datum, error) {
	s := string(data)
	switch t.Name {
	case "int4", "integer", "int":
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return Datum{}, fmt.Errorf("int4 parse %q: %w", s, err)
		}
		return Datum{Kind: KindInt, Int: v}, nil
	case "int8", "bigint":
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return Datum{}, fmt.Errorf("int8 parse %q: %w", s, err)
		}
		return Datum{Kind: KindInt, Int: v}, nil
	case "bool", "boolean":
		switch s {
		case "t", "true":
			return NewBoolDatum(true), nil
		case "f", "false":
			return NewBoolDatum(false), nil
		}
		return Datum{}, fmt.Errorf("bool parse %q", s)
	}
	// Variable-length text-like fallback — text / varchar /
	// numeric / unknown. Stored as a KindString datum which the
	// encoder roundtrips via the executor codec's varlen frame.
	return NewStringDatum(s), nil
}

// SafeRollback rolls back any in-progress txn. Idempotent.
// Driver code calls this from a defer so a Run loop that errors
// out doesn't leak an open xid.
func (w *ApplyWorker) SafeRollback() {
	if w.inXact {
		_ = w.txnMgr.Rollback(w.currentTx)
		w.inXact = false
		w.currentTx = mvcc.Transaction{}
	}
}
