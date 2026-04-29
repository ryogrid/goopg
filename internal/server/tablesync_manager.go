// Per-subscription tablesync manager. Walks
// pg_subscription_rel for one subscription, picks every row not
// yet at state 'r', and drives one initial-COPY exchange per rel
// through RunTableSync. The streaming apply path (LogicalReceiver
// → ApplyWorker) advances 's → r' on its own once a commit
// crosses the recorded sync-end LSN, so this manager doesn't
// touch state 'r' rows.
//
// See docs/design/0008-0004-apply-worker-and-tablesync.md.
//
// Design choices:
//
//   - Connection setup is injected as a callback (OpenConn) rather
//     than baked in. The manager doesn't dial; it only orchestrates
//     the per-rel exchange. This keeps the manager testable
//     (in-memory frame buffers) and lets a future LogicalReceiver
//     layer choose its own dial strategy (a fresh non-replication
//     query connection per rel, like upstream's tablesync worker).
//   - The LineWriter (typically wrapping *executor.CopyFromExecutor)
//     is also injected per rel. The manager doesn't know how to
//     turn a relOID into a heap-write target; the caller does.
//   - One rel that fails its sync does NOT abort the manager —
//     every rel gets its own TableSyncResult, with the per-rel
//     error captured so the caller can decide how aggressively to
//     retry. This mirrors upstream's behaviour where one stuck
//     table doesn't block the rest.

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
)

// ConnPair bundles the framed I/O for one publisher connection
// plus a closer. RunTableSyncManager takes ownership of every
// returned ConnPair and closes it before moving on to the next
// rel.
type ConnPair struct {
	Reader *protocol.FrameReader
	Writer *protocol.FrameWriter
	Close  func() error
}

// WriterPair bundles a per-rel LineWriter plus a closer. The
// closer is called even if RunTableSync errors so the underlying
// transaction is rolled back / committed appropriately.
type WriterPair struct {
	Writer LineWriter
	Close  func() error
}

// TableSyncManagerConfig parameterises one manager run.
type TableSyncManagerConfig struct {
	// PubSub is the catalog registry holding the subscription's
	// tablesync rows.
	PubSub *catalog.PubSub

	// SubName picks which subscription's rels to walk.
	SubName string

	// ResolveRel turns a relOID into the qualified "schema.name"
	// the publisher will see on its `COPY <name> TO STDOUT`
	// query. Returning ok=false for a known rel causes the
	// manager to record a per-rel error and skip the rel; the
	// state machine is left wherever it was.
	ResolveRel func(relOID uint32) (qualifiedName string, ok bool)

	// OpenConn establishes one fresh publisher query connection
	// (already authenticated and at ReadyForQuery). Called once
	// per non-`r` rel. The manager guarantees Close is called
	// before moving on.
	OpenConn func(ctx context.Context, relOID uint32) (*ConnPair, error)

	// OpenWriter builds a per-rel apply target. Typically wraps
	// a *executor.CopyFromExecutor opened against the local
	// table identified by relOID. The manager guarantees Close
	// is called before moving on.
	OpenWriter func(ctx context.Context, relOID uint32) (*WriterPair, error)
}

// TableSyncResult records what happened for one (subscription,
// rel) pair during a manager run. InitialState is the row's
// state before this attempt; FinalState is whatever the row is
// at after, by reading back from PubSub. Rows is the count
// returned by RunTableSync. Err is non-nil when this rel's sync
// did not succeed; the manager continues to the next rel
// regardless.
type TableSyncResult struct {
	RelOID       uint32
	Relname      string
	InitialState string
	FinalState   string
	Rows         int64
	Err          error
}

// RunTableSyncManager drives one full sweep of unsynced rels.
// Returns one TableSyncResult per non-`r` row that was visited
// (whether or not it succeeded). The function-level error is
// non-nil only for systemic failures (missing config, missing
// subscription); per-rel errors land in result.Err.
func RunTableSyncManager(ctx context.Context, cfg TableSyncManagerConfig) ([]TableSyncResult, error) {
	if cfg.PubSub == nil {
		return nil, errors.New("tablesync_manager: PubSub is nil")
	}
	if cfg.SubName == "" {
		return nil, errors.New("tablesync_manager: SubName is empty")
	}
	if cfg.ResolveRel == nil || cfg.OpenConn == nil || cfg.OpenWriter == nil {
		return nil, errors.New("tablesync_manager: ResolveRel/OpenConn/OpenWriter are required")
	}

	rels := cfg.PubSub.SubscriptionRels(cfg.SubName)
	var results []TableSyncResult
	for _, sr := range rels {
		if sr.State == catalog.SubRelStateReady {
			continue
		}
		if err := ctx.Err(); err != nil {
			// Cancellation between rels: stop walking and
			// surface the context error on the next visited
			// rel. Already-collected results are returned.
			return results, err
		}
		results = append(results, syncOneRel(ctx, cfg, sr.RelOID, sr.State))
	}
	return results, nil
}

// syncOneRel orchestrates the per-rel sequence: resolve the
// publisher relname, open a connection + writer, drive
// RunTableSync, close everything in order, and read the final
// state back from PubSub. Errors at any stage are captured on
// the returned TableSyncResult.
func syncOneRel(ctx context.Context, cfg TableSyncManagerConfig, relOID uint32, initial string) TableSyncResult {
	res := TableSyncResult{RelOID: relOID, InitialState: initial, FinalState: initial}

	relname, ok := cfg.ResolveRel(relOID)
	if !ok {
		res.Err = fmt.Errorf("tablesync_manager: cannot resolve relname for OID %d", relOID)
		return res
	}
	res.Relname = relname

	conn, err := cfg.OpenConn(ctx, relOID)
	if err != nil {
		res.Err = fmt.Errorf("tablesync_manager: open conn for rel %d (%s): %w", relOID, relname, err)
		return res
	}
	defer func() {
		if conn.Close != nil {
			_ = conn.Close()
		}
	}()

	writer, err := cfg.OpenWriter(ctx, relOID)
	if err != nil {
		res.Err = fmt.Errorf("tablesync_manager: open writer for rel %d (%s): %w", relOID, relname, err)
		return res
	}
	defer func() {
		if writer.Close != nil {
			_ = writer.Close()
		}
	}()

	rows, err := RunTableSync(TableSyncConfig{
		PubSub:  cfg.PubSub,
		SubName: cfg.SubName,
		RelOID:  relOID,
		Relname: relname,
		From:    writer.Writer,
		Reader:  conn.Reader,
		Writer:  conn.Writer,
	})
	res.Rows = rows
	if err != nil {
		res.Err = err
	}

	if got, ok := cfg.PubSub.LookupSubscriptionRel(cfg.SubName, relOID); ok {
		res.FinalState = got.State
	}
	return res
}
