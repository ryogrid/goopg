package executor

// after_trigger.go — the per-query AFTER trigger queue (M0146-0076).
//
// PostgreSQL does not run an AFTER trigger when its event happens. The
// ModifyTable node queues the event (AfterTriggerSaveEvent, trigger.c), and
// AfterTriggerEndQuery fires the query's queued events in queue order once
// the whole query has finished. The queue is per query level: a statement
// run by a trigger or function body opens its own level
// (AfterTriggerBeginQuery), and its events fire when that statement ends.
// A statement-level trigger fires once per query level for each relation
// and command:
//   - BEFORE STATEMENT is skipped when it has already fired
//     (before_stmt_triggers_fired);
//   - AFTER STATEMENT cancels any earlier queued copy and re-queues itself
//     at the tail (cancel_prior_stmt_triggers).
//
// For example, the writable CTE `WITH d AS (DELETE …) INSERT … FROM d`
// fires its triggers in this order:
//   1. the AFTER ROW DELETE and AFTER ROW INSERT events, interleaved;
//   2. AFTER STATEMENT DELETE;
//   3. AFTER STATEMENT INSERT.
//
// goopg opens a level at each statement root:
//   - stmtCTEScopeOp, for statements run by routine bodies and Run;
//   - OpIterator, for the simple-query dispatcher and cursors;
//   - BeginAfterTriggerQuery, for the extended-protocol Execute.
// The DML operators queue into the current level. A DML operator that runs
// with no level open (an internal path that is not a statement root) fires
// its AFTER triggers immediately, which was the behaviour before the queue
// existed.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// afterTriggerEvent is one queued AFTER trigger event. stmt marks a
// statement-level event, which carries no rows.
type afterTriggerEvent struct {
	tbl    *catalog.Table
	event  string
	stmt   bool
	oldRow Row
	newRow Row
	// updCols is an UPDATE event's target column set
	// (updateTargetColumns); nil when unknown or for other events.
	updCols map[string]bool
}

type stmtTriggerKey struct {
	oid   uint32
	name  string
	event string
}

// afterTriggerQuery is one query level's trigger state.
type afterTriggerQuery struct {
	events  []afterTriggerEvent
	bsFired map[stmtTriggerKey]bool
}

func stmtKey(tbl *catalog.Table, event string) stmtTriggerKey {
	return stmtTriggerKey{oid: uint32(tbl.OID), name: tbl.Schema + "." + tbl.Name, event: event}
}

// tableHasTrigger reports whether tbl has a trigger at this timing and level
// for the event.
func tableHasTrigger(tbl *catalog.Table, timing, event string, forEachRow bool) bool {
	if tbl == nil {
		return false
	}
	for i := range tbl.Triggers {
		trig := &tbl.Triggers[i]
		if trig.ForEachRow == forEachRow && triggerMatchesEvent(trig, timing, event) {
			return true
		}
	}
	return false
}

// updateTargetColumns is the set of lower-cased column names an UPDATE
// assigns: the non-nil entries of a SET list parallel to cols. A
// column-specific `UPDATE OF` trigger fires only when the set names one of
// its columns (TriggerEnabled's modifiedCols, trigger.c).
func updateTargetColumns(cols []catalog.Column, sets ...[]optimizer.Expr) map[string]bool {
	out := make(map[string]bool)
	for _, set := range sets {
		for i, e := range set {
			if e != nil && i < len(cols) {
				out[strings.ToLower(cols[i].Name)] = true
			}
		}
	}
	return out
}

// triggerColumnsMatch applies a trigger's `UPDATE OF` column list to an
// UPDATE event's target columns. A nil set means the columns are unknown,
// and the trigger fires.
func triggerColumnsMatch(trig *catalog.Trigger, event string, updCols map[string]bool) bool {
	if updCols == nil || len(trig.UpdateColumns) == 0 || event != "update" {
		return true
	}
	for _, c := range trig.UpdateColumns {
		if updCols[strings.ToLower(c)] {
			return true
		}
	}
	return false
}

// fireBeforeStatementTriggers fires tbl's BEFORE STATEMENT triggers for the
// event, once per query level (ExecBS*Triggers). updCols is the UPDATE
// target column set (nil for other events).
func fireBeforeStatementTriggers(ctx *Context, tbl *catalog.Table, event string, updCols map[string]bool) error {
	if !tableHasTrigger(tbl, "before", event, false) {
		return nil
	}
	if q := ctx.afterTrigQuery; q != nil {
		k := stmtKey(tbl, event)
		if q.bsFired[k] {
			return nil
		}
		if q.bsFired == nil {
			q.bsFired = make(map[stmtTriggerKey]bool)
		}
		q.bsFired[k] = true
	}
	return fireStatementTriggersCols(ctx, tbl, "before", event, updCols)
}

// queueAfterStatementTriggers queues tbl's AFTER STATEMENT triggers for the
// event (ExecAS*Triggers), replacing an earlier queued copy for the same
// relation and command.
func queueAfterStatementTriggers(ctx *Context, tbl *catalog.Table, event string, updCols map[string]bool) error {
	if !tableHasTrigger(tbl, "after", event, false) {
		return nil
	}
	q := ctx.afterTrigQuery
	if q == nil {
		return fireStatementTriggersCols(ctx, tbl, "after", event, updCols)
	}
	k := stmtKey(tbl, event)
	kept := q.events[:0]
	for _, ev := range q.events {
		if ev.stmt && stmtKey(ev.tbl, ev.event) == k {
			continue
		}
		kept = append(kept, ev)
	}
	q.events = append(kept, afterTriggerEvent{tbl: tbl, event: event, stmt: true, updCols: updCols})
	return nil
}

// queueAfterRowTriggers queues tbl's AFTER ROW triggers for one row
// (ExecAR*Triggers). updCols is the UPDATE target column set (nil for
// other events).
func queueAfterRowTriggers(ctx *Context, tbl *catalog.Table, event string, oldRow, newRow Row, updCols map[string]bool) error {
	if !tableHasTrigger(tbl, "after", event, true) {
		return nil
	}
	q := ctx.afterTrigQuery
	if q == nil {
		_, _, err := fireTriggersCols(ctx, tbl, "after", event, oldRow, newRow, updCols)
		return err
	}
	ev := afterTriggerEvent{tbl: tbl, event: strings.ToLower(event), updCols: updCols}
	if oldRow != nil {
		ev.oldRow = cloneRow(oldRow)
	}
	if newRow != nil {
		ev.newRow = cloneRow(newRow)
	}
	q.events = append(q.events, ev)
	return nil
}

// fireAfterTriggerQuery fires q's queued events in queue order
// (AfterTriggerEndQuery). q stays the current level while they run, so a
// trigger body's own statements open levels of their own beneath it.
func fireAfterTriggerQuery(ctx *Context, q *afterTriggerQuery) error {
	for i := 0; i < len(q.events); i++ {
		ev := q.events[i]
		if ev.stmt {
			if err := fireStatementTriggersCols(ctx, ev.tbl, "after", ev.event, ev.updCols); err != nil {
				return err
			}
			continue
		}
		if _, _, err := fireTriggersCols(ctx, ev.tbl, "after", ev.event, ev.oldRow, ev.newRow, ev.updCols); err != nil {
			return err
		}
	}
	q.events = nil
	return nil
}

// stmtAfterTriggers is the query level a statement root owns. swapIn makes
// it the Context's current level for the duration of one Open, Next or Close
// call and swapOut restores the caller's, as stmtCTEScopeOp does with the
// CTE caches. A root that outlives its caller, such as a cursor fetched by
// later statements, therefore never lends its level to them. The pair does
// not allocate, because OpIterator.Next runs it once per result row.
type stmtAfterTriggers struct {
	level   *afterTriggerQuery
	failed  bool
	flushed bool
}

func (s *stmtAfterTriggers) swapIn(ctx *Context) *afterTriggerQuery {
	if ctx == nil {
		return nil
	}
	if s.level == nil {
		s.level = &afterTriggerQuery{}
	}
	saved := ctx.afterTrigQuery
	ctx.afterTrigQuery = s.level
	return saved
}

func (s *stmtAfterTriggers) swapOut(ctx *Context, saved *afterTriggerQuery) {
	if ctx != nil {
		ctx.afterTrigQuery = saved
	}
}

// noteErr records a failed Open or Next: the statement aborts, and PG
// discards its queued events (AfterTriggerAbortQuery semantics at the
// statement's error exit).
func (s *stmtAfterTriggers) noteErr(err error) {
	if err != nil && err != EOF {
		s.failed = true
	}
}

// finish fires the level's queued events once, after the root has closed
// cleanly. It must run between swapIn and swapOut.
func (s *stmtAfterTriggers) finish(ctx *Context, closeErr error) error {
	if closeErr != nil {
		s.failed = true
		return closeErr
	}
	if s.failed || s.flushed || s.level == nil || ctx == nil {
		return nil
	}
	s.flushed = true
	return fireAfterTriggerQuery(ctx, s.level)
}

// BeginAfterTriggerQuery opens a query level on ctx for a statement root
// that is not wrapped in an executor-owned operator (the extended-protocol
// Execute). The returned function closes it: with fire=true it fires the
// queued events and returns the first trigger error, and with fire=false
// it discards them. Only the first call has an effect.
func BeginAfterTriggerQuery(ctx *Context) func(fire bool) error {
	saved := ctx.afterTrigQuery
	q := &afterTriggerQuery{}
	ctx.afterTrigQuery = q
	done := false
	return func(fire bool) error {
		if done {
			return nil
		}
		done = true
		var err error
		if fire {
			err = fireAfterTriggerQuery(ctx, q)
		}
		ctx.afterTrigQuery = saved
		return err
	}
}
