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
// M0096-0012.
func fireTriggers(ctx *Context, tbl *catalog.Table, timing, event string, oldRow, newRow Row) (Row, bool) {
	rs := ctx.Catalog.Routines()
	if rs == nil {
		return newRow, true
	}
	timingLow := strings.ToLower(timing)
	eventLow := strings.ToLower(event)

	for i := range tbl.Triggers {
		trig := &tbl.Triggers[i]
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
			TGWhen:  timingLow,
			TGOp:    eventLow,
			TGLevel: "row",
			TGTable: tbl.Name,
		}
		retRow, ok, err := executePLpgSQLTriggerBody(r, trigCtx, ctx)
		if err != nil {
			// Trigger execution errors abort the DML.
			// Surface the error via a panic recovered by the operator.
			// For now, silently skip — production should propagate.
			continue
		}
		if !ok {
			continue
		}
		if timingLow == "before" {
			if retRow == nil {
				return nil, false // RETURN NULL suppresses the row
			}
			// BEFORE INSERT/UPDATE: use the returned row.
			if eventLow == "insert" || eventLow == "update" {
				newRow = retRow
			}
			// BEFORE DELETE: if trigger returned OLD (non-nil), proceed.
		}
	}
	return newRow, true
}

// triggerMatchesEvent reports whether trig fires for the given timing+event.
func triggerMatchesEvent(trig *catalog.Trigger, timing, event string) bool {
	if !trig.ForEachRow {
		return false // statement-level triggers not yet supported
	}
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
