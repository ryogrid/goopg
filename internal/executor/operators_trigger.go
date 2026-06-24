package executor

// operators_trigger.go — trigger firing machinery for DML operators.
//
// fireTriggers is the single entry point used by insertOp, updateOp, and
// deleteOp to invoke BEFORE/AFTER row-level triggers on the target table.
// M0096-0012.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
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
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return newRow, true, nil
	}
	timingLow := strings.ToLower(timing)
	eventLow := strings.ToLower(event)

	for i := range tbl.Triggers {
		trig := &tbl.Triggers[i]
		if !trig.ForEachRow {
			continue // row-level only; statement-level handled by fireStatementTriggers
		}
		if !triggerMatchesEvent(trig, timingLow, eventLow) {
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
// on a table. Used for TRUNCATE triggers. Returns any execution error.
func fireStatementTriggers(ctx *Context, tbl *catalog.Table, timing, event string) error {
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return nil
	}
	timingLow := strings.ToLower(timing)
	eventLow := strings.ToLower(event)
	for i := range tbl.Triggers {
		trig := &tbl.Triggers[i]
		if trig.ForEachRow {
			continue // skip row-level triggers
		}
		if !triggerMatchesEvent(trig, timingLow, eventLow) {
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

// triggerMatchesEvent reports whether trig fires for the given timing+event.
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
