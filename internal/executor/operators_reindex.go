package executor

// operators_reindex.go — REINDEX statement executor (M0097-0023).
//
// REINDEX INDEX name validates the index exists; REINDEX TABLE validates
// the table. Physical reindex is a no-op in goopg v0 (no physical btree
// rebuild). Raises 42P01 for nonexistent targets matching PostgreSQL.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

type reindexOp struct {
	stmt *parser.ReindexStmt
	ctx  *Context
	done bool
}

func newReindexOp(s *parser.ReindexStmt) *reindexOp { return &reindexOp{stmt: s} }

func (o *reindexOp) Schema() planner.Schema { return nil }

func (o *reindexOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *reindexOp) Close() error { return nil }

func (o *reindexOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	if o.stmt.Name == "" || o.ctx == nil || o.ctx.Catalog == nil {
		return nil, EOF
	}

	name := parser.ObjectName{Name: o.stmt.Name}
	// Try schema-qualified form.
	if dotIdx := indexOfDot(o.stmt.Name); dotIdx >= 0 {
		name.Schema = o.stmt.Name[:dotIdx]
		name.Name = o.stmt.Name[dotIdx+1:]
	}

	// REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name> targets a synthetic
	// TOAST relation/index (exposed by the TOAST-exposure epic slices 1–4). It
	// has no physical heap/btree to rebuild, but the spec exercises the
	// CONCURRENTLY wait: REINDEX waits for every transaction holding the PARENT
	// table locked to finish before swapping the rebuilt index, then — if the
	// parent was dropped while it waited — errors that the TOAST relation no
	// longer exists (PG resolves the toast relation through its parent). Route
	// both object types here so the synthetic TOAST name resolves before the
	// real-relation lookups below (which would only see numeric pg_toast names).
	// M0118-0008 (reindex-concurrently-toast, design 0118-0088).
	if im, isMem := o.ctx.Catalog.(*catalog.InMemory); isMem && strings.EqualFold(name.Schema, "pg_toast") {
		toastOID, _, ok := im.LookupToastRel(name.Schema, name.Name)
		if !ok {
			return nil, &ExecError{
				Code:    "42P01",
				Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
			}
		}
		if o.stmt.Concurrently {
			// Wait for lockers of the TOAST relation (parentOID + offset), not
			// the parent table: REINDEX CONCURRENTLY on a toast relation/index
			// waits for transactions that toasted a value or dropped the table
			// (which lock the toast rel), not for a bare LOCK TABLE on the parent
			// (which doesn't). Both REINDEX TABLE and REINDEX INDEX on a toast
			// object resolve to the same toast relation's lockers.
			if parent, pok := im.ToastParentTable(toastOID); pok {
				if toastRel, has := im.ToastRelFileNode(im.RelFileNode(parent)); has {
					if err := o.ctx.waitForRelationLockers(toastRel); err != nil {
						return nil, err
					}
				}
			}
			// The wait above returned once the locking transaction ended; if it
			// dropped the parent table the synthetic TOAST relation is gone too.
			if _, _, stillOK := im.LookupToastRel(name.Schema, name.Name); !stillOK {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
		// Physical reindex of the synthetic TOAST object is a no-op in v0.
		return nil, EOF
	}

	switch o.stmt.ObjectType {
	case "INDEX":
		if _, ok := o.ctx.Catalog.LookupIndex(name); !ok {
			// Try unqualified.
			if _, ok2 := o.ctx.Catalog.LookupIndex(parser.ObjectName{Name: name.Name}); !ok2 {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
	case "TABLE":
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			if tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Name: name.Name}); !ok {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
		// REINDEX TABLE CONCURRENTLY waits for every transaction that holds a
		// lock on the table to finish before it can swap in the rebuilt index,
		// without itself blocking concurrent reads or writes (it holds only
		// ShareUpdateExclusive). goopg's index rebuild is a no-op, but the wait
		// is observable concurrency behaviour, so reproduce it via the
		// WaitForLockers analog on the dedicated table lock manager. M0118-0008
		// (reindex-concurrently isolation spec).
		if o.stmt.Concurrently {
			if err := o.ctx.waitForRelationLockers(o.ctx.Catalog.RelFileNode(tbl)); err != nil {
				return nil, err
			}
		}
	case "SCHEMA":
		// REINDEX SCHEMA rebuilds every index on every table in the schema.
		// goopg's physical rebuild is a no-op, but the lock behaviour is
		// observable: a plain reindex takes a ShareLock per relation (which
		// conflicts with a concurrent SHARE UPDATE EXCLUSIVE holder, so it
		// waits), while REINDEX SCHEMA CONCURRENTLY takes no conflicting lock
		// and instead waits for existing lockers to drain (the CONCURRENTLY
		// contract). Relations are processed in OID (creation) order so the
		// stall lands on the earliest-created locked table first — which lets a
		// concurrent DROP of a later, not-yet-reached table proceed while the
		// reindex waits, exactly like upstream. M0118-0008 (reindex-schema).
		schemaName := name.Name
		if name.Schema != "" {
			schemaName = name.Schema
		}
		for _, rel := range o.schemaRelsByOID(schemaName) {
			if o.stmt.Concurrently {
				if err := o.ctx.waitForRelationLockers(rel); err != nil {
					return nil, err
				}
			} else {
				if err := o.ctx.acquireRelLockMaybeTransient(rel, lockmgr.ShareLock); err != nil {
					return nil, err
				}
			}
		}
	}
	// Physical reindex is a no-op in v0.
	return nil, EOF
}

// schemaRelsByOID returns the RelFileNodes of every non-virtual user table in
// schemaName, ordered by OID (creation order). REINDEX SCHEMA locks/waits on
// relations in this order so the first stall is on the earliest-created locked
// table, matching upstream's relation processing order. M0118-0008.
func (o *reindexOp) schemaRelsByOID(schemaName string) []storage.RelFileNode {
	tables := o.ctx.Catalog.TablesInSchema(schemaName)
	type relOID struct {
		rfn storage.RelFileNode
		oid uint32
	}
	rels := make([]relOID, 0, len(tables))
	for _, tn := range tables {
		tbl, ok := o.ctx.Catalog.LookupTable(tn)
		if !ok {
			continue
		}
		rels = append(rels, relOID{rfn: o.ctx.Catalog.RelFileNode(tbl), oid: tbl.OID})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].oid < rels[j].oid })
	out := make([]storage.RelFileNode, len(rels))
	for i, r := range rels {
		out[i] = r.rfn
	}
	return out
}

// indexOfDot returns the index of '.' in s, or -1 if not present.
func indexOfDot(s string) int {
	for i, c := range s {
		if c == '.' {
			return i
		}
	}
	return -1
}
