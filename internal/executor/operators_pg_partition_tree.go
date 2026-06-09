package executor

// pg_partition_tree / pg_partition_ancestors multi-row SRF operators.
// Both functions traverse the catalog partition hierarchy and return rows
// sorted alphabetically by relation name. M0097-0023.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

type pgPartitionTreeOp struct {
	plan *planner.PgPartitionTree
	rows []Row
	idx  int
}

func newPgPartitionTreeOp(p *planner.PgPartitionTree) *pgPartitionTreeOp {
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

	relName := partitionResolveRegclass(argVal, im)
	if relName == "" {
		return nil
	}

	isTree := strings.EqualFold(o.plan.FuncName, "pg_partition_tree")
	if isTree {
		o.rows = partitionBuildTree(relName, im)
	} else {
		o.rows = partitionBuildAncestors(relName, im)
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

func (o *pgPartitionTreeOp) Close() error                  { return nil }
func (o *pgPartitionTreeOp) Schema() planner.Schema        { return o.plan.Output() }

// partitionResolveRegclass converts a KindInt OID or KindString name Datum to
// a relation name, searching both tables and indexes.
func partitionResolveRegclass(d Datum, im *catalog.InMemory) string {
	switch d.Kind {
	case KindInt:
		oid := uint32(d.Int)
		if tbl, ok := im.LookupTableByOID(oid); ok {
			return tbl.Name
		}
		if idx, ok := im.LookupIndexByOID(oid); ok {
			return idx.Name
		}
		return ""
	case KindString:
		name := d.StringValue()
		if tbl, ok := im.LookupTable(parser.ObjectName{Name: name}); ok {
			return tbl.Name
		}
		if idx, ok := im.LookupIndex(parser.ObjectName{Name: name}); ok {
			return idx.Name
		}
		return ""
	default:
		return ""
	}
}

// partitionBuildTree builds all rows for pg_partition_tree starting at relName.
// Looks up the name as both a table and an index.
func partitionBuildTree(relName string, im *catalog.InMemory) []Row {
	if tbl, ok := im.LookupTable(parser.ObjectName{Name: relName}); ok {
		return partitionTableTree(tbl, im)
	}
	if idx, ok := im.LookupIndex(parser.ObjectName{Name: relName}); ok {
		return partitionIndexTree(idx, im)
	}
	return nil
}

// partitionTableTree returns pg_partition_tree rows for a table partition hierarchy.
// Returns nil for non-partitioned tables (no PartitionKey), views, matviews,
// and legacy-inheritance tables.
func partitionTableTree(root *catalog.Table, im *catalog.InMemory) []Row {
	isRootNode := root.PartitionParentOID == 0
	if isRootNode && len(root.PartitionKey) == 0 {
		// Not a partitioned table and not a partition child — emit nothing.
		return nil
	}

	type bfsItem struct {
		tbl        *catalog.Table
		level      int
		parentName string
	}
	type entry struct {
		name       string
		parentName string
		isLeaf     bool
		level      int
	}

	startParentName := ""
	if root.PartitionParentOID != 0 {
		if parentTbl, ok2 := im.LookupTableByOID(root.PartitionParentOID); ok2 {
			startParentName = parentTbl.Name
		}
	}

	var entries []entry
	queue := []bfsItem{{tbl: root, level: 0, parentName: startParentName}}
	visited := map[uint32]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.tbl.OID] {
			continue
		}
		visited[cur.tbl.OID] = true
		children := im.PartitionChildren(cur.tbl.OID)
		isLeaf := len(cur.tbl.PartitionKey) == 0
		entries = append(entries, entry{
			name:       cur.tbl.Name,
			parentName: cur.parentName,
			isLeaf:     isLeaf,
			level:      cur.level,
		})
		for _, child := range children {
			queue = append(queue, bfsItem{tbl: child, level: cur.level + 1, parentName: cur.tbl.Name})
		}
	}

	rows := make([]Row, len(entries))
	for i, e := range entries {
		var parentRelid Datum
		if e.parentName != "" {
			if pt, ok := im.LookupTable(parser.ObjectName{Name: e.parentName}); ok {
				parentRelid = NewIntDatum(int64(pt.OID))
			} else {
				parentRelid = NullDatum
			}
		} else {
			parentRelid = NullDatum
		}
		var relOID Datum
		if rt, ok := im.LookupTable(parser.ObjectName{Name: e.name}); ok {
			relOID = NewIntDatum(int64(rt.OID))
		} else {
			relOID = NullDatum
		}
		rows[i] = Row{relOID, parentRelid, NewBoolDatum(e.isLeaf), NewIntDatum(int64(e.level))}
	}
	return rows
}

// partitionIndexTree returns pg_partition_tree rows for an index partition hierarchy.
func partitionIndexTree(root *catalog.Index, im *catalog.InMemory) []Row {
	// Index must be part of a partition tree (has children or a parent).
	if root.PartitionParentOID == 0 && len(im.IndexPartitionChildren(root.OID)) == 0 {
		return nil
	}

	type bfsItem struct {
		idx        *catalog.Index
		level      int
		parentName string
	}
	type entry struct {
		name       string
		parentName string
		isLeaf     bool
		level      int
	}

	startParentName := ""
	if root.PartitionParentOID != 0 {
		if parentIdx, ok2 := im.LookupIndexByOID(root.PartitionParentOID); ok2 {
			startParentName = parentIdx.Name
		}
	}

	var entries []entry
	queue := []bfsItem{{idx: root, level: 0, parentName: startParentName}}
	visited := map[uint32]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.idx.OID] {
			continue
		}
		visited[cur.idx.OID] = true
		children := im.IndexPartitionChildren(cur.idx.OID)
		isLeaf := cur.idx.Table == nil || len(cur.idx.Table.PartitionKey) == 0
		entries = append(entries, entry{
			name:       cur.idx.Name,
			parentName: cur.parentName,
			isLeaf:     isLeaf,
			level:      cur.level,
		})
		for _, child := range children {
			queue = append(queue, bfsItem{idx: child, level: cur.level + 1, parentName: cur.idx.Name})
		}
	}

	rows := make([]Row, len(entries))
	for i, e := range entries {
		var parentRelid Datum
		if e.parentName != "" {
			if pi, ok := im.LookupIndex(parser.ObjectName{Name: e.parentName}); ok {
				parentRelid = NewIntDatum(int64(pi.OID))
			} else {
				parentRelid = NullDatum
			}
		} else {
			parentRelid = NullDatum
		}
		var relOID Datum
		if ri, ok := im.LookupIndex(parser.ObjectName{Name: e.name}); ok {
			relOID = NewIntDatum(int64(ri.OID))
		} else {
			relOID = NullDatum
		}
		rows[i] = Row{relOID, parentRelid, NewBoolDatum(e.isLeaf), NewIntDatum(int64(e.level))}
	}
	return rows
}

// partitionBuildAncestors returns the ancestor chain rows for relName.
func partitionBuildAncestors(relName string, im *catalog.InMemory) []Row {
	if tbl, ok := im.LookupTable(parser.ObjectName{Name: relName}); ok {
		return partitionTableAncestors(tbl, im)
	}
	if idx, ok := im.LookupIndex(parser.ObjectName{Name: relName}); ok {
		return partitionIndexAncestors(idx, im)
	}
	return nil
}

// partitionTableAncestors walks up the PartitionParentOID chain from start,
// collecting the names, then returns rows sorted alphabetically.
// Returns nil for non-partitioned tables (no parent, no PartitionKey).
func partitionTableAncestors(start *catalog.Table, im *catalog.InMemory) []Row {
	// Non-partitioned table → no rows.
	if start.PartitionParentOID == 0 && len(start.PartitionKey) == 0 {
		return nil
	}

	var names []string
	cur := start
	visited := map[uint32]bool{}
	for cur != nil && !visited[cur.OID] {
		visited[cur.OID] = true
		names = append(names, cur.Name)
		if cur.PartitionParentOID == 0 {
			break
		}
		parent, ok := im.LookupTableByOID(cur.PartitionParentOID)
		if !ok {
			break
		}
		cur = parent
	}

	rows := make([]Row, len(names))
	for i, name := range names {
		if t, ok := im.LookupTable(parser.ObjectName{Name: name}); ok {
			rows[i] = Row{NewIntDatum(int64(t.OID))}
		} else {
			rows[i] = Row{NullDatum}
		}
	}
	return rows
}

// partitionIndexAncestors walks up the PartitionParentOID chain for indexes.
func partitionIndexAncestors(start *catalog.Index, im *catalog.InMemory) []Row {
	// Index not part of partition tree → no rows.
	if start.PartitionParentOID == 0 && len(im.IndexPartitionChildren(start.OID)) == 0 {
		return nil
	}

	var names []string
	cur := start
	visited := map[uint32]bool{}
	for cur != nil && !visited[cur.OID] {
		visited[cur.OID] = true
		names = append(names, cur.Name)
		if cur.PartitionParentOID == 0 {
			break
		}
		parent, ok := im.LookupIndexByOID(cur.PartitionParentOID)
		if !ok {
			break
		}
		cur = parent
	}

	rows := make([]Row, len(names))
	for i, name := range names {
		if idx, ok := im.LookupIndex(parser.ObjectName{Name: name}); ok {
			rows[i] = Row{NewIntDatum(int64(idx.OID))}
		} else {
			rows[i] = Row{NullDatum}
		}
	}
	return rows
}
