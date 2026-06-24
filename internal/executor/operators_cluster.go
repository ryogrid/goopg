package executor

// operators_cluster.go — CLUSTER statement executor (M0095-0008).
//
// CLUSTER is a no-op in goopg v0: no physical index-ordered heap rewrite
// is performed. The executor validates the target table exists (so that
// CLUSTER nonexistent returns an error as PostgreSQL would) and then
// returns EOF immediately.

import (
	"fmt"

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
	}
	// Table exists — no-op reorder.
	return nil, EOF
}
