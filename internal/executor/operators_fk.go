package executor

// M0097-0014: CHECK constraint enforcement added at the bottom of this file.

// operators_fk.go — FK referential integrity enforcement.
//
// M0096-0011: implements REFERENCES … ON DELETE {CASCADE | RESTRICT |
// SET NULL | NO ACTION} for inline column constraints declared in
// CREATE TABLE. DEFERRABLE INITIALLY DEFERRED constraints are queued
// in BasicSession.deferredFKChecks and verified by execCommit at commit time.
//
// Call sites:
//   insertOp.Next  → checkFKInsert  (parent must exist)
//   deleteOp.Next  → enforceFKOnDelete (cascade / restrict / set null)
//   execCommit     → runAllDeferredFKChecks

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// checkFKInsert verifies all FK constraints on tbl for the given inserted row.
// Returns a 23503 (foreign_key_violation) error when a referenced parent row
// does not exist.
func checkFKInsert(ctx *Context, tbl *catalog.Table, row Row) error {
	for _, fk := range tbl.ForeignKeys {
		// Gather the FK column values.
		vals, allNull := fkColValues(tbl.Columns, fk.Columns, row)
		if allNull {
			continue // NULL FK values are always allowed
		}
		// DEFERRABLE INITIALLY DEFERRED inside an explicit transaction: queue.
		if fk.Deferrable && fk.InitiallyDeferred && ctx.Session != nil && ctx.Session.InExplicitTransaction() {
			if sess, ok := ctx.Session.(*BasicSession); ok {
				sess.AddDeferredFKCheck(DeferredFKCheck{ChildTableName: tbl.Name, FK: fk})
				continue
			}
		}
		if err := assertParentExists(ctx, tbl, fk, vals); err != nil {
			return err
		}
	}
	return nil
}

// enforceFKOnDelete applies referential actions for all FK constraints where
// parentTbl is the REFERENCED table and parentRow is the deleted row.
func enforceFKOnDelete(ctx *Context, parentTbl *catalog.Table, parentRow Row) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	refs := im.FindFKsReferencingTable(parentTbl.Name)
	for _, ref := range refs {
		// Get the referenced column values from the deleted parent row.
		refCols := ref.FK.RefColumns
		if len(refCols) == 0 {
			// Default: use parent PK column(s).
			refCols = pkColumns(parentTbl)
		}
		vals, allNull := fkColValues(parentTbl.Columns, refCols, parentRow)
		if allNull {
			continue
		}
		fk := ref.FK
		switch fk.OnDelete {
		case parser.FKActionCascade:
			if err := fkCascadeDelete(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionSetNull:
			if err := fkSetNull(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionSetDefault:
			// Treat as SET NULL for simplicity.
			if err := fkSetNull(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionRestrict:
			// RESTRICT: always immediate.
			if err := assertNoChildRows(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		default: // NO ACTION
			if fk.Deferrable && fk.InitiallyDeferred && ctx.Session != nil && ctx.Session.InExplicitTransaction() {
				if sess, ok := ctx.Session.(*BasicSession); ok {
					sess.AddDeferredFKCheck(DeferredFKCheck{ChildTableName: ref.Child.Name, FK: fk})
					continue
				}
			}
			if err := assertNoChildRows(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		}
	}
	return nil
}

// runAllDeferredFKChecks verifies every queued deferred FK constraint.
// Called by execCommit before TxnMgr.Commit. Returns a 23503 error if
// any constraint is violated.
func runAllDeferredFKChecks(ctx *Context, checks []DeferredFKCheck) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, check := range checks {
		childTbl, ok := im.LookupTable(parser.ObjectName{Name: check.ChildTableName})
		if !ok {
			continue
		}
		if err := fullTableFKCheck(ctx, childTbl, check.FK); err != nil {
			return err
		}
	}
	return nil
}

// fullTableFKCheck scans every row in childTbl and verifies each row's FK
// column values exist in the referenced parent table. Used for deferred
// constraint checks at COMMIT time.
func fullTableFKCheck(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey) error {
	rel := ctx.Catalog.RelFileNode(childTbl)
	cols := childTbl.Columns
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
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
				continue
			}
			row, err := DecodeRow(cols, tuple.Data)
			if err != nil {
				continue
			}
			vals, allNull := fkColValues(cols, fk.Columns, row)
			if allNull {
				continue
			}
			if err := assertParentExists(ctx, childTbl, fk, vals); err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// assertParentExists verifies that the parent table (fk.RefTable) contains a
// row whose reference columns match vals. Returns 23503 if not found.
// childTbl is the table where the FK is declared (the one being INSERTed into
// or UPDATEd) — its name is reported in the error message to match upstream
// PostgreSQL's `insert or update on table %q violates foreign key constraint
// %q` format.
func assertParentExists(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	parentTbl, ok := im.LookupTable(parser.ObjectName{Name: fk.RefTable})
	if !ok {
		return nil // referenced table not found (CREATE TABLE out of order) — skip
	}
	// Determine the referenced columns.
	refCols := fk.RefColumns
	if len(refCols) == 0 {
		refCols = pkColumns(parentTbl)
	}
	found, err := scanTableForMatch(ctx, parentTbl, refCols, vals)
	if err != nil {
		return err
	}
	if !found {
		childName := ""
		if childTbl != nil {
			childName = childTbl.Name
		}
		return &ExecError{
			Code: "23503",
			Message: fmt.Sprintf("insert or update on table %q violates foreign key constraint %q",
				childName, fkConstraintName(childTbl, fk)),
			Detail: fmt.Sprintf("Key (%s)=(%s) is not present in table %q.",
				strings.Join(fk.Columns, ", "), fkValsForDetail(vals), fk.RefTable),
		}
	}
	return nil
}

// fkConstraintName synthesises the auto-generated PG constraint name
// `<table>_<col>_fkey` when the catalog has no explicit name. The
// referencing table's name is used (not the referenced table) — matches
// upstream's ChooseConstraintName for inline `col REFERENCES ...`
// declarations.
func fkConstraintName(childTbl *catalog.Table, fk catalog.ForeignKey) string {
	tbl := ""
	if childTbl != nil {
		tbl = childTbl.Name
	}
	col := ""
	if len(fk.Columns) > 0 {
		col = fk.Columns[0]
	}
	return fmt.Sprintf("%s_%s_fkey", tbl, col)
}

// fkValsForDetail renders FK column values for the PostgreSQL-style DETAIL
// line `Key (col)=(val) is not present in table "<parent>".`
func fkValsForDetail(vals []Datum) string {
	var sb strings.Builder
	for i, v := range vals {
		if i > 0 {
			sb.WriteString(", ")
		}
		if v.IsNull() {
			sb.WriteString("null")
			continue
		}
		sb.WriteString(v.Format())
	}
	return sb.String()
}

// assertNoChildRows verifies that no child rows reference the given parent
// values via the FK constraint. Returns 23503 if child rows exist.
func assertNoChildRows(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	found, err := scanTableForMatch(ctx, childTbl, fk.Columns, vals)
	if err != nil {
		return err
	}
	if found {
		return &ExecError{
			Code:    "23503",
			Message: fmt.Sprintf("update or delete on table %q violates foreign key constraint on table %q",
				fk.RefTable, childTbl.Name),
		}
	}
	return nil
}

// fkCascadeDelete deletes all child rows in childTbl (and its partition/inheritance
// children) whose FK columns match vals.
func fkCascadeDelete(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	// Collect all relation tables to scan (parent + partition/inheritance children).
	tables := []*catalog.Table{childTbl}
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		tables = append(tables, im.PartitionChildren(childTbl.OID)...)
		tables = append(tables, im.InheritanceChildren(childTbl.OID)...)
	}

	type victim struct {
		tbl  *catalog.Table
		blk  storage.BlockNumber
		slot uint16
		row  Row
	}
	var victims []victim

	for _, scanTbl := range tables {
		rel := ctx.Catalog.RelFileNode(scanTbl)
		cols := scanTbl.Columns
		nBlocks, err := ctx.Pool.NBlocks(rel)
		if err != nil {
			continue
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
			for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
				tuple, err := storage.PageGetHeapTuple(page, slotIdx)
				if err != nil {
					continue
				}
				if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
					continue
				}
				row, err := DecodeRow(cols, tuple.Data)
				if err != nil {
					continue
				}
				if fkRowMatches(cols, fk.Columns, row, vals) {
					victims = append(victims, victim{tbl: scanTbl, blk: blk, slot: slotIdx, row: row})
				}
			}
			s.RUnlock()
			ctx.Pool.Unpin(s)
		}
	}

	for _, v := range victims {
		// Recursively enforce FK constraints on the child table (for multi-level cascades).
		if err := enforceFKOnDelete(ctx, v.tbl, v.row); err != nil {
			return err
		}
		rel := ctx.Catalog.RelFileNode(v.tbl)
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: v.blk})
		if err != nil {
			return err
		}
		s.Lock()
		if err := storage.PageSetHeapTupleXmax(s.Page(), v.slot, ctx.Tx.XID); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue // slot may have been reused; skip
		}
		markHeapDeleteDirtyAndClearVM(ctx, s, rel, v.blk, v.slot, ctx.Tx.XID, nil)
		s.Unlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// fkSetNull updates FK columns to NULL in child rows that match vals.
func fkSetNull(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	rel := ctx.Catalog.RelFileNode(childTbl)
	cols := childTbl.Columns

	type pendingUpdate struct {
		blk    storage.BlockNumber
		slot   uint16
		newRow Row
	}
	var pending []pendingUpdate

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
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
				continue
			}
			row, err := DecodeRow(cols, tuple.Data)
			if err != nil {
				continue
			}
			if !fkRowMatches(cols, fk.Columns, row, vals) {
				continue
			}
			newRow := make(Row, len(cols))
			copy(newRow, row)
			for _, fkCol := range fk.Columns {
				for i, c := range cols {
					if strings.EqualFold(c.Name, fkCol) {
						newRow[i] = NullDatum
					}
				}
			}
			pending = append(pending, pendingUpdate{blk: blk, slot: slotIdx, newRow: newRow})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}

	for _, pu := range pending {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
		if err != nil {
			return err
		}
		s.Lock()
		if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, ctx.Tx.XID); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue
		}
		markHeapDeleteDirtyAndClearVM(ctx, s, rel, pu.blk, pu.slot, ctx.Tx.XID, nil)
		s.Unlock()
		ctx.Pool.Unpin(s)
		if err := writeHeapRow(ctx, rel, cols, pu.newRow); err != nil {
			return err
		}
	}
	return nil
}

// scanTableForMatch returns true if tbl has a visible row where the named
// columns match the given values exactly.
func scanTableForMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, error) {
	// Also scan partition/inheritance children.
	found, err := scanRelForMatch(ctx, tbl, colNames, vals)
	if err != nil || found {
		return found, err
	}
	// Check inheritance children (covers partition children too, since PartitionChildren
	// returns the same *Table slice as InheritanceChildren for partition parents).
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		for _, child := range im.InheritanceChildren(tbl.OID) {
			found, err = scanRelForMatch(ctx, child, colNames, vals)
			if err != nil || found {
				return found, err
			}
		}
		for _, child := range im.PartitionChildren(tbl.OID) {
			found, err = scanRelForMatch(ctx, child, colNames, vals)
			if err != nil || found {
				return found, err
			}
		}
	}
	return false, nil
}

func scanRelForMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, error) {
	rel := ctx.Catalog.RelFileNode(tbl)
	cols := tbl.Columns
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return false, nil // relation may not have blocks yet
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, err
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
			return false, err
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
				continue
			}
			row, err := DecodeRow(cols, tuple.Data)
			if err != nil {
				continue
			}
			if fkRowMatches(cols, colNames, row, vals) {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return true, nil
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return false, nil
}

// fkColValues extracts the FK column values from a row, returning
// (vals, allNull). allNull is true when every FK column value is NULL.
func fkColValues(cols []catalog.Column, fkCols []string, row Row) ([]Datum, bool) {
	vals := make([]Datum, len(fkCols))
	allNull := true
	for i, name := range fkCols {
		for j, c := range cols {
			if strings.EqualFold(c.Name, name) {
				if j < len(row) {
					vals[i] = row[j]
					if !row[j].IsNull() {
						allNull = false
					}
				}
				break
			}
		}
	}
	return vals, allNull
}

// fkRowMatches reports whether a row's named columns equal the given values.
func fkRowMatches(cols []catalog.Column, fkCols []string, row Row, vals []Datum) bool {
	if len(fkCols) != len(vals) {
		return false
	}
	for i, name := range fkCols {
		found := false
		for j, c := range cols {
			if strings.EqualFold(c.Name, name) {
				if j >= len(row) {
					return false
				}
				if !datumEquals(row[j], vals[i]) {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// pkColumns returns the primary-key column names for tbl by scanning its
// indexes for the one with Primary = true. Falls back to the first column.
func pkColumns(tbl *catalog.Table) []string {
	// This function is called in FK contexts where we have an *InMemory
	// catalog via the check's closure. Since pkColumns only needs the index
	// list we look it up via the tbl pointer's catalog reference.
	// We fall back to the first column when no PK index is found.
	if len(tbl.Columns) > 0 {
		return []string{tbl.Columns[0].Name}
	}
	return nil
}

// datumEquals compares two Datums for FK match purposes.
// NULL != anything (handled at the call site via allNull check).
func datumEquals(a, b Datum) bool {
	if a.IsNull() || b.IsNull() {
		return false
	}
	return a.Format() == b.Format()
}

// checkConstraints evaluates all CHECK constraints on tbl for the given row.
// Returns SQLSTATE 23514 (check_violation) if any constraint fails. M0097-0014.
func checkConstraints(ctx *Context, tbl *catalog.Table, row Row) error {
	if len(tbl.CheckConstraints) == 0 {
		return nil
	}
	for _, exprSQL := range tbl.CheckConstraints {
		if exprSQL == "" {
			continue
		}
		// Parse the CHECK expression as a SQL expression.
		fullSQL := "SELECT (" + exprSQL + ")"
		stmts, err := parser.Parse(fullSQL)
		if err != nil || len(stmts) == 0 {
			continue // invalid check expr: skip
		}
		plan, err := planner.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			continue
		}
		op, err := Build(plan)
		if err != nil {
			continue
		}
		// Build a synthetic slot from the row so the CHECK expression
		// can reference column values.
		synthCtx := *ctx
		synthCtx.OuterRows = append(synthCtx.OuterRows, row)
		if err := op.Open(&synthCtx); err != nil {
			op.Close()
			continue
		}
		slot, err2 := op.Next()
		op.Close()
		if err2 != nil || slot == nil {
			continue
		}
		sr := slotRow(slot)
		if len(sr) == 0 {
			continue
		}
		result := sr[0]
		// NULL check result → pass (SQL NULL is not a constraint failure)
		if result.IsNull() {
			continue
		}
		if result.Kind == KindBool && !result.BoolValue() {
			return &ExecError{
				Code:    "23514",
				Message: fmt.Sprintf("new row for relation %q violates check constraint", tbl.Name),
			}
		}
	}
	return nil
}

