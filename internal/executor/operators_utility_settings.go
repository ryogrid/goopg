package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// utilitySettingsOp executes SHOW / SET / RESET statements inside the
// executor path. This is required for multi-statement simple-query batches
// and for extended-query protocol execution, both of which bypass the
// lightweight string-matching handlers in internal/server/query.go.
type utilitySettingsOp struct {
	plan   *planner.Utility
	ctx    *Context
	rows   []Row
	rowIdx int
	done   bool
}

func newUtilitySettingsOp(p *planner.Utility) *utilitySettingsOp { return &utilitySettingsOp{plan: p} }

func (o *utilitySettingsOp) Schema() planner.Schema { return o.plan.Output() }
func (o *utilitySettingsOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}
func (o *utilitySettingsOp) Close() error { return nil }

func (o *utilitySettingsOp) Next() (TupleSlot, error) {
	switch stmt := o.plan.Stmt.(type) {
	case *parser.ShowStmt:
		return o.nextShow(stmt)
	case *parser.SetStmt:
		if o.done {
			return nil, EOF
		}
		o.done = true
		if o.ctx == nil {
			return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SET is not supported in this executor context"}
		}
		// "role" — no-op: goopg has no role management.
		if stmt.Name == "role" {
			return nil, EOF
		}
		// "session_authorization" — update non-superuser role tracking for
		// privilege checks (e.g. LEAKPROOF function attribute).
		if stmt.Name == "session_authorization" {
			if o.ctx != nil && o.ctx.SetSessionAuthorization != nil {
				if stmt.Default {
					o.ctx.SetSessionAuthorization("")
				} else {
					role := stmt.Value
					switch strings.ToUpper(role) {
					case "", "RESET", "POSTGRES":
						o.ctx.SetSessionAuthorization("")
					default:
						o.ctx.SetSessionAuthorization(role)
					}
				}
			}
			return nil, EOF
		}
		if stmt.Default {
			if o.ctx.ResetSetting == nil {
				return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "RESET is not supported in this executor context"}
			}
			if err := o.ctx.ResetSetting(stmt.Name); err != nil {
				return nil, &ExecError{Code: "22023", Pos: stmt.Pos(), Message: err.Error()}
			}
			return nil, EOF
		}
		if o.ctx.SetSetting == nil {
			return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SET is not supported in this executor context"}
		}
		if err := o.ctx.SetSetting(stmt.Name, stmt.Value, stmt.Local); err != nil {
			return nil, &ExecError{Code: "22023", Pos: stmt.Pos(), Message: err.Error()}
		}
		return nil, EOF
	case *parser.ResetStmt:
		if o.done {
			return nil, EOF
		}
		o.done = true
		if o.ctx == nil {
			return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "RESET is not supported in this executor context"}
		}
		if stmt.All {
			if o.ctx.ResetAllSettings != nil {
				o.ctx.ResetAllSettings()
			}
			return nil, EOF
		}
		// "role" — no-op: goopg has no role management.
		if stmt.Name == "role" {
			return nil, EOF
		}
		// "session_authorization" — restore superuser status.
		if stmt.Name == "session_authorization" {
			if o.ctx != nil && o.ctx.SetSessionAuthorization != nil {
				o.ctx.SetSessionAuthorization("")
			}
			return nil, EOF
		}
		if o.ctx.ResetSetting == nil {
			return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "RESET is not supported in this executor context"}
		}
		if err := o.ctx.ResetSetting(stmt.Name); err != nil {
			return nil, &ExecError{Code: "42704", Pos: stmt.Pos(), Message: err.Error()}
		}
		return nil, EOF
	default:
		if o.done {
			return nil, EOF
		}
		o.done = true
		return nil, EOF
	}
}

func (o *utilitySettingsOp) nextShow(stmt *parser.ShowStmt) (TupleSlot, error) {
	if o.rows == nil {
		if o.ctx == nil {
			return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SHOW is not supported in this executor context"}
		}
		if stmt.All {
			if o.ctx.AllSettings == nil {
				return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SHOW ALL is not supported in this executor context"}
			}
			settings := o.ctx.AllSettings()
			o.rows = make([]Row, 0, len(settings))
			for _, kv := range settings {
				o.rows = append(o.rows, Row{NewStringDatum(kv.Name), NewStringDatum(kv.Value)})
			}
		} else {
			if o.ctx.GetSetting == nil {
				return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SHOW is not supported in this executor context"}
			}
			value, ok := o.ctx.GetSetting(stmt.Name)
			if !ok {
				return nil, &ExecError{Code: "42704", Pos: stmt.Pos(), Message: fmt.Sprintf("unrecognized configuration parameter %q", stmt.Name)}
			}
			o.rows = []Row{{NewStringDatum(value)}}
		}
	}
	if o.rowIdx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.rowIdx]
	o.rowIdx++
	return SlotFromRow(o.plan.Output(), row), nil
}