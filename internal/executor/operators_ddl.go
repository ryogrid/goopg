package executor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// ddlOp is a one-shot operator that runs a DDL statement against the
// catalog and (when applicable) the storage manager. It produces no
// output rows; the wire-protocol path emits the canonical
// CommandComplete tag for the verb.
type ddlOp struct {
	plan *planner.DDL
	ctx  *Context
	done bool
}

func newDDLOp(p *planner.DDL) *ddlOp { return &ddlOp{plan: p} }

func (o *ddlOp) Schema() planner.Schema { return nil }
func (o *ddlOp) Open(ctx *Context) error {
	if ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "DDL requires Catalog in Context"}
	}
	o.ctx = ctx
	return nil
}
func (o *ddlOp) Close() error { return nil }

func (o *ddlOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	switch s := o.plan.Stmt.(type) {
	case *parser.CreateTableStmt:
		return nil, o.execCreateTable(s)
	case *parser.CreateIndexStmt:
		return nil, o.execCreateIndex(s)
	case *parser.DropTableStmt:
		return nil, o.execDropTable(s)
	case *parser.DropIndexStmt:
		return nil, o.execDropIndex(s)
	case *parser.TruncateStmt:
		return nil, o.execTruncate(s)
	case *parser.AlterTableStmt:
		return nil, o.execAlterTable(s)
	}
	return nil, &ExecError{Code: "0A000", Pos: o.plan.Pos(), Message: fmt.Sprintf("DDL %T not supported in v0 executor", o.plan.Stmt)}
}

func (o *ddlOp) execCreateTable(s *parser.CreateTableStmt) error {
	if _, exists := o.ctx.Catalog.LookupTable(s.Name); exists {
		if s.IfNotExists {
			return nil
		}
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", s.Name.String())}
	}
	cols := make([]catalog.Column, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = catalog.Column{
			Name:    c.Name,
			Type:    catalog.Type{Name: strings.ToLower(c.Type.Name), Args: append([]int64(nil), c.Type.Args...)},
			NotNull: c.NotNull,
		}
	}
	if _, err := o.ctx.Catalog.CreateTable(s.Name, cols); err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execDropTable(s *parser.DropTableStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP TABLE requires Pool in Context"}
	}
	for _, name := range s.Names {
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			if s.IfExists {
				continue
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		idxs := o.ctx.Catalog.IndexesOnTable(tbl)
		idxRels := make([]storage.RelFileNode, 0, len(idxs))
		for _, idx := range idxs {
			idxRels = append(idxRels, o.ctx.Catalog.IndexRelFileNode(idx))
		}
		rel := o.ctx.Catalog.RelFileNode(tbl)
		if err := o.ctx.Catalog.DropTable(name); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		o.ctx.Pool.InvalidateRel(rel)
		if err := o.ctx.Pool.Manager().DropRelation(rel); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		for _, idxRel := range idxRels {
			o.ctx.Pool.InvalidateRel(idxRel)
			if err := o.ctx.Pool.Manager().DropRelation(idxRel); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
		}
	}
	return nil
}

func (o *ddlOp) execCreateIndex(s *parser.CreateIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE INDEX requires Pool in Context"}
	}
	tbl, ok := o.ctx.Catalog.LookupTable(s.Table)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Table.String())}
	}
	name := s.Name
	if name == "" {
		name = o.autoIndexName(tbl, s.Columns, "idx")
	}
	idxName := parser.ObjectName{Schema: tbl.Schema, Name: name}
	if _, exists := o.ctx.Catalog.LookupIndex(idxName); exists {
		if s.IfNotExists {
			return nil
		}
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", idxName.String())}
	}
	method := strings.ToLower(strings.TrimSpace(s.Method))
	if method == "" {
		method = "btree"
	}
	if method != "btree" {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: fmt.Sprintf("index method %q is not supported in v0", method)}
	}
	return o.createSingleColumnBTreeIndex(s.Pos(), idxName, tbl, s.Columns, s.Unique, false)
}

func (o *ddlOp) execDropIndex(s *parser.DropIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP INDEX requires Pool in Context"}
	}
	for _, name := range s.Names {
		idx, ok := o.ctx.Catalog.LookupIndex(name)
		if !ok {
			if s.IfExists {
				continue
			}
			return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("index %q does not exist", name.String())}
		}
		rel := o.ctx.Catalog.IndexRelFileNode(idx)
		if err := o.ctx.Catalog.DropIndex(name); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		o.ctx.Pool.InvalidateRel(rel)
		if err := o.ctx.Pool.Manager().DropRelation(rel); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
	}
	return nil
}

func (o *ddlOp) execAlterTable(s *parser.AlterTableStmt) error {
	tbl, ok := o.ctx.Catalog.LookupTable(s.Name)
	if !ok {
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Name.String())}
	}
	for _, act := range s.Actions {
		switch act.Kind {
		case parser.AlterTableAddColumn:
			if err := o.execAlterTableAddColumn(s, tbl, act); err != nil {
				return err
			}
		case parser.AlterTableAddPrimaryKey:
			if err := o.execAlterTableAddPrimaryKey(tbl, act); err != nil {
				return err
			}
		default:
			return &ExecError{Code: "0A000", Pos: act.Pos(), Message: "ALTER TABLE action is not supported in v0"}
		}
	}
	return nil
}

func (o *ddlOp) execAlterTableAddColumn(stmt *parser.AlterTableStmt, tbl *catalog.Table, act parser.AlterTableAction) error {
	col := act.Column
	if col.NotNull {
		rel := o.ctx.Catalog.RelFileNode(tbl)
		n, err := o.ctx.Pool.NBlocks(rel)
		if err != nil {
			return &ExecError{Code: "XX000", Pos: act.Pos(), Message: err.Error()}
		}
		if n > 0 {
			return &ExecError{Code: "0A000", Pos: act.Pos(), Message: "ALTER TABLE ADD COLUMN ... NOT NULL is only supported on empty tables"}
		}
	}
	_, err := o.ctx.Catalog.AddColumn(tbl, catalog.Column{
		Name:    col.Name,
		Type:    catalog.Type{Name: strings.ToLower(col.Type.Name), Args: append([]int64(nil), col.Type.Args...)},
		NotNull: col.NotNull,
	})
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return &ExecError{Code: "42701", Pos: act.Pos(), Message: err.Error()}
	}
	return &ExecError{Code: "XX000", Pos: stmt.Pos(), Message: err.Error()}
}

func (o *ddlOp) execAlterTableAddPrimaryKey(tbl *catalog.Table, act parser.AlterTableAction) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: "ALTER TABLE ADD PRIMARY KEY requires Pool in Context"}
	}
	if o.ctx.Catalog.HasPrimaryKey(tbl) {
		return &ExecError{Code: "42P16", Pos: act.Pos(), Message: fmt.Sprintf("multiple primary keys for table %q are not allowed", tbl.QualifiedName())}
	}
	name := act.ConstraintName
	if name == "" {
		name = tbl.Name + "_pkey"
	}
	idxName := parser.ObjectName{Schema: tbl.Schema, Name: name}
	return o.createSingleColumnBTreeIndex(act.Pos(), idxName, tbl, act.Columns, true, true)
}

func (o *ddlOp) createSingleColumnBTreeIndex(pos int, idxName parser.ObjectName, tbl *catalog.Table, columns []string, unique bool, primary bool) error {
	if len(columns) != 1 {
		return &ExecError{Code: "0A000", Pos: pos, Message: "only single-column btree indexes are supported in v0"}
	}
	col, ok := o.ctx.Catalog.LookupColumn(tbl, columns[0])
	if !ok {
		return &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", columns[0], tbl.Name)}
	}
	if !isInt4Type(col.Type.Name) {
		return &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("btree v0 only supports int4 keys, got %q", col.Type.Name)}
	}
	idx, err := o.ctx.Catalog.CreateIndex(idxName, tbl, columns, unique, "btree", primary)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return &ExecError{Code: "42P07", Pos: pos, Message: err.Error()}
		}
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Create(o.ctx.Pool, idxRel)
	if err != nil {
		_ = o.ctx.Catalog.DropIndex(idxName)
		_ = o.ctx.Pool.Manager().DropRelation(idxRel)
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	if err := o.backfillSingleColumnBTree(tree, tbl, col, unique, idxName.String(), pos); err != nil {
		_ = o.ctx.Catalog.DropIndex(idxName)
		o.ctx.Pool.InvalidateRel(idxRel)
		_ = o.ctx.Pool.Manager().DropRelation(idxRel)
		return err
	}
	return nil
}

func (o *ddlOp) backfillSingleColumnBTree(tree *btree.BTree, tbl *catalog.Table, col *catalog.Column, unique bool, indexName string, pos int) error {
	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	seen := map[int32]struct{}{}
	const minInt32 = -1 << 31
	const maxInt32 = 1<<31 - 1
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		page := slot.Page()
		if storage.IsNew(page) {
			o.ctx.Pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			o.ctx.Pool.Unpin(slot)
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		for i := uint16(1); i <= uint16(count); i++ {
			tuple, err := storage.PageGetHeapTuple(page, i)
			if err != nil {
				if errors.Is(err, storage.ErrUnsupportedItem) {
					continue
				}
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
			}
			if tuple.Header.Xmin == storage.InvalidTransactionID || tuple.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := DecodeRow(tbl.Columns, tuple.Data)
			if err != nil {
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
			}
			v := row[col.Ordinal]
			if v.IsNull() {
				continue
			}
			if v.Kind != KindInt {
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not integer at runtime", col.Name)}
			}
			if v.Int < minInt32 || v.Int > maxInt32 {
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "22003", Pos: pos, Message: fmt.Sprintf("value %d out of int4 range for index key", v.Int)}
			}
			k := int32(v.Int)
			if unique {
				if _, exists := seen[k]; exists {
					o.ctx.Pool.Unpin(slot)
					return &ExecError{Code: "23505", Pos: pos, Message: fmt.Sprintf("duplicate key value violates unique index %q", indexName)}
				}
				seen[k] = struct{}{}
			}
			if err := tree.Insert(btree.EncodeInt4(k), storage.ItemPointer{Block: blk, Offset: i}); err != nil {
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
			}
		}
		o.ctx.Pool.Unpin(slot)
	}
	return nil
}

func (o *ddlOp) autoIndexName(tbl *catalog.Table, columns []string, suffix string) string {
	base := tbl.Name + "_" + strings.Join(columns, "_") + "_" + suffix
	candidate := base
	for i := 1; ; i++ {
		if _, exists := o.ctx.Catalog.LookupIndex(parser.ObjectName{Schema: tbl.Schema, Name: candidate}); !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
}

func isInt4Type(name string) bool {
	switch strings.ToLower(name) {
	case "int4", "integer", "int":
		return true
	default:
		return false
	}
}

func (o *ddlOp) execTruncate(s *parser.TruncateStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "TRUNCATE requires Pool in Context"}
	}
	for _, name := range s.Names {
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		idxs := o.ctx.Catalog.IndexesOnTable(tbl)
		rel := o.ctx.Catalog.RelFileNode(tbl)
		o.ctx.Pool.InvalidateRel(rel)
		if err := o.ctx.Pool.Manager().TruncateRelation(rel); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		for _, idx := range idxs {
			idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
			o.ctx.Pool.InvalidateRel(idxRel)
			if err := o.ctx.Pool.Manager().TruncateRelation(idxRel); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
			if _, err := btree.Create(o.ctx.Pool, idxRel); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
		}
	}
	return nil
}
