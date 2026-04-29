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
//     (`writeHeapRow`); Commit closes the txn and returns the
//     remote commit LSN so the caller can advance
//     `confirmed_flush_lsn`.
//
// What's deferred:
//
//   - DELETE / UPDATE row resolution — v0 pgoutput's `D`/`U`
//     don't carry a usable pre-image yet; the apply worker
//     no-ops on them (logs only) until the wire-format
//     extension lands.
//   - TCP transport that produces the message stream — tests
//     drive `ApplyMessage` directly today; the next M0008 loop
//     wires this through libpq START_REPLICATION.
//   - Tablesync (`pg_subscription_rel.srsubstate`).

package executor

import (
	"errors"
	"fmt"
	"strconv"

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

// ApplyMessage handles one decoded pgoutput event. The second
// return value is non-zero only for `C` (commit) messages — it
// carries the remote commit LSN so the caller can advance the
// slot's `confirmed_flush_lsn`. Errors abort the apply and
// rollback the in-progress txn (if any).
func (w *ApplyWorker) ApplyMessage(m *wal.DecodedMessage) (uint64, error) {
	if m == nil {
		return 0, errors.New("applyworker: nil message")
	}
	switch m.Kind {
	case 'B':
		return 0, w.applyBegin(m)
	case 'R':
		return 0, w.applyRelation(m)
	case 'I':
		return 0, w.applyInsert(m)
	case 'D':
		return 0, w.applyDelete(m)
	case 'C':
		return m.CommitLSN, w.applyCommit(m)
	}
	return 0, fmt.Errorf("applyworker: unsupported pgoutput kind %q", m.Kind)
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
	if err := writeHeapRow(ctx, rel, r.local.Columns, row); err != nil {
		return fmt.Errorf("applyworker: writeHeapRow %q: %w", r.local.Name, err)
	}
	return nil
}

func (w *ApplyWorker) applyDelete(_ *wal.DecodedMessage) error {
	// v0 pgoutput emits DELETE with an empty K body (no
	// pre-image). The apply worker needs (rel, block, slot) or
	// the key-tuple bytes to find the row to stamp xmax on;
	// neither is on the wire today. Treat as a no-op until the
	// wire-format extension lands. See
	// docs/design/0008-0004-apply-worker-and-tablesync.md.
	return nil
}

func (w *ApplyWorker) applyCommit(m *wal.DecodedMessage) error {
	if !w.inXact {
		// Idle commit (no Begin observed) — v0 pgoutput emits
		// commit-only sequences only when every change was
		// filtered. Tolerate as a no-op.
		return nil
	}
	if err := w.txnMgr.Commit(w.currentTx); err != nil {
		return fmt.Errorf("applyworker: commit (commit_lsn=%d): %w", m.CommitLSN, err)
	}
	w.inXact = false
	w.currentTx = mvcc.Transaction{}
	return nil
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
			return Datum{Kind: KindBool, Bool: true}, nil
		case "f", "false":
			return Datum{Kind: KindBool, Bool: false}, nil
		}
		return Datum{}, fmt.Errorf("bool parse %q", s)
	}
	// Variable-length text-like fallback — text / varchar /
	// numeric / unknown. Stored as a KindString datum which the
	// encoder roundtrips via the executor codec's varlen frame.
	return Datum{Kind: KindString, String: s}, nil
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
