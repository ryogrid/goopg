package executor

// pg_partition_tree / pg_partition_ancestors multi-row SRF operators.
// Both functions traverse the catalog partition hierarchy of a table or an
// index. M0097-0023.
//
// The traversal is OID-based (M0149-0001h1a31): the argument is a regclass
// (an OID, or text run through regclassin, which honours search_path), and
// every hop resolves by OID — the earlier name-based walk looked relations up
// by bare name, so anything outside the default schema produced no rows. A
// table's parent is its PartitionParentOID or, for an ATTACHed partition (which
// leaves that field 0), the live partition registry.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type pgPartitionTreeOp struct {
	plan *optimizer.PgPartitionTree
	rows []Row
	idx  int
}

func newPgPartitionTreeOp(p *optimizer.PgPartitionTree) *pgPartitionTreeOp {
	return &pgPartitionTreeOp{plan: p}
}

func (o *pgPartitionTreeOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}

	if o.plan.Arg == nil {
		return nil
	}
	argVal, err := evalExpr(o.plan.Arg, nil, ctx)
	if err != nil || argVal.IsNull() {
		return nil
	}

	oid, ok := partitionResolveRegclass(argVal, ctx)
	if !ok {
		return nil
	}
	pt := partitionTree{im: im, dbOid: catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)}

	isTree := strings.EqualFold(o.plan.FuncName, "pg_partition_tree")
	if tbl, found := im.LookupTableByOID(oid, pt.dbOid); found {
		if isTree {
			o.rows = pt.tableTree(tbl)
		} else {
			o.rows = pt.tableAncestors(tbl)
		}
		return nil
	}
	if idx, found := im.LookupIndexByOID(oid, pt.dbOid); found {
		if isTree {
			o.rows = pt.indexTree(idx)
		} else {
			o.rows = pt.indexAncestors(idx)
		}
	}
	return nil
}

func (o *pgPartitionTreeOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

func (o *pgPartitionTreeOp) Close() error             { return nil }
func (o *pgPartitionTreeOp) Schema() optimizer.Schema { return o.plan.Output() }

// partitionResolveRegclass turns the regclass argument into an OID: an
// integer is the OID itself; text goes through regclassin (search_path,
// schema qualification, quoted names).
func partitionResolveRegclass(d Datum, ctx *Context) (uint32, bool) {
	switch d.Kind {
	case KindInt:
		return uint32(d.Int), d.Int > 0
	case KindString:
		r, err := regIdentifierInput(d, "regclass", ctx, 0)
		if err != nil || r.Kind != KindInt || r.Int <= 0 {
			return 0, false
		}
		return uint32(r.Int), true
	}
	return 0, false
}

type partitionTree struct {
	im    *catalog.InMemory
	dbOid uint32
}

// tableParent is the partition parent of t — PartitionParentOID, or the live
// registry for an ATTACHed partition. A partition left in the incomplete-detach
// state (DetachPendingEpoch != 0) has no parent for traversal: PG's
// get_partition_ancestors stops at the detached boundary (M0118-0008).
func (pt partitionTree) tableParent(t *catalog.Table) uint32 {
	if t.DetachPendingEpoch != 0 {
		return 0
	}
	if t.PartitionParentOID != 0 {
		return t.PartitionParentOID
	}
	if p, ok := pt.im.PartitionParentOf(t.OID); ok {
		return p
	}
	return 0
}

// isPartition reports whether t is linked to a partition parent at all,
// whatever its detach state — pg_partition_tree / _ancestors answer for a
// detach-pending partition as a one-node tree rooted at itself
// (detach-partition-concurrently-3).
func (pt partitionTree) isPartition(t *catalog.Table) bool {
	if t.PartitionParentOID != 0 {
		return true
	}
	_, ok := pt.im.PartitionParentOf(t.OID)
	return ok
}

func oidDatum(oid uint32) Datum {
	if oid == 0 {
		return NullDatum
	}
	return NewIntDatum(int64(oid))
}

// tableTree is pg_partition_tree for a table: the hierarchy below t, BFS
// order, (relid, parentrelid, isleaf, level). Nothing for a table that is
// neither partitioned nor a partition.
func (pt partitionTree) tableTree(root *catalog.Table) []Row {
	if !pt.isPartition(root) && len(root.PartitionKey) == 0 {
		return nil
	}
	rootParent := pt.tableParent(root)
	type item struct {
		tbl    *catalog.Table
		parent uint32
		level  int
	}
	var rows []Row
	queue := []item{{tbl: root, parent: rootParent}}
	visited := map[uint32]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.tbl.OID] {
			continue
		}
		visited[cur.tbl.OID] = true
		isLeaf := len(cur.tbl.PartitionKey) == 0
		rows = append(rows, Row{oidDatum(cur.tbl.OID), oidDatum(cur.parent), NewBoolDatum(isLeaf), NewIntDatum(int64(cur.level))})
		for _, child := range pt.im.PartitionChildren(cur.tbl.OID, pt.dbOid) {
			// A detach-pending child is no longer part of this tree.
			if child.DetachPendingEpoch != 0 {
				continue
			}
			queue = append(queue, item{tbl: child, parent: cur.tbl.OID, level: cur.level + 1})
		}
	}
	return rows
}

// indexTree is pg_partition_tree for a partitioned index hierarchy.
func (pt partitionTree) indexTree(root *catalog.Index) []Row {
	if root.PartitionParentOID == 0 && len(pt.im.IndexPartitionChildren(root.OID)) == 0 {
		return nil
	}
	type item struct {
		idx    *catalog.Index
		parent uint32
		level  int
	}
	var rows []Row
	queue := []item{{idx: root, parent: root.PartitionParentOID}}
	visited := map[uint32]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.idx.OID] {
			continue
		}
		visited[cur.idx.OID] = true
		isLeaf := cur.idx.Table == nil || len(cur.idx.Table.PartitionKey) == 0
		rows = append(rows, Row{oidDatum(cur.idx.OID), oidDatum(cur.parent), NewBoolDatum(isLeaf), NewIntDatum(int64(cur.level))})
		for _, child := range pt.im.IndexPartitionChildren(cur.idx.OID) {
			queue = append(queue, item{idx: child, parent: cur.idx.OID, level: cur.level + 1})
		}
	}
	return rows
}

// tableAncestors is pg_partition_ancestors for a table: t itself, then each
// parent up to the root. Nothing for a table outside any partition tree.
func (pt partitionTree) tableAncestors(start *catalog.Table) []Row {
	if !pt.isPartition(start) && len(start.PartitionKey) == 0 {
		return nil
	}
	var rows []Row
	visited := map[uint32]bool{}
	for cur := start; cur != nil && !visited[cur.OID]; {
		visited[cur.OID] = true
		rows = append(rows, Row{NewIntDatum(int64(cur.OID))})
		p := pt.tableParent(cur)
		if p == 0 {
			break
		}
		next, ok := pt.im.LookupTableByOID(p, pt.dbOid)
		if !ok {
			break
		}
		cur = next
	}
	return rows
}

// indexAncestors is pg_partition_ancestors for an index.
func (pt partitionTree) indexAncestors(start *catalog.Index) []Row {
	if start.PartitionParentOID == 0 && len(pt.im.IndexPartitionChildren(start.OID)) == 0 {
		return nil
	}
	var rows []Row
	visited := map[uint32]bool{}
	for cur := start; cur != nil && !visited[cur.OID]; {
		visited[cur.OID] = true
		rows = append(rows, Row{NewIntDatum(int64(cur.OID))})
		if cur.PartitionParentOID == 0 {
			break
		}
		next, ok := pt.im.LookupIndexByOID(cur.PartitionParentOID, pt.dbOid)
		if !ok {
			break
		}
		cur = next
	}
	return rows
}
