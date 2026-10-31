package postmaster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/replication"
)

// RefreshSubscriptionOnPublisher runs the DATA half of
// AlterSubscription_refresh (subscriptioncmds.c:820-880) for the
// subscription's already-committed publication list:
//
//  1. fetch_table_list (:2103) over the publisher — the merged list's
//     published tables;
//  2. resolve each against the local catalog: a published table missing
//     on the subscriber is the "table %s does not exist on the
//     subscriber" 42P01 arm (:2176-2181) — PG checks ALL names before
//     touching pg_subscription_rel, so this aborts with no row changes;
//  3. drop the pg_subscription_rel rows of tables that left the list
//     (RemoveSubscriptionRel; PG TRUNCATEs those tables too —
//     ledgered), and seed 'i' rows for tables that entered it
//     (create_subscription_rel);
//  4. copy_data = false advances the new rows to READY (PG inserts 'r'
//     straight away; goopg steps i→s→r because the registry transition
//     table requires the SyncDone waypoint);
//  5. copy_data = true drives RunTableSyncManager: one fresh publisher
//     connection + one COPY <rel> TO STDOUT → CopyFromExecutor exchange
//     per non-'r' row (the tablesync workers' job);
//  6. kills the subscription's apply worker (StopWorker) so the
//     executor's post-write launcher kick relaunches it on the new
//     list (PG's kill-in-refresh + relaunch-after-commit split);
//     the relaunched worker streams with the new publication list —
//     goopg restarts eagerly inside the command; PG lets the existing
//     worker finish its current stream and swaps the list at its next
//     START_REPLICATION (documented deviation).
//
// pubs is the post-merge list; copyData is WITH (copy_data). It returns
// a ready *ExecError on PG's error arms: 08006 connect, XX000
// fetch/query refusal, 42P01 missing subscriber table.
func (s *Server) RefreshSubscriptionOnPublisher(subName, conninfo string, dbOid uint32, pubs []string, copyData bool) error {
	if s.cfg.PubSub == nil {
		return &executor.ExecError{Code: "XX000",
			Message: fmt.Sprintf("subscription %q refresh: PubSub registry unconfigured", subName)}
	}
	pubTables, err := s.applyLauncher.FetchPublicationTables(conninfo, pubs)
	if err != nil {
		var rce *replication.RemoteCommandError
		if errors.As(err, &rce) {
			return &executor.ExecError{Code: "XX000",
				Message: fmt.Sprintf("could not receive list of tables from the publisher: %s", rce.Error())}
		}
		return &executor.ExecError{Code: "08006",
			Message: fmt.Sprintf("subscription %q could not connect to the publisher: %v", subName, errors.Unwrap(err))}
	}

	// Resolve the fetched list locally (PG range-var-resolves every name
	// with MissingOk and reports misses together). The search-path
	// catalog wrapper matches the executor's FROM-table resolution
	// (schema-qualified names resolve exactly; "public." goes through
	// the search_path arm, which finds public).
	cat := catalog.WithSearchPath(s.cfg.Catalog, nil)
	cat.DBOid = catalog.NamespaceDBOid(dbOid)
	rels := map[uint32]*refreshRel{}
	seenName := map[string]bool{}
	fetched := make([]uint32, 0, len(pubTables)) // entering rows, fetch order
	for _, pt := range pubTables {
		schema := pt.Schema
		if schema == "public" {
			schema = "" // search-path arm resolves public, as the executor resolves FROM names
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: schema, Name: pt.Name}, dbOid)
		if !ok {
			qual := pt.Name
			if pt.Schema != "" {
				qual = pt.Schema + "." + pt.Name
			}
			return &executor.ExecError{Code: "42P01",
				Message: fmt.Sprintf("table %s does not exist on the subscriber", qual)}
		}
		if seenName[strings.ToLower(tbl.Schema+"."+tbl.Name)] {
			continue // the fetch's DISTINCT can still yield one local rel twice
		}
		seenName[strings.ToLower(tbl.Schema+"."+tbl.Name)] = true
		qual := tbl.Name
		if tbl.Schema != "" {
			qual = quoteIdent(tbl.Schema) + "." + quoteIdent(tbl.Name)
		}
		rels[tbl.OID] = &refreshRel{tbl: tbl, qual: qual, dbOid: dbOid}
		if _, exists := s.cfg.PubSub.LookupSubscriptionRel(subName, tbl.OID); !exists {
			fetched = append(fetched, tbl.OID)
		}
	}

	// RemoveSubscriptionRel for rows whose table left the list. PG
	// drops every row whose rel is not in the NEW table list,
	// TRUNCATEing the rel first (TRUNCATE half ledgered).
	for _, sr := range s.cfg.PubSub.SubscriptionRels(subName) {
		if _, in := rels[sr.RelOID]; !in {
			_ = s.cfg.PubSub.DropSubscriptionRel(subName, sr.RelOID)
		}
	}

	// create_subscription_rel: seed the entering rows.
	for _, oid := range fetched {
		if _, err := s.cfg.PubSub.AddSubscriptionRel(subName, oid); err != nil &&
			!errors.Is(err, catalog.ErrSubscriptionRelExists) {
			return &executor.ExecError{Code: "XX000",
				Message: fmt.Sprintf("subscription %q refresh: seed pg_subscription_rel: %v", subName, err)}
		}
		if !copyData {
			// WITH (copy_data = false): the table is marked READY with
			// no data copy (PG inserts 'r' directly; the registry's
			// transition table needs the 's' waypoint on the way).
			if _, err := s.cfg.PubSub.AdvanceSubscriptionRel(subName, oid, catalog.SubRelStateSyncDone, 0); err != nil {
				return &executor.ExecError{Code: "XX000",
					Message: fmt.Sprintf("subscription %q refresh: mark pg_subscription_rel ready: %v", subName, err)}
			}
			if _, err := s.cfg.PubSub.AdvanceSubscriptionRel(subName, oid, catalog.SubRelStateReady, 0); err != nil {
				return &executor.ExecError{Code: "XX000",
					Message: fmt.Sprintf("subscription %q refresh: mark pg_subscription_rel ready: %v", subName, err)}
			}
		}
	}

	// Initial synchronization: one tablesync exchange per non-'r' row
	// (upstream's tablesync workers; the command reports the workers'
	// outcomes, so per-rel failures surface below).
	results, mgrErr := replication.RunTableSyncManager(context.Background(), replication.TableSyncManagerConfig{
		PubSub:  s.cfg.PubSub,
		SubName: subName,
		ResolveRel: func(relOID uint32) (string, bool) {
			rr, ok := rels[relOID]
			if !ok {
				return "", false
			}
			return rr.qual, true
		},
		OpenConn: func(ctx context.Context, relOID uint32) (*replication.ConnPair, error) {
			pc, err := s.applyLauncher.OpenPublisherConn(conninfo)
			if err != nil {
				return nil, err
			}
			return &replication.ConnPair{Reader: pc.Reader, Writer: pc.Writer, Close: pc.Close}, nil
		},
		OpenWriter: s.refreshOpenWriter(rels),
	})
	var syncErrs []string
	if mgrErr != nil {
		syncErrs = append(syncErrs, mgrErr.Error())
	}
	for _, r := range results {
		if r.Err != nil {
			syncErrs = append(syncErrs, fmt.Sprintf("%s: %v", r.Relname, r.Err))
		}
	}
	// Sync-end promotion: a rel whose COPY completed sits at 's' with
	// the recorded sync LSN 0 (the v0 transport carries no real LSN),
	// and the apply worker's 's' gate would swallow the first streamed
	// commit and spend it on the promotion — losing that commit's
	// changes. PG promotes at the restart point, so do the same here:
	// the sweep completed, the worker restarts at the slot's resume
	// point, and every change after it streams onto a 'r' rel.
	for _, r := range results {
		if r.Err != nil || r.FinalState != catalog.SubRelStateSyncDone {
			continue
		}
		if _, perr := s.cfg.PubSub.AdvanceSubscriptionRel(subName, r.RelOID, catalog.SubRelStateReady, 1); perr == nil {
			continue
		}
	}
	// Worker kill: the Publications snapshot the running worker
	// dialed with is stale (and the slot must go ACTIVE→free before
	// the relaunched worker re-acquires it). PG kills the worker
	// inside AlterSubscription_refresh and relaunches it from the
	// launcher after the catalog update commits; the executor's
	// post-write launcher kick is goopg's twin of that relaunch, so
	// only the kill happens here (documented eager-restart
	// deviation). The kill runs even on sync failure — rows left at
	// 'i'/'d' are picked up by the next refresh, as PG's failed
	// tablesync workers leave them, and a stopped worker relaunches
	// on the launcher's poll even when the statement aborts.
	s.applyLauncher.StopWorker(subName)
	if len(syncErrs) > 0 {
		return &executor.ExecError{Code: "XX000",
			Message: fmt.Sprintf("subscription %q refresh: table copy failed: %s", subName, strings.Join(syncErrs, "; "))}
	}
	return nil
}

// refreshOpenWriter builds RunTableSyncManager's per-rel writer: a
// per-table transaction (the auto-assign ProcArray slot — refresh runs
// off-connection, so there is no connection slot to reuse, and PG's
// tablesync workers commit one transaction per table) plus a
// CopyFromExecutor bound to the local rel with every column in order
// (the publisher's COPY stream is the full table).
func (s *Server) refreshOpenWriter(rels map[uint32]*refreshRel) func(ctx context.Context, relOID uint32) (*replication.WriterPair, error) {
	return func(ctx context.Context, relOID uint32) (*replication.WriterPair, error) {
		rr, ok := rels[relOID]
		if !ok {
			return nil, fmt.Errorf("subscription refresh: relation %d is not a fetched published table", relOID)
		}
		tbl := rr.tbl
		tx, err := s.cfg.TxnMgr.Begin(transam.IsolationReadCommitted)
		if err != nil {
			return nil, err
		}
		snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
		if err != nil {
			_ = s.cfg.TxnMgr.Rollback(tx)
			return nil, err
		}
		ectx := executor.NewContext()
		ectx.Ctx = ctx
		ectx.Pool = s.cfg.Pool
		ectx.Catalog = s.cfg.Catalog
		ectx.TxnMgr = s.cfg.TxnMgr
		ectx.MultiXact = s.cfg.MultiXact
		ectx.FSM = s.cfg.FSM
		ectx.VM = s.cfg.VM
		ectx.WAL = s.cfg.WAL
		ectx.Tx = tx
		ectx.Snap = snap
		ectx.CurrentDatabaseOid = rr.dbOid
		colIdx := make([]int, len(tbl.Columns))
		for i := range colIdx {
			colIdx[i] = i
		}
		from, err := executor.NewCopyFromExecutor(ectx, &optimizer.Copy{
			Direction:   optimizer.CopyFrom,
			Table:       tbl,
			ColumnIndex: colIdx,
			Endpoint:    optimizer.CopyEndpointStdin,
		})
		if err != nil {
			_ = s.cfg.TxnMgr.Rollback(tx)
			return nil, err
		}
		return &replication.WriterPair{
			Writer: from,
			Close: func() error {
				ferr := from.Finish()
				if ferr != nil {
					_ = s.cfg.TxnMgr.Rollback(tx)
					return ferr
				}
				return ectx.CommitTransaction(tx)
			},
		}, nil
	}
}

// refreshRel is one resolved published relation: the local table,
// the publisher-side qualified COPY name, and the database it lives in.
type refreshRel struct {
	tbl   *catalog.Table
	qual  string // quote_qualified_identifier, the publisher's COPY target
	dbOid uint32
}

// quoteIdent mirrors quote_identifier for the publisher-side COPY name.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
