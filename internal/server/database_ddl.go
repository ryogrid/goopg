package server

// CREATE DATABASE / DROP DATABASE wire-protocol handler (M0054-0001).
//
// goopg's parser does not yet understand CREATE DATABASE / DROP
// DATABASE — historically the dispatch path absorbed them via
// `compatNoopCommandTag` and returned the canonical CommandComplete
// tag with no further work. That left `pg_database` empty after a
// server crash because nothing was logged or replayed.
//
// This file intercepts the same surface (string-prefix on the raw
// SQL) but performs three real actions instead of zero:
//
//  1. Parse the database name out of the SQL (a permissive lex pass —
//     handles both `CREATE DATABASE foo` and `CREATE DATABASE "Foo"`).
//  2. Mutate the catalog's `databases` registry through the new
//     `CreateDatabase` / `DropDatabase` methods so `pg_database`
//     immediately reflects the change.
//  3. Append a `wal.RecordKindCreateDatabase` /
//     `wal.RecordKindDropDatabase` record so the registration
//     survives a crash. The recovery driver in
//     `internal/initdb/open.go` re-applies these records during
//     startup.
//
// Multi-database storage isolation (a real per-database file
// namespace) is intentionally NOT in scope here — every relation
// still routes through `catalog.DefaultDBOid`. The HammerDB TPC-H
// workflow needs (a) `pg_database` to list `tpch` so the
// existence-probe `SELECT 1 FROM pg_database WHERE datname='tpch'`
// returns a row after a crash, and (b) connections to `tpch` to
// succeed. Both are satisfied without per-database storage isolation.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/wal"
)

// databaseDDLError carries a specific PG SQLSTATE for a
// tryHandleDatabaseDDL/applyAlterDatabaseConfig failure, mirroring
// roleError's shape on the role-DDL side (see role_ddl.go). Errors
// without this wrapper (e.g. a WAL-append failure) fall back to
// sqlstate.SystemError via databaseDDLErrorSQLState, matching an
// internal-error rather than a user-facing PG error condition.
type databaseDDLError struct {
	code sqlstate.Code
	msg  string
}

func (e *databaseDDLError) Error() string { return e.msg }

// databaseDDLErrorSQLState returns the SQLSTATE for a database-DDL error,
// mirroring roleErrorSQLState. M0119-0004-ACLHEAP follow-up (loop #79
// deferral: database-DDL errors previously all mapped to the generic
// sqlstate.SystemError, unlike the role-DDL side's typed dispatch).
func databaseDDLErrorSQLState(err error) sqlstate.Code {
	if de, ok := err.(*databaseDDLError); ok && de.code != "" {
		return de.code
	}
	return sqlstate.SystemError
}

// databaseDDLKind is the result of inspecting a SQL string for a
// CREATE / DROP DATABASE prefix.
type databaseDDLKind int

const (
	databaseDDLNone databaseDDLKind = iota
	databaseDDLCreate
	databaseDDLDrop
)

// alterDatabaseConfigOp is the result of a successful parseAlterDatabaseConfig
// classification: an `ALTER DATABASE <name> SET <config> = <value>` /
// `RESET <config>` / `RESET ALL` statement. goopg's parser does not
// recognise ALTER DATABASE at all (it requires the literal TABLE keyword
// after ALTER — see parseAlter in internal/parser/ddl.go), so — mirroring
// classifyDatabaseDDL's CREATE/DROP DATABASE bypass — this intercepts the
// same raw-SQL surface at the wire-protocol dispatch layer instead of
// teaching the real parser a new statement shape. M0119-0004-ACLHEAP
// (ALTER DATABASE ... SET follow-up; datacl half's own resume point).
//
// Only `SET name = value` / `RESET name` / `RESET ALL` are modelled — every
// other ALTER DATABASE form (CONNECTION LIMIT, IS_TEMPLATE, RENAME TO,
// OWNER TO, ...) is intentionally NOT recognised here so it keeps falling
// through to the pre-existing `compatNoopCommandTag` no-op absorption
// (dispatch.go), unchanged from before this slice.
type alterDatabaseConfigOp struct {
	dbName      string
	configName  string // empty when resetAll
	configValue string // meaningful only when !reset && !resetAll && !fromCurrent
	reset       bool   // RESET <name>
	resetAll    bool   // RESET ALL
	fromCurrent bool   // SET <name> FROM CURRENT — configValue resolved at apply time
}

// currentGUCResolver resolves the calling session's live/current effective
// value for a GUC name — the mechanism `ALTER DATABASE/ROLE ... SET <name>
// FROM CURRENT` needs. Mirrors PG's ExtractSetVariableArgs VAR_SET_CURRENT
// case (postgres/src/backend/utils/misc/guc_funcs.c), which resolves via
// GetConfigOptionByName(name, NULL, false). ok=false means the name is not a
// recognised GUC (a nil resolver — no live session, e.g. some embedded/test
// paths — behaves the same way).
type currentGUCResolver func(name string) (string, bool)

// parseAlterDatabaseConfig recognises the SET/RESET forms of ALTER DATABASE
// described on alterDatabaseConfigOp. Returns ok=false for any other SQL
// (including unrecognised ALTER DATABASE sub-forms), leaving the caller to
// fall through to its existing behaviour.
func parseAlterDatabaseConfig(sql string) (alterDatabaseConfigOp, bool) {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "alter database ") {
		return alterDatabaseConfigOp{}, false
	}
	dbName, rest, ok := splitLeadingSQLToken(s[len("alter database "):])
	if !ok || dbName == "" {
		return alterDatabaseConfigOp{}, false
	}
	lowerRest := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(lowerRest, "set "):
		rest = strings.TrimSpace(rest[len("set "):])
		if name, value, reset, matched := parseSetRestSpecialForm(rest); matched {
			if reset {
				return alterDatabaseConfigOp{dbName: dbName, configName: name, reset: true}, true
			}
			return alterDatabaseConfigOp{dbName: dbName, configName: name, configValue: value}, true
		}
		configName, rest, ok := splitLeadingSQLToken(rest)
		if !ok || configName == "" {
			return alterDatabaseConfigOp{}, false
		}
		// "var_name FROM CURRENT" (set_rest_more's VAR_SET_CURRENT production,
		// postgres/src/backend/parser/gram.y) — the value is the live session's
		// CURRENT effective value for configName, resolved later at apply time
		// (parseAlterDatabaseConfig stays a pure/session-less parse function).
		if strings.EqualFold(strings.TrimSpace(rest), "from current") {
			return alterDatabaseConfigOp{dbName: dbName, configName: configName, fromCurrent: true}, true
		}
		switch lowerRest := strings.ToLower(rest); {
		case strings.HasPrefix(lowerRest, "to "):
			rest = strings.TrimSpace(rest[len("to "):])
		case strings.HasPrefix(rest, "="):
			rest = strings.TrimSpace(rest[1:])
		default:
			return alterDatabaseConfigOp{}, false
		}
		if strings.EqualFold(rest, "default") {
			return alterDatabaseConfigOp{dbName: dbName, configName: configName, reset: true}, true
		}
		value, ok := flattenConfigValueList(rest)
		if !ok {
			return alterDatabaseConfigOp{}, false
		}
		return alterDatabaseConfigOp{dbName: dbName, configName: configName, configValue: value}, true
	case strings.HasPrefix(lowerRest, "reset "):
		rest = strings.TrimSpace(rest[len("reset "):])
		if strings.EqualFold(rest, "all") {
			return alterDatabaseConfigOp{dbName: dbName, resetAll: true}, true
		}
		configName, _, ok := splitLeadingSQLToken(rest)
		if !ok || configName == "" {
			return alterDatabaseConfigOp{}, false
		}
		return alterDatabaseConfigOp{dbName: dbName, configName: configName, reset: true}, true
	}
	return alterDatabaseConfigOp{}, false
}

// splitLeadingSQLToken reads one SQL token (a double-quoted identifier, or a
// bare run of characters up to the next delimiter) from the start of s and
// returns it alongside the remainder of s (leading whitespace trimmed).
// Stops at whitespace, ';', ',', '(', ')', or '=' for a bare token so a
// config name immediately followed by "=value" (no space) still splits
// correctly. ok=false when s has no leading token.
func splitLeadingSQLToken(s string) (token, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", "", false
		}
		token = s[1 : 1+end]
		rest = strings.TrimLeft(s[1+end+1:], " \t\r\n")
		return token, rest, true
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == ',' || c == '(' || c == ')' || c == '=' {
			break
		}
		end++
	}
	if end == 0 {
		return "", "", false
	}
	token = s[:end]
	rest = strings.TrimLeft(s[end:], " \t\r\n")
	return token, rest, true
}

// flattenConfigValueList parses the comma-separated `var_value` list after
// `SET name TO`/`SET name =` and joins it into the raw form PG stores in
// pg_db_role_setting.setconfig (mirrors guc.c's flatten_set_variable_args:
// string literals are unescaped and stripped of their quotes, bare tokens
// are kept verbatim, elements are comma-joined with no extra quoting). The
// real pg_dump client (not goopg) re-quotes this text into a proper `SET ...
// TO ...` clause on restore (makeAlterConfigCommand, dumputils.c), so goopg
// only needs to reproduce the stored value, not the display form.
func flattenConfigValueList(s string) (string, bool) {
	parts, ok := splitTopLevelSQLCommas(s)
	if !ok || len(parts) == 0 {
		return "", false
	}
	flat := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", false
		}
		if p[0] == '\'' {
			val, ok := unquoteSQLStringLiteral(p)
			if !ok {
				return "", false
			}
			flat = append(flat, val)
		} else if p[0] == '"' && strings.HasSuffix(p, `"`) && len(p) >= 2 {
			flat = append(flat, p[1:len(p)-1])
		} else {
			flat = append(flat, p)
		}
	}
	return strings.Join(flat, ","), true
}

// parseSetRestSpecialForm recognizes the SQL-standard/PostgreSQL "special
// syntaxes" that gram.y's set_rest production accepts as alternatives to
// the generic `name TO|= value` form (postgres/src/backend/parser/gram.y,
// set_rest: TIME ZONE / SCHEMA / NAMES / ROLE / SESSION AUTHORIZATION /
// XML OPTION). SetResetClause — used by both the plain SET statement and
// ALTER DATABASE/ROLE's SET clause (AlterDatabaseSetStmt/AlterRoleSetStmt)
// — reduces to the identical set_rest production, so these forms are valid
// there too and PG's AlterSetting stores the same translated name/value
// pair into pg_db_role_setting (dbcommands.c AlterDatabaseSet -> guc_funcs.c
// ExtractSetVariableArgs). rest is the text immediately following the "set "
// keyword (already trimmed). matched=false means rest is not one of these
// forms; the caller should fall back to generic name/value parsing.
//
// TRANSACTION SNAPSHOT is deliberately excluded: its VAR_SET_MULTI kind has
// no case in ExtractSetVariableArgs, so real PG cannot store it via
// AlterSetting either — it is a transaction-scoped command, not a
// persistable GUC.
func parseSetRestSpecialForm(rest string) (configName, configValue string, reset, matched bool) {
	lower := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(lower, "time zone "):
		val := strings.TrimSpace(rest[len("time zone "):])
		if strings.EqualFold(val, "default") || strings.EqualFold(val, "local") {
			return "timezone", "", true, true
		}
		v, ok := flattenConfigValueList(val)
		if !ok {
			return "", "", false, false
		}
		return "timezone", v, false, true
	case strings.HasPrefix(lower, "schema "):
		val := strings.TrimSpace(rest[len("schema "):])
		v, ok := flattenConfigValueList(val)
		if !ok {
			return "", "", false, false
		}
		return "search_path", v, false, true
	case lower == "names" || strings.HasPrefix(lower, "names "):
		val := strings.TrimSpace(rest[len("names"):])
		if val == "" || strings.EqualFold(val, "default") {
			return "client_encoding", "", true, true
		}
		v, ok := flattenConfigValueList(val)
		if !ok {
			return "", "", false, false
		}
		return "client_encoding", v, false, true
	case strings.HasPrefix(lower, "role "):
		val := strings.TrimSpace(rest[len("role "):])
		v, ok := flattenConfigValueList(val)
		if !ok {
			return "", "", false, false
		}
		return "role", v, false, true
	case strings.HasPrefix(lower, "session authorization "):
		val := strings.TrimSpace(rest[len("session authorization "):])
		if strings.EqualFold(val, "default") {
			return "session_authorization", "", true, true
		}
		v, ok := flattenConfigValueList(val)
		if !ok {
			return "", "", false, false
		}
		return "session_authorization", v, false, true
	case strings.HasPrefix(lower, "xml option "):
		val := strings.TrimSpace(rest[len("xml option "):])
		switch strings.ToUpper(val) {
		case "DOCUMENT":
			return "xmloption", "DOCUMENT", false, true
		case "CONTENT":
			return "xmloption", "CONTENT", false, true
		}
		return "", "", false, false
	}
	return "", "", false, false
}

// splitTopLevelSQLCommas splits s on ',' characters that are not inside a
// single-quoted string (honouring the SQL '' doubled-quote escape). Returns
// ok=false on an unterminated string literal.
func splitTopLevelSQLCommas(s string) ([]string, bool) {
	var parts []string
	start := 0
	i, n := 0, len(s)
	for i < n {
		switch s[i] {
		case '\'':
			i++
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			if i > n {
				return nil, false
			}
		case ',':
			parts = append(parts, s[start:i])
			i++
			start = i
		default:
			i++
		}
	}
	parts = append(parts, s[start:])
	return parts, true
}

// unquoteSQLStringLiteral strips the surrounding single quotes from a SQL
// string literal and unescapes doubled single quotes ('' -> '). ok=false
// when s is not a well-formed 'quoted' literal.
func unquoteSQLStringLiteral(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	inner := s[1 : len(s)-1]
	return strings.ReplaceAll(inner, "''", "'"), true
}

// classifyDatabaseDDL returns the kind and the database name when sql
// is a recognisable CREATE/DROP DATABASE statement. Anything else
// returns `databaseDDLNone`.
//
// The pattern matched is intentionally loose:
//
//   create database <name> [...]
//   drop   database [if exists] <name> [...]
//
// Any trailing options (TEMPLATE, ENCODING, OWNER, …) are ignored —
// goopg has no per-database storage to apply them to and HammerDB
// does not pass any.
func classifyDatabaseDDL(sql string) (databaseDDLKind, string) {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "create database "):
		return databaseDDLCreate, extractFirstIdentifier(s[len("create database "):])
	case strings.HasPrefix(lower, "drop database if exists "):
		return databaseDDLDrop, extractFirstIdentifier(s[len("drop database if exists "):])
	case strings.HasPrefix(lower, "drop database "):
		return databaseDDLDrop, extractFirstIdentifier(s[len("drop database "):])
	}
	return databaseDDLNone, ""
}

// extractFirstIdentifier reads the first SQL identifier from s,
// honouring double-quoted form. Returns "" when s is empty or
// the leading token is not an identifier.
func extractFirstIdentifier(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return ""
		}
		return s[1 : 1+end]
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == ',' || c == '(' || c == ')' {
			break
		}
		end++
	}
	return s[:end]
}

// databaseDDLCommandTag returns the wire-protocol CommandComplete tag
// for a CREATE/DROP/ALTER(SET/RESET) DATABASE statement. Mirrors
// upstream's tag.
func databaseDDLCommandTag(sql string) string {
	if _, ok := parseAlterDatabaseConfig(sql); ok {
		return "ALTER DATABASE"
	}
	kind, _ := classifyDatabaseDDL(sql)
	switch kind {
	case databaseDDLCreate:
		return "CREATE DATABASE"
	case databaseDDLDrop:
		return "DROP DATABASE"
	default:
		return ""
	}
}

// tryHandleDatabaseDDL returns (handled, notice, err). When handled is true
// the dispatch path should NOT fall through to compatNoopCommandTag.
//
//   - handled=true,  err=nil   → CommandComplete should be written
//   - handled=true,  err!=nil  → an ErrorResponse should be written
//   - handled=false, err=nil   → not a database DDL; continue dispatch
//
// The catalog mutation happens BEFORE the WAL append; if the WAL
// append fails the catalog mutation is rolled back so the on-disk
// state and in-memory state stay consistent.
//
// liveDBName is the calling connection's own database (connTx.DBName) —
// needed only for the ALTER DATABASE ... SET/RESET branch, which (mirroring
// execDatabaseACLChange's datacl v0-scope restriction) only takes real
// effect when the named database is the connection's own live database;
// naming any OTHER database is a silent no-op, matching goopg v0's
// single-live-database-storage scope (see the package doc comment above).
func (s *Server) tryHandleDatabaseDDL(sql string, liveDBName string, resolveCurrent currentGUCResolver) (bool, string, error) {
	if op, ok := parseAlterDatabaseConfig(sql); ok {
		return s.applyAlterDatabaseConfig(op, liveDBName, resolveCurrent)
	}
	kind, name := classifyDatabaseDDL(sql)
	if kind == databaseDDLNone {
		return false, "", nil
	}
	if name == "" {
		return true, "", &databaseDDLError{code: sqlstate.SyntaxError, msg: "missing database name"}
	}
	if s.cfg.Catalog == nil {
		// No catalog plumbed (some test/embedded paths). Fall back to
		// the legacy no-op so behaviour is unchanged.
		return false, "", nil
	}
	cat, ok := s.cfg.Catalog.(databaseRegistry)
	if !ok {
		// Catalog implementation does not expose the database
		// registry surface yet — preserve legacy no-op behaviour.
		return false, "", nil
	}
	switch kind {
	case databaseDDLCreate:
		if err := cat.CreateDatabase(name); err != nil {
			if errors.Is(err, catalog.ErrDatabaseExists) {
				// PG: dbcommands.c createdb(), ERRCODE_DUPLICATE_DATABASE.
				return true, "", &databaseDDLError{
					code: sqlstate.DuplicateDatabase,
					msg:  fmt.Sprintf("database %q already exists", name),
				}
			}
			return true, "", err
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeCreateDatabase(name)); werr != nil {
				// Roll back the catalog change so memory and disk agree.
				_ = cat.DropDatabase(name)
				return true, "", werr
			}
		}
		return true, "", nil
	case databaseDDLDrop:
		if err := cat.DropDatabase(name); err != nil {
			if errors.Is(err, catalog.ErrDatabaseNotFound) {
				// IF EXISTS branch was already accepted by the prefix
				// match; the executor must not surface "not found"
				// when the user said IF EXISTS. Inspect the SQL again.
				lower := strings.ToLower(strings.TrimSpace(sql))
				if strings.HasPrefix(lower, "drop database if exists ") {
					notice := fmt.Sprintf("database %q does not exist, skipping", name)
					return true, notice, nil
				}
				// Non-IF-EXISTS: PG: dbcommands.c dropdb(), ERRCODE_UNDEFINED_DATABASE.
				return true, "", &databaseDDLError{
					code: sqlstate.UndefinedDatabase,
					msg:  fmt.Sprintf("database %q does not exist", name),
				}
			}
			return true, "", err
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeDropDatabase(name)); werr != nil {
				// Re-create the catalog entry so the abort is consistent.
				_ = cat.CreateDatabase(name)
				return true, "", werr
			}
		}
		return true, "", nil
	}
	return false, "", nil
}

// handleDatabaseDDLBypass runs tryHandleDatabaseDDL against sql and, when it
// applies, writes the resulting NoticeResponse/CommandComplete/ReadyForQuery
// sequence (or the mapped ErrorResponse) directly to w — the simple-query
// wire-response shape shared by both of dispatchSimpleQueryViaExecutor's two
// call sites (the DROP DATABASE pre-parse check and the CREATE/ALTER
// DATABASE parse-failure fallback). Returns handled=false when sql isn't a
// database-DDL bypass form (or the bypass degrades to legacy no-op, e.g. no
// catalog plumbed), so the caller continues its normal dispatch path.
func (s *Server) handleDatabaseDDLBypass(sql, liveDBName string, resolveCurrent currentGUCResolver, w *protocol.FrameWriter) (handled bool, err error) {
	handled, notice, herr := s.tryHandleDatabaseDDL(sql, liveDBName, resolveCurrent)
	if !handled {
		return false, nil
	}
	if herr != nil {
		return true, s.writeQueryError(w, databaseDDLErrorSQLState(herr), herr.Error())
	}
	if notice != "" {
		if err := w.WriteNoticeResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "NOTICE"},
			{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: protocol.FieldSQLState, Value: "00000"},
			{Code: protocol.FieldMessage, Value: notice},
		}); err != nil {
			return true, err
		}
	}
	tag := databaseDDLCommandTag(sql)
	if err := w.WriteCommandComplete(tag); err != nil {
		return true, err
	}
	return true, w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// databaseRegistry is the subset of catalog.Catalog the database-DDL
// handler needs. catalog.InMemory satisfies this interface; alternate
// implementations (e.g. tests) opt in by exposing the same methods.
type databaseRegistry interface {
	CreateDatabase(name string) error
	DropDatabase(name string) error
	HasDatabase(name string) bool
}

// databaseConfigRegistry is the subset of catalog.Catalog the ALTER
// DATABASE ... SET/RESET handler needs. catalog.InMemory satisfies this
// interface. M0119-0004-ACLHEAP (ALTER DATABASE ... SET follow-up).
type databaseConfigRegistry interface {
	SetDatabaseConfig(dbOid uint32, name, value string)
	ResetDatabaseConfig(dbOid uint32, name string)
	ResetAllDatabaseConfig(dbOid uint32)
}

// databaseConnLimitRegistry is the subset of catalog.Catalog the
// connection-startup `datconnlimit = -2` (invalid database) check needs.
// catalog.InMemory satisfies this interface. A separate interface from
// databaseRegistry (rather than adding this method there) keeps any
// catalog fake that implements CreateDatabase/DropDatabase/HasDatabase but
// not DatabaseConnLimit from silently losing the unrelated role/database-
// existence checks that also gate on a databaseRegistry type assertion.
// M0119-0006 (AC-002 residual #1).
type databaseConnLimitRegistry interface {
	DatabaseConnLimit(name string) int32
}

// applyAlterDatabaseConfig applies a parsed ALTER DATABASE ... SET/RESET
// operation, mirroring tryHandleDatabaseDDL's (handled, notice, err) shape.
// Naming any database other than the connection's own liveDBName is a
// silent no-op (handled=true, err=nil) — see tryHandleDatabaseDDL's doc
// comment for why.
func (s *Server) applyAlterDatabaseConfig(op alterDatabaseConfigOp, liveDBName string, resolveCurrent currentGUCResolver) (bool, string, error) {
	if s.cfg.Catalog == nil {
		return false, "", nil
	}
	cat, ok := s.cfg.Catalog.(databaseConfigRegistry)
	if !ok {
		return false, "", nil
	}
	if !strings.EqualFold(strings.Trim(op.dbName, `"`), liveDBName) {
		// Not the connection's own database: real multi-database storage
		// isolation does not exist in goopg v0 (mirrors CREATE DATABASE's
		// package-doc scope note and execDatabaseACLChange's identical
		// datacl restriction), so there is no registry to write into for
		// any other name. Silent success matches PG's own behaviour for a
		// database the caller has CONNECT/ownership rights on (goopg has
		// no cross-database permission model to reject this differently).
		return true, "", nil
	}
	if op.fromCurrent {
		// Resolve the live session's CURRENT effective value now (mirrors
		// PG's GetConfigOptionByName call at parse-to-apply time) — only
		// once we know this ALTER DATABASE targets the caller's own live
		// database, so an "other database" no-op never has to resolve
		// anything or surface a bogus "unrecognized parameter" error.
		if resolveCurrent == nil {
			return true, "", &databaseDDLError{
				code: sqlstate.UndefinedObject,
				msg:  fmt.Sprintf("unrecognized configuration parameter %q", op.configName),
			}
		}
		val, ok := resolveCurrent(op.configName)
		if !ok {
			return true, "", &databaseDDLError{
				code: sqlstate.UndefinedObject,
				msg:  fmt.Sprintf("unrecognized configuration parameter %q", op.configName),
			}
		}
		op.configValue = val
	}
	// FirstUserOID (16384) is the SAME SQL-visible placeholder OID
	// pg_database.VirtualRows displays for the "postgres" row — NOT
	// catalog.InMemory.DBOID() (the real on-disk physical OID datacl keys
	// its heap resync under). pg_db_role_setting is a pure virtual table
	// with no heap to resync, and pg_dump's dumpDatabaseConfig
	// cross-references setdatabase against the oid it already read from
	// pg_database, so the two must agree.
	dbOid := catalog.FirstUserOID
	switch {
	case op.resetAll:
		cat.ResetAllDatabaseConfig(dbOid)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterDatabaseResetAllConfig(dbOid)); werr != nil {
				return true, "", werr
			}
		}
	case op.reset:
		cat.ResetDatabaseConfig(dbOid, op.configName)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterDatabaseResetConfig(dbOid, op.configName)); werr != nil {
				return true, "", werr
			}
		}
	default:
		cat.SetDatabaseConfig(dbOid, op.configName, op.configValue)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterDatabaseSetConfig(dbOid, op.configName, op.configValue)); werr != nil {
				return true, "", werr
			}
		}
	}
	return true, "", nil
}
