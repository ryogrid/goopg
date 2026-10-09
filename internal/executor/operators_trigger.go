package executor

// operators_trigger.go — trigger firing machinery for DML operators.
//
// fireTriggers is the single entry point used by insertOp, updateOp, and
// deleteOp to invoke BEFORE/AFTER row-level triggers on the target table.
// M0096-0012.

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// fireTriggers fires all matching triggers on tbl and returns the
// (possibly modified) row that DML should use, or (nil, false) to
// indicate the row should be skipped (BEFORE trigger returned NULL).
//
//   timing   "before" or "after"
//   event    "insert", "update", or "delete"
//   oldRow   the old row (for DELETE / UPDATE), nil for INSERT
//   newRow   the new row (for INSERT / UPDATE), nil for DELETE
//
// For BEFORE row triggers the returned Row replaces the original new
// row for INSERT/UPDATE, or the original old row for DELETE (BEFORE
// DELETE can suppress deletion by returning NULL).
// For AFTER triggers the return value is always ignored (pass-through).
//
// A non-nil error means a trigger body raised an exception (e.g. plpgsql
// RAISE) or hit a runtime error; callers MUST propagate it to abort the DML,
// exactly as PostgreSQL aborts the statement when a trigger errors. M0096-0012;
// error propagation M0118-0009 (design 0118-0097).
func fireTriggers(ctx *Context, tbl *catalog.Table, timing, event string, oldRow, newRow Row) (Row, bool, error) {
	return fireTriggersCols(ctx, tbl, timing, event, oldRow, newRow, nil)
}

// fireTriggersCols is fireTriggers for an UPDATE whose target columns are
// known: a column-specific `UPDATE OF` trigger is skipped unless updCols
// names one of its columns (TriggerEnabled, trigger.c; M0146-0076).
func fireTriggersCols(ctx *Context, tbl *catalog.Table, timing, event string, oldRow, newRow Row, updCols map[string]bool) (Row, bool, error) {
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return newRow, true, nil
	}
	timingLow := strings.ToLower(timing)
	eventLow := strings.ToLower(event)

	for _, i := range triggerFireOrder(tbl) {
		trig := &tbl.Triggers[i]
		if !trig.ForEachRow {
			continue // row-level only; statement-level handled by fireStatementTriggers
		}
		if !triggerMatchesEvent(trig, timingLow, eventLow) || !triggerColumnsMatch(trig, eventLow, updCols) {
			continue
		}
		if !triggerModeFires(ctx, trig.Enabled) {
			continue
		}
		if pass, err := triggerWhenPasses(ctx, tbl, trig, oldRow, newRow); err != nil {
			return nil, false, err
		} else if !pass {
			continue
		}
		r := lookupTriggerRoutine(ctx, trig)
		if r == nil {
			continue // trigger function not found — skip
		}
		trigCtx := &plpgsqlTrigCtx{
			OldRow:  oldRow,
			NewRow:  newRow,
			Cols:    tbl.Columns,
			TGName:  trig.Name,
			TGWhen:  strings.ToUpper(timing),  // PostgreSQL uses uppercase BEFORE/AFTER
			TGOp:    strings.ToUpper(event),   // PostgreSQL uses uppercase INSERT/UPDATE/DELETE
			TGLevel: "ROW",                     // PostgreSQL uses uppercase ROW/STATEMENT
			TGTable: tbl.Name,
			TGArgs:  trig.Args,
		}
		retRow, ok, err := executePLpgSQLTriggerBody(r, trigCtx, ctx)
		if err != nil {
			// A trigger body raised an exception or hit a runtime error;
			// propagate so the caller aborts the DML (PostgreSQL semantics).
			return nil, false, err
		}
		if !ok {
			continue
		}
		if timingLow == "before" {
			if retRow == nil {
				return nil, false, nil // RETURN NULL suppresses the row
			}
			// BEFORE INSERT/UPDATE: use the returned row.
			if eventLow == "insert" || eventLow == "update" {
				newRow = retRow
			}
			// BEFORE DELETE: if trigger returned OLD (non-nil), proceed.
		}
	}
	return newRow, true, nil
}

// fireStatementTriggers fires FOR EACH STATEMENT triggers for the given event
// on a table. Returns any execution error.
func fireStatementTriggers(ctx *Context, tbl *catalog.Table, timing, event string) error {
	return fireStatementTriggersCols(ctx, tbl, timing, event, nil)
}

// fireStatementTriggersCols is fireStatementTriggers for an UPDATE whose
// target columns are known: a column-specific `UPDATE OF` trigger is skipped
// unless updCols names one of its columns (M0146-0076).
func fireStatementTriggersCols(ctx *Context, tbl *catalog.Table, timing, event string, updCols map[string]bool) error {
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return nil
	}
	timingLow := strings.ToLower(timing)
	eventLow := strings.ToLower(event)
	for _, i := range triggerFireOrder(tbl) {
		trig := &tbl.Triggers[i]
		if trig.ForEachRow {
			continue // skip row-level triggers
		}
		if !triggerMatchesEvent(trig, timingLow, eventLow) || !triggerColumnsMatch(trig, eventLow, updCols) {
			continue
		}
		if !triggerModeFires(ctx, trig.Enabled) {
			continue
		}
		// goopg does not materialise transition tables (REFERENCING OLD /
		// NEW TABLE), so a body reading one would fail and abort the DML.
		// Such a trigger stays unfired, as every statement trigger was
		// before M0146-0076 (deferral ledger 2026-10-06).
		if trig.OldTransitionTable != "" || trig.NewTransitionTable != "" {
			continue
		}
		if pass, err := triggerWhenPasses(ctx, tbl, trig, nil, nil); err != nil {
			return err
		} else if !pass {
			continue
		}
		r := lookupTriggerRoutine(ctx, trig)
		if r == nil {
			continue
		}
		trigCtx := &plpgsqlTrigCtx{
			OldRow:  nil,
			NewRow:  nil,
			Cols:    tbl.Columns,
			TGName:  trig.Name,
			TGWhen:  strings.ToUpper(timing),
			TGOp:    strings.ToUpper(event),
			TGLevel: "STATEMENT",
			TGTable: tbl.Name,
			TGArgs:  trig.Args,
		}
		_, _, err := executePLpgSQLTriggerBody(r, trigCtx, ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// triggerWhenPasses evaluates trig's WHEN condition against the event's
// OLD and NEW rows (TriggerEnabled, trigger.c; M0146-0077). A trigger
// without one always passes; a NULL result does not pass. OLD.col / NEW.col
// become literals of the column's type and OLD / OLD.* / NEW / NEW.* become
// ROW(...) of them, the same binding a PL/pgSQL variable gets
// (rewriteParserExpr), so the condition is planned as an ordinary
// expression. A row the event lacks binds as NULL: CREATE TRIGGER rejects a
// WHEN that names it, so only a statement-level WHEN, which names neither,
// reaches here with both rows nil.
//
// PG checks an AFTER ROW trigger's WHEN when the event is queued
// (AfterTriggerSaveEvent). goopg checks it when the queued event fires. The
// rows are the same at both points, so only the timing of a volatile
// function inside WHEN differs (deferral ledger 2026-10-07).
func triggerWhenPasses(ctx *Context, tbl *catalog.Table, trig *catalog.Trigger, oldRow, newRow Row) (bool, error) {
	if trig.WhenExpr == nil {
		return true, nil
	}
	rowOf := func(name string) (Row, bool) {
		switch strings.ToLower(name) {
		case "old":
			return oldRow, true
		case "new":
			return newRow, true
		}
		return nil, false
	}
	rowLiteral := func(pos int, row Row) parser.Expr {
		var elems []parser.Expr
		for i, col := range tbl.Columns {
			if col.Dropped {
				continue
			}
			d := NullDatum
			if i < len(row) {
				d = row[i]
			}
			elems = append(elems, typedDatumLiteral(pos, d, col.Type))
		}
		return parser.NewRowExpr(pos, elems)
	}
	bound := rewriteParserExpr(trig.WhenExpr, func(x parser.Expr) (parser.Expr, bool) {
		switch r := x.(type) {
		case *parser.ColumnRef:
			if r.Schema != "" {
				return nil, false
			}
			if r.Table == "" {
				if row, ok := rowOf(r.Column); ok {
					return rowLiteral(r.Pos(), row), true
				}
				return nil, false
			}
			row, ok := rowOf(r.Table)
			if !ok {
				return nil, false
			}
			if r.Column == "*" {
				return rowLiteral(r.Pos(), row), true
			}
			// System column: tableoid is the trigger's own relation.
			// PG also exposes ctid / xmin / … of OLD (and of NEW in an
			// AFTER trigger), which goopg's rows do not carry; such a
			// reference is left unbound and fails to plan (ledgered).
			if strings.EqualFold(r.Column, "tableoid") {
				return typedDatumLiteral(r.Pos(), NewIntDatum(int64(tbl.OID)), catalog.Type{Name: "oid"}), true
			}
			for i, col := range tbl.Columns {
				if !col.Dropped && strings.EqualFold(col.Name, r.Column) {
					d := NullDatum
					if i < len(row) {
						d = row[i]
					}
					return typedDatumLiteral(r.Pos(), d, col.Type), true
				}
			}
		case *parser.StarExpr:
			if r.Schema == "" {
				if row, ok := rowOf(r.Table); ok {
					return rowLiteral(r.Pos(), row), true
				}
			}
		}
		return nil, false
	})
	d, err := evalExprViaSQL(bound, ctx)
	if err != nil {
		return false, err
	}
	return !d.IsNull() && d.Kind == KindBool && d.BoolValue(), nil
}

// triggerFireOrder returns the indices of tbl.Triggers in name order, the
// order PG fires triggers for the same event (RelationBuildTriggers reads
// pg_trigger through its relid+name index; names compare bytewise, as
// strcmp). tbl.Triggers is kept in creation order (M0146-0076).
func triggerFireOrder(tbl *catalog.Table) []int {
	order := make([]int, len(tbl.Triggers))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return tbl.Triggers[order[a]].Name < tbl.Triggers[order[b]].Name
	})
	return order
}

// triggerMatchesEvent reports whether trig fires for the given timing+event.
// triggerModeFires is TriggerEnabled's tgenabled test (trigger.c): in the
// replica session_replication_role only 'R' and 'A' triggers fire; in origin
// and local only 'O' and 'A'; a 'D' trigger never fires. The RI checks
// (catalog.ForeignKey.CheckTrigEnabled / ActionTrigEnabled) take the same
// test, as PG's RI triggers do (M0146-0082).
func triggerModeFires(ctx *Context, code byte) bool {
	mode := catalog.TriggerFireMode(code)
	if mode == 'D' {
		return false
	}
	replica := false
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("session_replication_role"); ok {
			replica = strings.EqualFold(v, "replica")
		}
	}
	if replica {
		return mode == 'R' || mode == 'A'
	}
	return mode == 'O' || mode == 'A'
}

func triggerMatchesEvent(trig *catalog.Trigger, timing, event string) bool {
	switch strings.ToLower(timing) {
	case "before":
		if trig.Timing != catalog.TriggerBefore {
			return false
		}
	case "after":
		if trig.Timing != catalog.TriggerAfter {
			return false
		}
	default:
		return false
	}
	for _, e := range trig.Events {
		if strings.EqualFold(e, event) {
			return true
		}
	}
	return false
}

// lookupTriggerRoutine finds the trigger function in the routine registry.
func lookupTriggerRoutine(ctx *Context, trig *catalog.Trigger) *catalog.Routine {
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return nil
	}
	name := parseRoutineName(trig.FuncName)
	if trig.FuncSchema != "" {
		name.Schema = trig.FuncSchema
	}
	candidates := rs.LookupByName(name)
	if len(candidates) == 0 {
		return nil
	}
	return candidates[0]
}
