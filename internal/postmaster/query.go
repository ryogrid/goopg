package postmaster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// oidInt4 / oidText are pg_type.oids pinned to upstream's pg_type.dat
// entries (postgres/src/include/catalog/pg_type.dat).
const (
	oidInt4  = 23
	oidText  = 25
	oidBytea = 17
)

// setIsSuperuserGUC keeps the reportable "is_superuser" GUC (see
// isSuperuserRoleName / server.go's startup wiring) in sync whenever
// SET ROLE / SET SESSION AUTHORIZATION / their RESET counterparts flip
// connTx.NonSuperuserRole. Without this, a client that ran e.g. `SET
// ROLE some_role` then re-checked its own privilege level (as some
// tools do) would see a stale "on" from connection startup.
func setIsSuperuserGUC(sess *misc.SessionRegistry, isSuper bool) {
	val := "off"
	if isSuper {
		val = "on"
	}
	_ = sess.SetInternal("is_superuser", val)
}

// applySetSessionAuthorization implements SET/SET LOCAL/RESET SESSION
// AUTHORIZATION on the query.go string-matching fast path — the sibling of
// dispatch.go's ectx.SetSessionAuthorization closure (M0134-0009, both must
// agree: a query routed through either path yields the same session_user()/
// current_user() result). role=="" covers DEFAULT/RESET/no-argument: it
// restores the connect-time login user. A non-empty role sets the session
// user AND clears any active SET ROLE (PG parity: guc.c:4092-4127 — SET
// session_authorization forcibly performs "SET ROLE NONE"/"RESET role" with
// the same context/source, per the SQL spec; NOT miscinit.c
// SetSessionAuthorization, which does not itself clear SetRoleIsActive).
func applySetSessionAuthorization(connTx *connTxState, role string) {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "", "DEFAULT", "RESET":
		role = connTx.LoginUser
	}
	connTx.SessionUser = role
	connTx.SetRoleIsActive = false
	if strings.EqualFold(role, "postgres") {
		connTx.NonSuperuserRole = ""
	} else {
		connTx.NonSuperuserRole = role
	}
}

// applySetRole implements SET/SET LOCAL/RESET ROLE on the query.go
// string-matching fast path — the sibling of dispatch.go's ectx.SetRole
// closure (M0134-0009). Unlike applySetSessionAuthorization, session_user is
// left untouched: SET ROLE only changes the effective role
// (current_user()/current_role), never session_user() (miscinit.c
// GetSessionUserId is unaffected by SET ROLE).
func applySetRole(connTx *connTxState, role string) {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "", "DEFAULT", "NONE":
		// Only clear NonSuperuserRole if a SET ROLE was actually the thing
		// that set it — a bare RESET ROLE with no SET ROLE active (e.g. only
		// SET SESSION AUTHORIZATION ran) must be a no-op, not blindly wipe
		// out the session-authorization role override (PG parity:
		// miscinit.c GetCurrentRoleId's SetRoleIsActive gate).
		if connTx.SetRoleIsActive {
			connTx.NonSuperuserRole = ""
		}
		connTx.SetRoleIsActive = false
	case "POSTGRES":
		// SET ROLE postgres is an explicit role target, NOT a synonym for
		// NONE/DEFAULT (round-2 review R7 — the old code conflated them,
		// which made `SET ROLE postgres` report the session/login user
		// instead of "postgres" for current_user()). NonSuperuserRole stays
		// "" because "postgres" is the bootstrap superuser — the
		// NonSuperuserRole=="" privilege-check convention (is_superuser,
		// LEAKPROOF, …) must still read "superuser" here, per guc.c's
		// GUC_check_errcode for the "role" variable. SetRoleIsActive=true
		// records that a role assignment IS active, so
		// Context.EffectiveUserName can report "postgres" instead of
		// falling back to SessionUser (see its invariant comment).
		connTx.NonSuperuserRole = ""
		connTx.SetRoleIsActive = true
	default:
		connTx.NonSuperuserRole = role
		connTx.SetRoleIsActive = true
	}
}

// handleQuery implements the simple Query path. v0 recognises:
//
//   - SELECT 1                       → single int4 column, value "1"
//   - SHOW name | SHOW ALL           → registry inspection
//   - SET name = value | SET LOCAL …
//   - RESET name | RESET ALL
//   - <empty / whitespace only>      → EmptyQueryResponse
//
// Everything else still returns a feature-not-supported ErrorResponse.
// Each statement is terminated with ReadyForQuery('I') so the client
// can keep going.
func (s *Server) handleQuery(ctx context.Context, r *libpq.FrameReader, w *libpq.FrameWriter, sess *misc.SessionRegistry, payload []byte, connTx *connTxState, prepStmts *preparedStatements) error {
	q, err := extractCString(payload)
	if err != nil {
		return s.writeQueryError(w, errcodes.ProtocolViolation,
			fmt.Sprintf("malformed Query message: %v", err))
	}
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return err
		}
		return w.ReadyForQuery()
	}

	// Per-statement query logging (GOOPG_LOG_STATEMENT). Logged here — the
	// earliest point the full simple-query string is known, before routing to
	// the string-match path, the CREATE/DROP DATABASE|ROLE intercepts, or the
	// executor — mirroring PostgreSQL's exec_simple_query, which logs before
	// parse. No-op when logging is disabled. root-0023.
	stmtStart := time.Now()
	wasLogged := s.logStatement("simple", trimmed, sess, connTx)
	// `log_min_duration_statement` (check_log_duration, postgres.c): timed
	// across every return path below via defer, since handleQuery's
	// string-match fast paths and the executor dispatch each return
	// independently. root-0023 follow-up.
	defer s.logDuration(stmtStart, wasLogged, "simple", trimmed, sess, connTx)

	matchable := strings.TrimRight(trimmed, ";")
	matchable = strings.TrimSpace(matchable)

	// Multi-statement queries (internal ';' after stripping trailing ones)
	// must go through the parser-based executor so each statement is parsed
	// and executed individually. The string-matching path below only handles
	// single statements correctly.
	if strings.ContainsRune(matchable, ';') && s.cfg.hasStorage() {
		return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
	}

	upper := strings.ToUpper(matchable)

	// M0132-S5: the aborted-block gate must sit AHEAD of the string-match fast
	// paths below, not only inside dispatchSimpleQueryViaExecutor's
	// per-statement loop. A constant `SELECT 1` (and `SHOW`/`SET`/`RESET`)
	// never reaches that loop, so a failed block answered them normally while
	// PostgreSQL rejects every statement except the block-ending verbs
	// (M0132-S1 finding (ii), pinned by
	// TestM0132S1_ConstantSelectBypassesTheAbortedBlockGate). The loop's own
	// gate stays: it is the one that covers each statement of a multi-statement
	// message, which this single-statement gate deliberately does not touch.
	if connTx != nil && connTx.IsFailed() && !allowedInAbortedBlock(upper) {
		return s.writeQueryError(w, "25P02", abortedBlockMessage)
	}

	if strings.EqualFold(matchable, "SELECT 1") {
		return s.respondSelectOne(w)
	}

	// A GRANT/REVOKE … ON TYPE|DOMAIN … changes pg_type.typacl, which is
	// heap-backed (PG18-standby basebackup parity, M0097-0022) — unlike the
	// table/sequence/schema/function ACLs the server records virtually below. It
	// must run through the executor (dispatchSimpleQueryViaExecutor at the foot of
	// this function), where an *executor.Context is in scope to re-sync the heap
	// row; execCompatNoop updates the OID-keyed ACL store and rewrites the
	// pg_type row. Exclude it from the server GRANT/REVOKE fast path so it falls
	// through. M0119-0004-ACLHEAP.
	// `GRANT/REVOKE … ON DATABASE …` changes pg_database.datacl, also heap-backed
	// (a SHARED cluster-wide catalog), so it must run through the executor
	// alongside TYPE/DOMAIN rather than the server's own ACL fast path below
	// (which only records the virtually-served relation/schema/function ACLs).
	// "database" is already excluded from that fast path's actual recording via
	// nonTableGrantObjects (grant_ddl.go), but without this the fast path still
	// short-circuits with an empty no-op "GRANT"/"REVOKE" completion before the
	// executor ever sees the statement, so execDatabaseACLChange never ran for a
	// single-statement autocommit GRANT ON DATABASE. M0119-0004-ACLHEAP (datacl
	// half).
	// `GRANT/REVOKE … ON PARAMETER …` changes pg_parameter_acl. It is a
	// goopg-virtual-only catalog (no heap row to re-sync), but like role
	// membership the server's virtual-ACL fast path below has no model for
	// it (tryRecordTableGrant/tryRecordTableRevoke's nonTableGrantObjects
	// already excludes "parameter" from being recorded there, but without
	// this it would still short-circuit to an empty no-op completion before
	// the executor ever sees the statement) — route it to the executor
	// alongside TYPE/DOMAIN/DATABASE instead. M0119-0004-ACLHEAP (parameter
	// ACL half).
	isHeapACLObject := strings.Contains(upper, " ON TYPE ") || strings.Contains(upper, " ON DOMAIN ") ||
		strings.Contains(upper, " ON DATABASE ") || strings.Contains(upper, " ON PARAMETER ")
	// A column-level GRANT/REVOKE — `GRANT <priv>(<cols>) ON [TABLE] <name> …` —
	// changes pg_attribute.attacl, which is heap-backed like pg_type.typacl, so it
	// too must run through the executor (where an *executor.Context re-syncs the heap
	// row). Its signature is a parenthesised column list BEFORE the ON keyword (a
	// function GRANT's parens follow ON), so a '(' earlier than " ON " marks it.
	// M0119-0004-ACLHEAP (attacl half).
	if onPos := strings.Index(upper, " ON "); onPos > 0 {
		if lp := strings.IndexByte(upper, '('); lp >= 0 && lp < onPos {
			isHeapACLObject = true
		}
	}
	// GRANT/REVOKE role membership (`GRANT <role> TO <role>`) has no `ON
	// <object>` clause at all — the discriminator vs. every privilege-GRANT
	// variant above, which all require one. It changes pg_auth_members, which
	// the server's virtual-ACL fast path below does not model
	// (tryRecordTableGrant/tryRecordTableRevoke, grant_ddl.go, both bail
	// immediately on a missing " on "), so it must run through the executor
	// (execCompatNoop → execRoleMembershipChange) where the parser's
	// RoleMembershipChange is available. M0119-0004-ACLHEAP.
	if !strings.Contains(upper, " ON ") {
		isHeapACLObject = true
	}

	// A single-statement, autocommit table-level GRANT is recorded in the
	// catalog ACL store so SET ROLE + a privileged command (e.g. TRUNCATE) is
	// enforced (truncate-conflict isolation spec, M0118-0008). Inside an
	// explicit transaction we fall through to the executor's no-op path so
	// transaction state and the protocol response are handled normally.
	if strings.HasPrefix(upper, "GRANT ") && !isHeapACLObject && (connTx == nil || !connTx.InExplicit()) {
		actingRole := ""
		if connTx != nil {
			actingRole = connTx.NonSuperuserRole
		}
		if err := s.tryRecordTableGrant(matchable, actingRole); err != nil {
			return s.writeQueryError(w, errcodes.FeatureNotSupported, err.Error())
		}
		if err := w.WriteCommandComplete("GRANT"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	}

	// A single-statement, autocommit REVOKE is recorded symmetrically so the
	// materialized relacl drops the revoked privileges and pg_dump re-emits only
	// what remains (DU-002 slice 338). Like GRANT it is left to the executor's
	// no-op path inside an explicit transaction.
	if strings.HasPrefix(upper, "REVOKE ") && !isHeapACLObject && (connTx == nil || !connTx.InExplicit()) {
		s.tryRecordTableRevoke(matchable)
		if err := w.WriteCommandComplete("REVOKE"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	}

	switch {
	case upper == "SHOW ALL":
		return s.handleShowAll(w, sess)
	// SHOW TIME ZONE — PG's dedicated two-word alias for the "timezone" GUC
	// (gram.y:1974 VariableShowStmt). Must be checked before the generic
	// "SHOW " case, which would otherwise mis-read "TIME" as the bare GUC
	// name and fail with `unrecognized configuration parameter "TIME"`.
	// M0134-0028a round 2 (mirrors the parser-level fix in
	// internal/parser/parser.go's parseShow — this fast path never reaches
	// the parser for a single-statement simple-query message).
	case upper == "SHOW TIME ZONE":
		return s.handleShow(w, sess, "timezone")
	case strings.HasPrefix(upper, "SHOW "):
		name := strings.TrimSpace(matchable[len("SHOW "):])
		if strings.EqualFold(name, "ALL") {
			return s.handleShowAll(w, sess)
		}
		return s.handleShow(w, sess, name)
	// SET [LOCAL|SESSION] TRANSACTION <mode> and SET SESSION CHARACTERISTICS AS
	// TRANSACTION <mode> set transaction characteristics (isolation level,
	// read-only), NOT a GUC. They must be routed through the parser-based
	// executor (which builds a SetTransactionStmt) before the generic "SET "
	// case below, otherwise handleSet mis-reads "TRANSACTION" as a GUC name and
	// fails with `unrecognized configuration parameter "TRANSACTION"`. pg_dump's
	// setup_connection issues `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ,
	// READ ONLY`. The "TRANSACTION " trailing space distinguishes this from the
	// transaction_timeout GUC ("SET TRANSACTION_TIMEOUT ...").
	case strings.HasPrefix(upper, "SET TRANSACTION "),
		strings.HasPrefix(upper, "SET LOCAL TRANSACTION "),
		strings.HasPrefix(upper, "SET SESSION TRANSACTION "),
		strings.HasPrefix(upper, "SET SESSION CHARACTERISTICS "):
		if s.cfg.hasStorage() {
			return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
		}
		// No storage backend (bare protocol server): accept as a no-op.
		if err := w.WriteCommandComplete("SET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// SET CONSTRAINTS { ALL | name [, ...] } { DEFERRED | IMMEDIATE } controls
	// runtime constraint deferral, NOT a GUC — route through the parser-based
	// executor (which builds a SetConstraintsStmt and updates the executor
	// session's deferral state) before the generic "SET " GUC case, which would
	// otherwise mis-read "CONSTRAINTS" as a configuration parameter. 0119-0004.
	case strings.HasPrefix(upper, "SET CONSTRAINTS "):
		if s.cfg.hasStorage() {
			return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
		}
		// No storage backend (bare protocol server): accept as a no-op.
		if err := w.WriteCommandComplete("SET CONSTRAINTS"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// SET [LOCAL] TIME ZONE <zone_value> — PG's dedicated two-word alias for
	// the "timezone" GUC (gram.y:1709 set_rest). Must be checked before the
	// generic "SET LOCAL "/"SET " cases below, which would otherwise
	// mis-read "TIME" as the bare GUC name (splitSet stops at the first
	// whitespace) and fail with `unrecognized configuration parameter
	// "TIME"`. Rewrites "TIME ZONE <value>" to "timezone <value>" and hands
	// it to the existing handleSet/splitSet path rather than duplicating
	// its quote-stripping/TO/DEFAULT value parsing — splitSet already
	// canonicalizes DEFAULT to a Reset via SessionRegistry.Set. M0134-0028a
	// round 2 (mirrors the parser-level fix in internal/parser/parser.go's
	// parseSet — this fast path never reaches the parser for a
	// single-statement simple-query message).
	case strings.HasPrefix(upper, "SET LOCAL TIME ZONE "), upper == "SET LOCAL TIME ZONE":
		value := strings.TrimSpace(matchable[len("SET LOCAL TIME ZONE"):])
		return s.handleSet(w, sess, "timezone "+value, true)
	case strings.HasPrefix(upper, "SET TIME ZONE "), upper == "SET TIME ZONE":
		value := strings.TrimSpace(matchable[len("SET TIME ZONE"):])
		return s.handleSet(w, sess, "timezone "+value, false)
	// SET LOCAL SESSION AUTHORIZATION name — must check before generic "SET LOCAL ".
	case strings.HasPrefix(upper, "SET LOCAL SESSION AUTHORIZATION "),
		upper == "SET LOCAL SESSION AUTHORIZATION":
		if connTx != nil {
			if setAuthzGenericSetForm(matchable[len("SET LOCAL SESSION AUTHORIZATION"):]) {
				// SESSION AUTHORIZATION has no generic_set grammar upstream
				// (gram.y:1764/:1774 dedicated productions: bare rolename or
				// DEFAULT only — unlike SET ROLE, which also parses through
				// generic_set because ROLE is an unreserved keyword), so
				// `TO x` / `= x` must reach the real parser for PG's 42601
				// "syntax error at or near ..." instead of being applied as
				// a role name. M0134-0155.
				return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
			}
			role := strings.TrimSpace(matchable[len("SET LOCAL SESSION AUTHORIZATION"):])
			role = strings.Trim(role, `"'`)
			connTx.SnapshotLocalRoleIfNeeded(true)
			applySetSessionAuthorization(connTx, role)
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("SET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// SET LOCAL ROLE rolename — must check before generic "SET LOCAL ", which
	// would otherwise mis-parse "ROLE rolename" as GUC name "role" and fail
	// with "unrecognized configuration parameter" ("role" is not a
	// config.Registry variable — SET ROLE is tracked entirely via
	// connTx.NonSuperuserRole, not the GUC layer). Mirrors the "SET LOCAL
	// SESSION AUTHORIZATION" case above.
	case strings.HasPrefix(upper, "SET LOCAL ROLE "), upper == "SET LOCAL ROLE":
		if connTx != nil {
			role := stripSetToOrEquals(matchable[len("SET LOCAL ROLE"):])
			role = strings.Trim(role, `"'`)
			connTx.SnapshotLocalRoleIfNeeded(true)
			applySetRole(connTx, role)
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("SET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	case strings.HasPrefix(upper, "SET LOCAL "):
		return s.handleSet(w, sess, matchable[len("SET LOCAL "):], true)
	// SET SESSION AUTHORIZATION name — track non-superuser role for privilege checks.
	// Must be checked before the generic "SET " case so splitSet doesn't mis-parse
	// "SESSION AUTHORIZATION name" as parameter "SESSION" with value "AUTHORIZATION name".
	case strings.HasPrefix(upper, "SET SESSION AUTHORIZATION "),
		upper == "SET SESSION AUTHORIZATION":
		if connTx != nil {
			if setAuthzGenericSetForm(matchable[len("SET SESSION AUTHORIZATION"):]) {
				// See the SET LOCAL SESSION AUTHORIZATION case above: no
				// generic_set grammar upstream; parse for the true 42601.
				// M0134-0155.
				return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
			}
			// Extract the role name after "SET SESSION AUTHORIZATION ".
			role := strings.TrimSpace(matchable[len("SET SESSION AUTHORIZATION"):])
			role = strings.Trim(role, `"'`)
			applySetSessionAuthorization(connTx, role)
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("SET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// SET ROLE rolename — track the effective role for privilege checks.
	// Must be before the generic "SET " case so "ROLE" is not passed to handleSet.
	// Like SET SESSION AUTHORIZATION, a non-superuser target populates
	// connTx.NonSuperuserRole so the executor enforces object privileges
	// (e.g. TRUNCATE — truncate-conflict isolation spec, M0118-0008); NONE /
	// DEFAULT / the bootstrap superuser restore full privileges.
	case strings.HasPrefix(upper, "SET ROLE "), upper == "SET ROLE":
		if connTx != nil {
			role := stripSetToOrEquals(matchable[len("SET ROLE"):])
			role = strings.Trim(role, `"'`)
			applySetRole(connTx, role)
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("SET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	case strings.HasPrefix(upper, "SET "):
		return s.handleSet(w, sess, matchable[len("SET "):], false)
	case upper == "RESET ALL":
		sess.ResetAll()
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// RESET SESSION AUTHORIZATION — restore superuser status.
	// Must be checked before the generic "RESET " case so "SESSION AUTHORIZATION"
	// is not used verbatim as a GUC parameter name.
	case upper == "RESET SESSION AUTHORIZATION":
		if connTx != nil {
			applySetSessionAuthorization(connTx, "")
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// RESET ROLE — restore the bootstrap superuser's full privileges.
	case upper == "RESET ROLE":
		if connTx != nil {
			applySetRole(connTx, "")
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	// RESET TIME ZONE — PG's dedicated two-word alias for the "timezone" GUC
	// (gram.y:1904 generic_reset). Must be checked before the generic
	// "RESET " case, which would otherwise take the whole remainder "TIME
	// ZONE" verbatim as the GUC name and fail with `unrecognized
	// configuration parameter "TIME ZONE"`. M0134-0028a round 2 (mirrors
	// the parser-level fix in internal/parser/parser.go's parseReset).
	case upper == "RESET TIME ZONE":
		if err := sess.Reset("timezone"); err != nil {
			return s.writeQueryError(w, errcodes.UndefinedObject, err.Error())
		}
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	case strings.HasPrefix(upper, "RESET "):
		name := strings.TrimSpace(matchable[len("RESET "):])
		if err := sess.Reset(name); err != nil {
			return s.writeQueryError(w, errcodes.UndefinedObject, err.Error())
		}
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.ReadyForQuery()
	}

	if s.cfg.hasStorage() {
		return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, trimmed, connTx, prepStmts)
	}

	return s.writeQueryError(w, errcodes.FeatureNotSupported,
		fmt.Sprintf("query not supported by goopg v0: %q "+
			"(only SELECT 1 / SHOW / SET / RESET are recognised until storage is wired via -D)", trimmed))
}

// handleShow returns the value of one variable as a single text column
// named after the variable. Matches upstream's SHOW behaviour.
func (s *Server) handleShow(w *libpq.FrameWriter, sess *misc.SessionRegistry, name string) error {
	name = strings.Trim(name, " \"'")
	v, eff, ok := sess.GetDisplay(name)
	if !ok {
		return s.writeQueryError(w, errcodes.UndefinedObject,
			fmt.Sprintf("unrecognized configuration parameter %q", name))
	}
	if err := w.WriteRowDescription([]libpq.FieldDescription{{
		Name: v.Name, TableOID: 0, ColumnAttNum: 0,
		TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0,
	}}); err != nil {
		return err
	}
	if err := w.WriteDataRow([][]byte{[]byte(eff)}); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("SHOW"); err != nil {
		return err
	}
	return w.ReadyForQuery()
}

// handleShowAll returns every variable as (name, setting). Upstream also
// emits a description column; we include name + setting only and leave
// description for milestone 5 (catalog) work.
func (s *Server) handleShowAll(w *libpq.FrameWriter, sess *misc.SessionRegistry) error {
	if err := w.WriteRowDescription([]libpq.FieldDescription{
		{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	rows := sess.AllDisplay()
	for _, kv := range rows {
		if err := w.WriteDataRow([][]byte{[]byte(kv.Name), []byte(kv.Value)}); err != nil {
			return err
		}
	}
	if err := w.WriteCommandComplete("SHOW"); err != nil {
		return err
	}
	return w.ReadyForQuery()
}

// gucSetErrorFields unwraps a SET-time GUC validation error, splitting the
// ERROR message from an attached HINT (e.g. an enum GUC's "Available
// values: ..." list — postgres/src/backend/utils/misc/guc.c's PGC_ENUM
// branch of set_config_option attaches this as a separate errhint(), not
// baked into the primary message). Non-ValidationError failures pass their
// Error() text through unchanged with no extra fields.
func gucSetErrorFields(err error) (msg, hint string) {
	var verr *misc.ValidationError
	if errors.As(err, &verr) {
		return verr.Msg, verr.Hint
	}
	return err.Error(), ""
}

// handleSet applies a SET / SET LOCAL statement. Body is the text after
// the keyword: "name = value", "name TO value", or "name value".
func (s *Server) handleSet(w *libpq.FrameWriter, sess *misc.SessionRegistry, body string, isLocal bool) error {
	name, value, ok := splitSet(body)
	if !ok {
		return s.writeQueryError(w, errcodes.SyntaxError,
			fmt.Sprintf("could not parse SET statement: %q", body))
	}
	if err := sess.Set(name, value, isLocal); err != nil {
		msg, hint := gucSetErrorFields(err)
		if hint == "" {
			return s.writeQueryError(w, errcodes.InvalidParameterValue, msg)
		}
		return s.writeQueryError(w, errcodes.InvalidParameterValue, msg, libpq.ErrorField{Code: libpq.FieldHint, Value: hint})
	}
	if err := w.WriteCommandComplete("SET"); err != nil {
		return err
	}
	return w.ReadyForQuery()
}

// stripSetToOrEquals removes an optional "TO " or "=" separator from the
// front of s, mirroring PG's generic_set grammar (gram.y:1656-1693:
// `var_name TO var_list | var_name '=' var_list`, both also accepted for
// `var_name TO DEFAULT`/`var_name '=' DEFAULT`). SET ROLE is matched as a
// fixed-prefix fast path below rather than going through splitSet's generic
// name/value split, and because ROLE is an unreserved keyword upstream it
// ALSO parses through generic_set (gram.y:1754 dedicated bare-form prod +
// generic_set fallback), so the ROLE call sites need this normalization
// applied to their already-prefix-stripped remainder before treating it as
// the target role name. Without this, `SET ROLE TO x` silently stored the
// literal garbage role name "TO x" instead of "x" — a non-empty,
// never-matching NonSuperuserRole that permanently (until the next explicit
// SET/RESET ROLE) denied every CREATEROLE-gated privilege check for the
// *real* superuser session. NOTE: this deliberately does NOT apply to SET
// [LOCAL] SESSION AUTHORIZATION — those have no generic_set grammar upstream
// (see setAuthzGenericSetForm). M0134-0155.
func stripSetToOrEquals(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "=")
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToUpper(s), "TO ") {
		s = strings.TrimSpace(s[3:])
	} else if strings.EqualFold(s, "to") {
		s = ""
	}
	return s
}

// setAuthzGenericSetForm reports whether rest — the text following a
// "SET [LOCAL] SESSION AUTHORIZATION" prefix — begins with one of PG's
// generic_set separators ("=" or TO), i.e. a spelling that looks like
// `SET ... SESSION AUTHORIZATION TO x` / `= x`. Those spellings are syntax
// errors upstream (no generic_set production for SESSION AUTHORIZATION,
// gram.y:1764/:1774; oracle-verified 42601 on PG 18.3), so the wire fast
// paths must not apply them as role names — callers hand such statements to
// the parser-driven path for the genuine error instead. M0134-0155.
func setAuthzGenericSetForm(rest string) bool {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "=") {
		return true
	}
	return len(rest) >= 2 && strings.EqualFold(rest[:2], "to") &&
		(len(rest) == 2 || rest[2] == ' ' || rest[2] == '\t')
}

// splitSet splits "name = value", "name TO value", or "name value" into
// (name, value). Quoted values strip the surrounding quotes; "DEFAULT"
// returns the boot value.
func splitSet(body string) (string, string, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", false
	}
	// Find the first whitespace or '=' after the name.
	end := 0
	for end < len(body) && body[end] != ' ' && body[end] != '\t' && body[end] != '=' {
		end++
	}
	if end == 0 {
		return "", "", false
	}
	name := body[:end]
	rest := strings.TrimSpace(body[end:])
	rest = strings.TrimPrefix(rest, "=")
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(strings.ToUpper(rest), "TO ") {
		rest = strings.TrimSpace(rest[3:])
	}
	if rest == "" {
		return "", "", false
	}
	// Strip outer single quotes (and double them out per SQL spec).
	if strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") && len(rest) >= 2 {
		inner := rest[1 : len(rest)-1]
		inner = strings.ReplaceAll(inner, "''", "'")
		return name, inner, true
	}
	return name, rest, true
}

func (s *Server) respondSelectOne(w *libpq.FrameWriter) error {
	if err := w.WriteRowDescription([]libpq.FieldDescription{{
		Name:         "?column?",
		TableOID:     0,
		ColumnAttNum: 0,
		TypeOID:      oidInt4,
		TypeSize:     4,
		TypeModifier: -1,
		Format:       0, // text
	}}); err != nil {
		return err
	}
	if err := w.WriteDataRow([][]byte{[]byte("1")}); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("SELECT 1"); err != nil {
		return err
	}
	return w.ReadyForQuery()
}

// errQueryErrorSent is a sentinel returned by writeQueryError when the
// ErrorResponse + ReadyForQuery pair was successfully written to the client.
// Callers that loop over statements (e.g. dispatchSimpleQueryViaExecutor)
// MUST check for this sentinel and NOT send an additional ReadyForQuery.
// The runPostStartupLoop MUST treat this as a "keep-going" signal (the
// client received a clean error response and is ready for the next query).
var errQueryErrorSent = errors.New("server: error response sent to client")

// writeQueryError emits an ErrorResponse with the given SQLSTATE plus a
// trailing ReadyForQuery, matching how upstream finishes a failed simple
// Query (the parse error is reported and the connection stays open).
// extra fields (e.g. FieldPosition) are appended after the standard set.
//
// Returns errQueryErrorSent (not nil) on success so that callers in a
// multi-statement loop can detect the error-and-stop condition without
// sending a duplicate ReadyForQuery (M0097-0003 normalization fix).
func (s *Server) writeQueryError(w *libpq.FrameWriter, code errcodes.Code, msg string, extra ...libpq.ErrorField) error {
	fields := []libpq.ErrorField{
		{Code: libpq.FieldSeverity, Value: "ERROR"},
		{Code: libpq.FieldSeverityNonLocal, Value: "ERROR"},
		{Code: libpq.FieldSQLState, Value: string(code)},
		{Code: libpq.FieldMessage, Value: msg},
		{Code: libpq.FieldRoutine, Value: "postmaster.handleQuery"},
	}
	fields = append(fields, extra...)
	if err := w.WriteErrorResponse(fields); err != nil {
		return err
	}
	// afterError: an explicit transaction block that is erroring right now
	// reports 'E', even though dispatch marks connTxState failed only after
	// this returns errQueryErrorSent. See connTxState.wireStatus.
	if err := w.ReadyForQueryAfterError(); err != nil {
		return err
	}
	return errQueryErrorSent
}

// extractCString returns the C string at the start of buf (everything up to
// the first NUL). The buf is required to end in a NUL; the bytes after that
// NUL are ignored, matching upstream's exec_simple_query which only looks
// at the leading string.
func extractCString(buf []byte) (string, error) {
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return "", errors.New("missing NUL terminator")
}
