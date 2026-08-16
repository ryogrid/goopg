package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// utilitySettingsOp executes SHOW / SET / RESET statements inside the
// executor path. This is required for multi-statement simple-query batches
// and for extended-query protocol execution, both of which bypass the
// lightweight string-matching handlers in internal/server/query.go.
type utilitySettingsOp struct {
	plan   *optimizer.Utility
	ctx    *Context
	rows   []Row
	rowIdx int
	done   bool
}

func newUtilitySettingsOp(p *optimizer.Utility) *utilitySettingsOp { return &utilitySettingsOp{plan: p} }

func (o *utilitySettingsOp) Schema() optimizer.Schema { return o.plan.Output() }
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
		// "role" — update non-superuser role tracking for privilege checks
		// (e.g. TRUNCATE ownership, M0118-0008), mirroring the string-matching
		// SET ROLE handling in server/query.go for statements that instead
		// reach the executor (multi-statement simple-query batches, the
		// extended-query protocol). M0119-0004.
		if stmt.Name == "role" {
			if o.ctx != nil && o.ctx.SetRole != nil {
				if stmt.Default {
					o.ctx.SetRole("", stmt.Local)
				} else {
					switch strings.ToUpper(stmt.Value) {
					case "", "NONE", "POSTGRES":
						o.ctx.SetRole("", stmt.Local)
					default:
						o.ctx.SetRole(stmt.Value, stmt.Local)
					}
				}
			}
			return nil, EOF
		}
		// "session_authorization" — update non-superuser role tracking for
		// privilege checks (e.g. LEAKPROOF function attribute).
		if stmt.Name == "session_authorization" {
			if o.ctx != nil && o.ctx.SetSessionAuthorization != nil {
				if stmt.Default {
					o.ctx.SetSessionAuthorization("", stmt.Local)
				} else {
					role := stmt.Value
					switch strings.ToUpper(role) {
					case "", "RESET", "POSTGRES":
						o.ctx.SetSessionAuthorization("", stmt.Local)
					default:
						o.ctx.SetSessionAuthorization(role, stmt.Local)
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
		// "role" — restore superuser status (RESET ROLE). M0119-0004.
		if stmt.Name == "role" {
			if o.ctx != nil && o.ctx.SetRole != nil {
				o.ctx.SetRole("", false)
			}
			return nil, EOF
		}
		// "session_authorization" — restore superuser status.
		if stmt.Name == "session_authorization" {
			if o.ctx != nil && o.ctx.SetSessionAuthorization != nil {
				o.ctx.SetSessionAuthorization("", false)
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
	case *parser.DiscardStmt:
		if o.done {
			return nil, EOF
		}
		o.done = true
		// DISCARD SEQUENCES (and DISCARD ALL) clear per-session currval/lastval state.
		if stmt.Mode == "SEQUENCES" || stmt.Mode == "ALL" {
			if o.ctx != nil {
				o.ctx.CurrSeqVals = map[string]int64{}
				o.ctx.LastSeqSet = false
				o.ctx.LastSeqVal = 0
				o.ctx.LastSeqName = ""
			}
		}
		// DISCARD TEMP / TEMPORARY (and DISCARD ALL) drop every temporary
		// relation owned by the calling session. The session's temp namespace
		// (pg_temp_<id>) itself persists — PostgreSQL keeps the namespace for the
		// life of the backend and reuses it. A subsequent cross-session scan of
		// pg_class WHERE relnamespace = pg_my_temp_schema() therefore finds no
		// rows. M0118-0009 (temp-schema-cleanup, design 0118-0091).
		if stmt.Mode == "TEMP" || stmt.Mode == "ALL" {
			if o.ctx != nil {
				if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
					owner := sessionTempOwner(o.ctx)
					// Capture the temp tables' names before dropping them: a temp
					// table's implicit composite rowtype is dropped with it, which
					// cascades to any (possibly non-temp) routine that takes or
					// returns that rowtype — e.g. the temp-schema-cleanup spec's
					// uses_a_temp_type(just_give_me_a_type). PostgreSQL tracks this
					// via pg_depend; goopg matches by the temp table's
					// session-unique name. M0118-0009 (temp-schema-cleanup).
					tempTypeNames := im.SessionTempTableNames(owner)
					im.DropSessionTempObjects(owner)
					if len(tempTypeNames) > 0 {
						if rs := o.ctx.Catalog.Routines(); rs != nil {
							rs.DropRoutinesReferencingTypes(tempTypeNames)
						}
					}
				}
			}
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
			allSettings := o.ctx.AllSettingsDisplay
			if allSettings == nil {
				allSettings = o.ctx.AllSettings
			}
			if allSettings == nil {
				return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SHOW ALL is not supported in this executor context"}
			}
			settings := allSettings()
			o.rows = make([]Row, 0, len(settings))
			for _, kv := range settings {
				o.rows = append(o.rows, Row{NewStringDatum(kv.Name), NewStringDatum(kv.Value)})
			}
		} else {
			getSetting := o.ctx.GetSettingDisplay
			if getSetting == nil {
				getSetting = o.ctx.GetSetting
			}
			if getSetting == nil {
				return nil, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: "SHOW is not supported in this executor context"}
			}
			value, ok := getSetting(stmt.Name)
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