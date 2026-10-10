package executor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/misc"
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

// SetLocalOutsideBlockMessage is WarnNoTransactionBlock's 25P01 text for SET
// LOCAL (xact.c CheckTransactionBlock: "%s can only be used in transaction
// blocks"); the wire fast paths raise the same warning.
const SetLocalOutsideBlockMessage = "SET LOCAL can only be used in transaction blocks"

// execErrorFromGUCError wraps a SET-time validation error for the wire
// protocol, preserving the HINT PostgreSQL attaches to some GUC failures
// (e.g. an enum's "Available values: ..." list) instead of collapsing it
// into the bare ERROR message.
func execErrorFromGUCError(pos int, err error) *ExecError {
	var verr *misc.ValidationError
	if errors.As(err, &verr) {
		return &ExecError{Code: "22023", Pos: pos, Message: verr.Msg, Hint: verr.Hint}
	}
	return &ExecError{Code: "22023", Pos: pos, Message: err.Error()}
}

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
		// ExecSetVariableStmt: WarnNoTransactionBlock(isTopLevel, "SET
		// LOCAL") before anything else. The value is still applied; the
		// surrounding transaction's end discards it.
		if stmt.Local && o.ctx.InTransactionBlock != nil && !o.ctx.InTransactionBlock() {
			o.ctx.AddWarningWithHint("25P01", SetLocalOutsideBlockMessage, "")
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
					case "", "NONE":
						o.ctx.SetRole("", stmt.Local)
					default:
						// "postgres" is NOT collapsed to "" here — it is an
						// explicit role target, not a NONE/DEFAULT synonym
						// (round-2 review R6/R7); ctx.SetRole's own switch
						// distinguishes it from a genuine reset.
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
					case "", "RESET":
						o.ctx.SetSessionAuthorization("", stmt.Local)
					default:
						// "postgres" is NOT collapsed to "" here — see the
						// SET ROLE case above (round-2 review R6):
						// SetSessionAuthorization's own switch already
						// distinguishes an explicit "postgres" target from
						// DEFAULT/RESET (it sets SessionUser="postgres"
						// rather than restoring LoginUser).
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
		value := stmt.Value
		if stmt.Args != nil {
			v, err := misc.FlattenSetArgs(stmt.Name, stmt.Args)
			if err != nil {
				return nil, execErrorFromGUCError(stmt.Pos(), err)
			}
			value = v
		}
		if err := o.ctx.SetSetting(stmt.Name, value, stmt.Local); err != nil {
			return nil, execErrorFromGUCError(stmt.Pos(), err)
		}
		return nil, EOF
	case *parser.AlterSystemStmt:
		if o.done {
			return nil, EOF
		}
		o.done = true
		return nil, o.execAlterSystem(stmt)
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
		// DISCARD ALL (discard.c DiscardAll): PreventInTransactionBlock, then
		// SET SESSION AUTHORIZATION DEFAULT (which also resets the role),
		// RESET ALL and the release of every session advisory lock. The
		// connection-held state (prepared statements, cursors, LISTEN) is
		// dropped by the postmaster before this runs; sequences and temp
		// tables follow below, shared with DISCARD SEQUENCES / TEMP.
		if stmt.Mode == "ALL" {
			if o.ctx != nil && o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
				return nil, &ExecError{Code: "25001", Message: "DISCARD ALL cannot run inside a transaction block"}
			}
			if o.ctx != nil {
				if o.ctx.SetSessionAuthorization != nil {
					o.ctx.SetSessionAuthorization("", false)
				}
				if o.ctx.SetRole != nil {
					o.ctx.SetRole("", false)
				}
				if o.ctx.ResetAllSettings != nil {
					o.ctx.ResetAllSettings()
				}
				_, _ = evalAdvisoryUnlockAll(o.ctx)
			}
		}
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
			descs := o.gucShortDescriptions()
			o.rows = make([]Row, 0, len(settings))
			for _, kv := range settings {
				// PG's SHOW ALL is `SELECT name, setting, short_desc FROM
				// pg_settings` — three columns. goopg emitted only the first
				// two, so any client reading the third by index (or by name)
				// broke (review/260831-2 EO2-8). The description comes from the
				// same pg_settings rows the catalog already serves; GUCs that
				// view does not carry yet get an empty description rather than
				// invented text.
				o.rows = append(o.rows, Row{
					NewStringDatum(kv.Name),
					NewStringDatum(kv.Value),
					NewStringDatum(descs[strings.ToLower(kv.Name)]),
				})
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

// gucShortDescriptions maps GUC name -> pg_settings.short_desc, the text PG's
// SHOW ALL prints in its third column. Returns an empty (non-nil) map when the
// catalog has no pg_settings view, so callers can index it unconditionally.
func (o *utilitySettingsOp) gucShortDescriptions() map[string]string {
	out := map[string]string{}
	if o.ctx == nil || o.ctx.Catalog == nil {
		return out
	}
	tbl, ok := o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_settings"})
	if !ok || tbl == nil || tbl.VirtualRows == nil {
		return out
	}
	const shortDescCol = 4 // pg_settings column ordinal
	for _, r := range tbl.VirtualRows() {
		if len(r) > shortDescCol {
			out[strings.ToLower(r[0])] = r[shortDescCol]
		}
	}
	return out
}

// execAlterSystem runs ALTER SYSTEM (utility.c T_AlterSystemStmt +
// guc.c AlterSystemSetConfigFile). PreventInTransactionBlock, then the
// permission check — superuser, or else the ALTER SYSTEM privilege on the
// parameter (pg_parameter_aclcheck), never for RESET ALL — then the
// validation and file rewrite behind ctx.AlterSystem. The new value takes
// effect on the next pg_reload_conf() or restart, as in PostgreSQL.
func (o *utilitySettingsOp) execAlterSystem(stmt *parser.AlterSystemStmt) error {
	if o.ctx == nil || o.ctx.AlterSystem == nil {
		return &ExecError{Code: "0A000", Message: "ALTER SYSTEM is not supported in this context"}
	}
	if o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
		return &ExecError{Code: "25001", Message: "ALTER SYSTEM cannot run inside a transaction block"}
	}
	name := strings.ToLower(stmt.Name)
	// ExtractSetVariableArgs runs before the permission check upstream.
	value := stmt.Value
	if stmt.Args != nil && !stmt.Reset && !stmt.ResetAll {
		v, err := misc.FlattenSetArgs(name, stmt.Args)
		if err != nil {
			return execErrorFromGUCError(0, err)
		}
		value = v
	}
	if role := o.ctx.NonSuperuserRole; role != "" {
		if stmt.ResetAll {
			return &ExecError{Code: "42501", Message: "permission denied to perform ALTER SYSTEM RESET ALL"}
		}
		if !parameterACLGrants(o.ctx, name, role, 'A') {
			return &ExecError{Code: "42501", Message: fmt.Sprintf("permission denied to set parameter %q", name)}
		}
	}
	set := !stmt.Reset && !stmt.Default
	if err := o.ctx.AlterSystem(name, value, set, stmt.ResetAll); err != nil {
		var aerr *misc.AlterSystemError
		if errors.As(err, &aerr) {
			return &ExecError{Code: aerr.Code, Message: aerr.Msg, Hint: aerr.Hint}
		}
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	return nil
}

// parameterACLGrants reports whether the parameter's pg_parameter_acl entry
// grants privilege (an aclitem letter, e.g. 'A' for ALTER SYSTEM) to role or
// to PUBLIC. A parameter with no entry grants nothing to a non-superuser,
// which is upstream's default (pg_parameter_aclmask with no row).
func parameterACLGrants(ctx *Context, name, role string, privilege byte) bool {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return false
	}
	oid := im.ParameterACLOID(name)
	if oid == 0 {
		return false
	}
	text := strings.Trim(im.ParameterACLText(oid), "{}")
	for _, item := range strings.Split(text, ",") {
		eq := strings.IndexByte(item, '=')
		if eq < 0 {
			continue
		}
		grantee := strings.Trim(item[:eq], `"`)
		privs := item[eq+1:]
		if slash := strings.IndexByte(privs, '/'); slash >= 0 {
			privs = privs[:slash]
		}
		if (grantee == "" || strings.EqualFold(grantee, role)) && strings.IndexByte(privs, privilege) >= 0 {
			return true
		}
	}
	return false
}

// flattenFunctionConfigOps prepares CREATE/ALTER FUNCTION SET / RESET
// clauses for proconfig the way functioncmds.c update_proconfig_value does:
// each SET value list goes through flatten_set_variable_args
// (ExtractSetVariableArgs), and every named entry through GUCArrayAdd /
// GUCArrayDelete's validation and name normalisation (misc.ConfigArrayItem)
// — so proconfig holds PostgreSQL's text (`DateStyle=iso, mdy`) and a bad
// name or value fails the statement. It returns a copy; the parsed
// statement is left untouched.
func flattenFunctionConfigOps(pos int, ops []parser.FunctionConfigOp) ([]parser.FunctionConfigOp, error) {
	if len(ops) == 0 {
		return ops, nil
	}
	out := append([]parser.FunctionConfigOp(nil), ops...)
	for i := range out {
		op := &out[i]
		if op.ResetAll {
			continue
		}
		var value *string
		if !op.Reset {
			if op.Args != nil {
				v, err := misc.FlattenSetArgs(op.Name, op.Args)
				if err != nil {
					return nil, execErrorFromGUCError(pos, err)
				}
				op.Value = v
			}
			value = &op.Value
		}
		name, err := misc.ConfigArrayItem(op.Name, value)
		if err != nil {
			var aerr *misc.AlterSystemError
			if errors.As(err, &aerr) {
				return nil, &ExecError{Code: aerr.Code, Pos: pos, Message: aerr.Msg, Hint: aerr.Hint}
			}
			return nil, execErrorFromGUCError(pos, err)
		}
		op.Name = name
	}
	return out, nil
}
