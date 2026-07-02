package executor

// operators_cluster.go — CLUSTER statement executor (M0095-0008).
//
// CLUSTER is a no-op in goopg v0: no physical index-ordered heap rewrite
// is performed. The executor validates the target table exists (so that
// CLUSTER nonexistent returns an error as PostgreSQL would) and then
// returns EOF immediately.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)


// clusterOp implements the CLUSTER statement.
// CLUSTER without a target always succeeds.
// CLUSTER tablename errors if the table does not exist.
type clusterOp struct {
	stmt *parser.ClusterStmt
	ctx  *Context
	done bool
}

func newClusterOp(s *parser.ClusterStmt) *clusterOp { return &clusterOp{stmt: s} }

func (o *clusterOp) Schema() planner.Schema { return nil }

func (o *clusterOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *clusterOp) Close() error { return nil }

// Next runs CLUSTER as a one-shot side effect.
func (o *clusterOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	if o.stmt.Target == nil {
		// CLUSTER with no table — no-op, always succeed.
		return nil, EOF
	}

	// CLUSTER tablename — verify the table exists.
	// Try schema-qualified first (e.g. "public.test1"), then bare name
	// (e.g. "test1") for user tables created without an explicit schema.
	if o.ctx != nil && o.ctx.Catalog != nil {
		tbl, ok := o.ctx.Catalog.LookupTable(*o.stmt.Target)
		if !ok && o.stmt.Target.Schema != "" {
			tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Name: o.stmt.Target.Name})
		}
		if !ok {
			return nil, &ExecError{
				Code:    "42P01",
				Message: fmt.Sprintf("relation %q does not exist", o.stmt.Target.Name),
			}
		}
		// CLUSTER takes an AccessExclusiveLock on the table (cluster.c
		// cluster_rel → LockRelationOid(AccessExclusiveLock)). That conflicts with
		// every other lock mode, so CLUSTER blocks behind a concurrent
		// LOCK ... IN SHARE UPDATE EXCLUSIVE MODE until the holder commits, then
		// proceeds. acquireRelLockMaybeTransient holds the lock to commit inside an
		// explicit transaction and acquires+releases it transiently in autocommit
		// (so the wait still happens during acquisition). M0118-0008
		// (cluster-conflict).
		if tbl != nil {
			rel := o.ctx.Catalog.RelFileNode(tbl)
			if err := o.ctx.acquireRelLockMaybeTransient(rel, lockmgr.AccessExclusiveLock); err != nil {
				if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
					ee.Pos = o.stmt.Pos()
				}
				return nil, err
			}
		}
		if o.stmt.IndexName != "" && tbl != nil {
			// CLUSTER tbl USING idx — mark the chosen index as the table's
			// clustering index and clear the flag on every other index
			// (mark_index_clustered, cluster.c). The `ALTER TABLE <t> CLUSTER ON
			// <idx>` clause records the same selection (slice 321). DU-002 slice 320.
			if err := markTableClusterIndex(o.ctx, tbl, o.stmt.IndexName, o.stmt.Pos()); err != nil {
				return nil, err
			}
		}
	}
	// Table exists — no physical reorder; the clustering selection (if any)
	// is recorded above for dump fidelity.
	return nil, EOF
}

// markTableClusterIndex marks idxName as table tbl's clustering index
// (pg_index.indisclustered = true) and clears the flag on every other index of
// the table, mirroring mark_index_clustered (cluster.c). goopg performs no
// physical index-ordered heap rewrite, but it records the selection so pg_dump
// re-emits a trailing `ALTER TABLE <t> CLUSTER ON <idx>;` after the index's
// CREATE INDEX / ADD CONSTRAINT, keyed on pg_index.indisclustered (pg_dump.c
// dumpIndex / dumpConstraint). The flag lives on the in-memory catalog.Index
// and in the pg_index HEAP row pg_dump reads, so each changed index's heap row
// is re-synced (mirrors the REPLICA IDENTITY USING INDEX path). Shared by the
// `CLUSTER <t> USING <idx>` statement (slice 320) and the `ALTER TABLE <t>
// CLUSTER ON <idx>` action (slice 321). Returns 42704 if no such index exists.
func markTableClusterIndex(ctx *Context, tbl *catalog.Table, idxName string, pos int) error {
	var chosen *catalog.Index
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if strings.EqualFold(idx.Name, idxName) {
			chosen = idx
			break
		}
	}
	if chosen == nil {
		return &ExecError{
			Code:    "42704",
			Pos:     pos,
			Message: fmt.Sprintf("index %q for table %q does not exist", idxName, tbl.Name),
		}
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		want := idx == chosen
		if idx.IsClustered != want {
			idx.IsClustered = want
			if err := resyncIndexHeapRow(ctx, idx); err != nil {
				return err
			}
		}
	}
	return nil
}

// clearTableClusterIndex clears the clustering selection on every index of tbl
// (pg_index.indisclustered → false), mirroring `ALTER TABLE … SET WITHOUT
// CLUSTER` (tablecmds.c ATExecSetWithoutCluster → mark_index_clustered(rel,
// InvalidOid)). DU-002 slice 321.
func clearTableClusterIndex(ctx *Context, tbl *catalog.Table) error {
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if idx.IsClustered {
			idx.IsClustered = false
			if err := resyncIndexHeapRow(ctx, idx); err != nil {
				return err
			}
		}
	}
	return nil
}
