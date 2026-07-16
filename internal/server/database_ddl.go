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
// Multi-database storage isolation was NOT in scope when this file was
// first written — every relation routed through `catalog.DefaultDBOid`
// regardless of which database a connection attached to. That has since
// changed (M0122-0007, design docs/design/0122-0018-per-database-catalog-
// namespace.md): each CREATE DATABASE now allocates its own real dbOid
// (DatabaseOid), a connection's DML/DDL keys off its OWN database's
// catalog.tableNamespace and its own base/<dbOid> physical directory
// (internal/storage/smgr.go), and CREATE DATABASE ... TEMPLATE is validated
// against the template's real per-database relation set
// (resolveCreateDatabaseTemplate below) rather than silently ignored.
// TEMPLATE now also actually COPIES the bounded case real PostgreSQL's
// createdb() handles most commonly: a template whose user relations are all
// plain, unindexed heap tables (copyTemplateTables below) — physically
// copying each relation's MainFork file plus registering fresh catalog.Table
// entries and pg_class/pg_attribute catalog-heap rows under the new dbOid.
// Templates containing an index, sequence, view, materialized view, or typed
// table still error with FeatureNotSupported (see
// resolveCreateDatabaseTemplate's doc comment and the deferral ledger) —
// everything else in this file's original scope note (pg_database surviving
// a crash, connections to a recovered database succeeding) still holds.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/storage"
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
// Any trailing options other than TEMPLATE (ENCODING, OWNER, …) are still
// ignored — goopg has no per-database storage to apply them to and HammerDB
// does not pass any. TEMPLATE is extracted separately by
// createDatabaseTemplateName and validated by resolveCreateDatabaseTemplate
// (M0122-0007 4e).
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

// dropDatabaseForceRe matches the trailing `[ [ WITH ] ( FORCE ) ]` clause of
// a DROP DATABASE statement (gram.y's createdb_opt_list analog, reused for
// DROP DATABASE) — the only option this bypass recognises. Any other
// trailing option keeps falling through classifyDatabaseDDL's existing
// "trailing options are ignored" looseness (package doc comment above).
var dropDatabaseForceRe = regexp.MustCompile(`(?:with\s*)?\(\s*force\s*\)\s*$`)

// dropDatabaseHasForce reports whether sql is a DROP DATABASE statement
// carrying a WITH (FORCE) (or bare (FORCE)) option — PG's dbcommands.c
// dropdb() `force` argument, sourced from DropdbStmt.options in
// standard_ProcessUtility.
func dropDatabaseHasForce(sql string) bool {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return dropDatabaseForceRe.MatchString(strings.ToLower(s))
}

// createDatabaseTemplateRe matches the `TEMPLATE [=] <name>` option
// (gram.y's createdb_opt_item T_TEMPLATE case) within a CREATE DATABASE
// statement's trailing option list. Applied only to the substring AFTER the
// new database's own name (see createDatabaseTemplateName) — never to the
// whole statement — so a name that happens to start with "template" (e.g.
// `CREATE DATABASE template_scratch`) is never mistaken for the option
// itself.
var createDatabaseTemplateRe = regexp.MustCompile(`(?i)\btemplate\s*=?\s*(?:"([^"]*)"|([A-Za-z_][A-Za-z0-9_$]*))`)

// createDatabaseTemplateName returns the TEMPLATE option's target database
// name for a CREATE DATABASE statement, defaulting to "template1" when the
// option is absent — mirrors dbcommands.c createdb()'s "if (!dbtemplate)
// dbtemplate = template1" default (postgres/src/backend/commands/
// dbcommands.c). sql must already be known (via classifyDatabaseDDL) to be a
// CREATE DATABASE statement.
func createDatabaseTemplateName(sql string) string {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "create database ") {
		return "template1"
	}
	rest := s[len("create database "):]
	_, end := extractFirstIdentifierSpan(rest)
	m := createDatabaseTemplateRe.FindStringSubmatch(rest[end:])
	if m == nil {
		return "template1"
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// extractFirstIdentifier reads the first SQL identifier from s,
// honouring double-quoted form. Returns "" when s is empty or
// the leading token is not an identifier.
func extractFirstIdentifier(s string) string {
	name, _ := extractFirstIdentifierSpan(s)
	return name
}

// extractFirstIdentifierSpan is extractFirstIdentifier's span-returning
// twin: it additionally reports how many bytes of the ORIGINAL (untrimmed) s
// the identifier — plus any leading whitespace and surrounding quotes —
// consumed, so a caller can slice the remainder of s (e.g. CREATE DATABASE's
// trailing option list, see createDatabaseTemplateName) without re-parsing
// the name itself. extractFirstIdentifier is a thin wrapper that discards
// end.
func extractFirstIdentifierSpan(s string) (name string, end int) {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	skipped := len(s) - len(trimmed)
	if trimmed == "" {
		return "", len(s)
	}
	if trimmed[0] == '"' {
		close := strings.IndexByte(trimmed[1:], '"')
		if close < 0 {
			return "", len(s)
		}
		return trimmed[1 : 1+close], skipped + 1 + close + 1
	}
	e := 0
	for e < len(trimmed) {
		c := trimmed[e]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == ',' || c == '(' || c == ')' {
			break
		}
		e++
	}
	return trimmed[:e], skipped + e
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

// resolveActingRoleOID resolves actingRole (the connTx.NonSuperuserRole
// convention: "" means the bootstrap superuser) to a role OID, for recording
// as pg_database.datdba / checking DROP DATABASE ownership. Falls back to
// catalog.BootstrapSuperuserOID when the catalog doesn't expose RoleOID or
// the name isn't registered — defensive; a live session's own effective role
// is always registered in practice.
func (s *Server) resolveActingRoleOID(actingRole string) uint32 {
	if actingRole == "" {
		return catalog.BootstrapSuperuserOID
	}
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return catalog.BootstrapSuperuserOID
	}
	if oid, found := im.RoleOID(actingRole); found {
		return oid
	}
	return catalog.BootstrapSuperuserOID
}

// resolveCreateDatabaseTemplate resolves templateName to the real dbOid
// CREATE DATABASE ... TEMPLATE should copy from, plus the sets of the
// template's own tables, sequences, plain views, and materialized views that
// this bounded slice knows how to copy. The template database must exist
// (mirrors dbcommands.c createdb()'s get_db_info ERRCODE_UNDEFINED_DATABASE
// check). Beyond that, three shapes are distinguished:
//
//   - templateName resolves to catalog.DefaultDBOid ("template1"/"postgres",
//     see the dual-mirror note below), or genuinely has zero user content:
//     returns (0, nil, nil, nil, nil) — "nothing to copy", the caller
//     proceeds exactly like a plain CREATE DATABASE (unchanged pre-existing
//     behavior, relied on by TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds).
//   - every one of the template's user relations is a plain, unindexed heap
//     table or an unindexed materialized view (!Virtual && !IsSequence &&
//     View == nil && OfTypeOID == 0 for plain tables; IsMatView && no index
//     for matviews, and the template's dbOid has zero registered indexes at
//     all — an index anywhere, including on a matview, still rules out the
//     whole database), plus any number of non-TEMPORARY sequences and plain
//     views: returns (srcOid, tables, sequences, views, matViews, nil) — the
//     caller copies each via copyTemplateTables / copyTemplateSequences /
//     copyTemplateViews / copyTemplateMatViews respectively (independent of
//     one another; a template can contain any combination of the four, or
//     none).
//   - the template contains anything else this slice does not implement
//     copying for (an index anywhere in the database, a typed table, or a
//     temporary sequence): returns a FeatureNotSupported error.
//     Index/typed-table TEMPLATE copying is deferred to a future loop (see
//     the deferral ledger) — copying just the bounded common case (plain
//     tables + sequences + views + matviews) first keeps each loop's blast
//     radius bounded, without also having to invent index-file cloning or
//     typed-table cloning in the same pass.
//
// The emptiness/kind checks are skipped entirely when templateName resolves
// to catalog.DefaultDBOid: "template1" and "postgres" both alias that one
// shared oid in goopg's current architecture (the pre-existing postgres/
// DefaultDBOid dual-mirror, design 0122-0018-per-database-catalog-
// namespace.md) — a legacy namespace that predates per-database storage
// isolation and that every fixture and pre-4c code path still writes real
// user tables into. There is no way to tell "template1's own content" apart
// from "postgres's own content" at that shared oid, so any check there would
// misfire on every server that has ever created a table (the overwhelmingly
// common case) — skip it and keep the exact pre-existing behavior (silently
// produce an empty new database) there, same as before this loop. Only a
// template that resolves to its OWN distinct, CreateDatabase-allocated dbOid
// (any other registered database, or template0's fixed, never-aliased oid 4)
// gets the new checks, since only there are they reliable/meaningful
// questions. A catalog that doesn't expose databaseTemplateRegistry (some
// test/embedded paths) skips every check, matching every other soft-typed-
// interface fallback in this file.
func (s *Server) resolveCreateDatabaseTemplate(templateName string) (srcOid uint32, tables []*catalog.Table, sequences []executor.SeqInfo, views []*catalog.Table, matViews []*catalog.Table, err error) {
	tmpl, ok := s.cfg.Catalog.(databaseTemplateRegistry)
	if !ok {
		return 0, nil, nil, nil, nil, nil
	}
	oid, ok := tmpl.ResolveDatabaseOid(templateName)
	if !ok {
		return 0, nil, nil, nil, nil, &databaseDDLError{
			code: sqlstate.UndefinedDatabase,
			msg:  fmt.Sprintf("template database %q does not exist", templateName),
		}
	}
	if oid == catalog.DefaultDBOid {
		return 0, nil, nil, nil, nil, nil
	}
	// Any index anywhere in the template rules out the whole database for
	// this bounded slice — goopg has no per-database sys-btree catalog
	// bootstrap yet (M0122-0007 4e follow-up 39's own deferral) and this loop
	// does not implement index-file cloning, so even a template whose TABLES
	// are otherwise all plain heap tables cannot be copied if any of them
	// carries an index.
	unsupported := len(tmpl.AllIndexes(oid)) > 0
	var userTables []*catalog.Table
	var matViewTables []*catalog.Table
	for _, t := range tmpl.AllTables(oid) {
		if catalog.IsSystemRelation(t.OID) {
			// System/catalog relations (anything below catalog.FirstUserOID,
			// e.g. the heap-backed pg_attribute/pg_type rows every database's
			// namespace carries from bootstrap) don't represent user content
			// to copy and are never rejected for being present.
			continue
		}
		// Sequences and plain views never appear here: AllTables skips every
		// Virtual entry, and both CreateSequenceCatalogRelation and
		// CreateView always mark their pg_class row Virtual — they're
		// detected via executor.AllSequenceInfos / tmpl.AllViews below
		// instead, straight from their own respective registries.
		if t.OfTypeOID != 0 {
			unsupported = true
			continue
		}
		// Materialized views (M0122-0007 4e follow-up 43): unlike a plain
		// view, a matview's pg_class row is NOT Virtual (it owns real heap
		// storage, see execCreateMatView's CreateTable call) — it already
		// surfaces through this same AllTables loop, so it needs no separate
		// registry surface the way AllViews does for plain views. Still
		// subject to the whole-database index veto above (an unindexed
		// matview is the only shape this slice copies).
		if t.IsMatView {
			matViewTables = append(matViewTables, t)
			continue
		}
		userTables = append(userTables, t)
	}
	var seqInfos []executor.SeqInfo
	for _, si := range executor.AllSequenceInfos(oid) {
		// A TEMPORARY sequence is session-scoped and has no durable state
		// worth cloning; keep the whole template unsupported in that (rare)
		// case rather than silently dropping it.
		name := si.Name
		if si.Schema != "" && si.Schema != "public" {
			name = si.Schema + "." + si.Name
		}
		if executor.IsSequenceTemporary(name, oid) {
			unsupported = true
			continue
		}
		seqInfos = append(seqInfos, si)
	}
	// Plain views (M0122-0007 4e follow-up 42): unlike typed-tables above, a
	// view is always copyable in this slice — it is only an AST + raw SQL
	// text, no relation file and no per-database sys-btree bootstrap needed
	// (same reasoning as sequences). No unsupported check here.
	viewTables := tmpl.AllViews(oid)
	if unsupported {
		return 0, nil, nil, nil, nil, &databaseDDLError{
			code: sqlstate.FeatureNotSupported,
			msg: fmt.Sprintf("CREATE DATABASE ... TEMPLATE %q: copying a template database "+
				"containing indexes, typed tables, or temporary sequences is not yet "+
				"supported", templateName),
		}
	}
	if len(userTables) == 0 && len(seqInfos) == 0 && len(viewTables) == 0 && len(matViewTables) == 0 {
		return 0, nil, nil, nil, nil, nil
	}
	return oid, userTables, seqInfos, viewTables, matViewTables, nil
}

// createDatabasePhysicalDirectory creates base/<oid>/PG_VERSION for a
// just-allocated CREATE DATABASE oid (M0122-0007 physical-storage-isolation
// slice 2). Every relation created under this connection's own dbOid now
// physically routes its files through base/<oid> (internal/storage/smgr.go's
// relDir/sharedOrPerDBRelDir, wired by the per-database catalog-namespace
// epic, design 0122-0018) — so this scaffold is real, live storage, not a
// vestigial directory. A nil Pool (some test/embedded contexts, mirroring
// executor's ddlOp.relocateRelationPhysicalFile) or an empty DataDir is a
// silent no-op — the registry entry still stands alone, matching how other
// DDL operators skip cluster-filesystem effects in those contexts.
//
// TEMPLATE's target-existence and target-kind checks happen earlier, in
// resolveCreateDatabaseTemplate (called from tryHandleDatabaseDDL before this
// function runs) — but this function itself still only ever creates an EMPTY
// scaffold: it never copies the template's relation files into the new oid's
// directory. When resolveCreateDatabaseTemplate did find real tables to copy,
// tryHandleDatabaseDDL calls copyTemplateTables (below) separately, AFTER
// this function, to physically copy each relation file and register the
// cloned catalog.Table entries — the two-step split keeps this function's own
// contract (an empty base/<oid> scaffold) unchanged for every caller that
// still has nothing to copy (template0/template1/any other currently-empty
// database). Index/sequence/view/matview TEMPLATE copying remains
// unimplemented — see the deferral ledger.
func (s *Server) createDatabasePhysicalDirectory(oid uint32) error {
	if s.cfg.Pool == nil {
		return nil
	}
	dataDir := s.cfg.Pool.Manager().DataDir()
	if dataDir == "" {
		return nil
	}
	return initdb.CreatePerDatabaseScaffolding(dataDir, oid)
}

// removeDatabasePhysicalDirectory best-effort removes base/<oid>. Two
// callers: (1) CREATE DATABASE rollback when a later step (currently only
// the WAL append in the databaseDDLCreate branch above) fails — keeps the
// rollback symmetric with createDatabasePhysicalDirectory's creation; (2)
// DROP DATABASE's own success path (databaseDDLDrop branch, M0122-0007
// physical-storage-isolation slice 3), once the drop itself is durable. A
// failure here is non-fatal in both cases: it only leaves a harmless
// orphaned (CREATE rollback) or leaked (DROP) directory, the same "safe
// orphan" tolerance relocateRelationPhysicalFileCleanupOld documents for
// the tablespace-relocation case — DROP DATABASE has already succeeded
// from the client's point of view by the time this runs, so a filesystem
// error here must not turn into a user-visible DROP DATABASE failure.
func (s *Server) removeDatabasePhysicalDirectory(oid uint32) {
	if s.cfg.Pool == nil {
		return
	}
	dataDir := s.cfg.Pool.Manager().DataDir()
	if dataDir == "" {
		return
	}
	_ = initdb.RemovePerDatabaseScaffolding(dataDir, oid)
}

// copyTemplateTables clones each of tables (already validated by
// resolveCreateDatabaseTemplate as plain, unindexed heap tables owned by
// srcOid) into newOid: a fresh catalog.Table registration under newOid, a
// physical copy of the relation's MainFork data file, and pg_class/
// pg_attribute catalog-heap rows so a restart rediscovers the copy through
// the same per-database catalog-heap loader M0122-0007 4e follow-up 39 wired
// for an ordinary CREATE TABLE (internal/initdb/open.go's
// loadUserTablesFromHeapForDB). Mirrors real PostgreSQL's createdb(), which
// physically copies each relation's heap file page-by-page
// (copy_relation_data, postgres/src/backend/commands/dbcommands.c) rather
// than re-inserting rows one at a time — the row bytes are opaque tuples,
// self-describing only via the catalog's own TupleDesc (registered here
// separately via CreateTable), so copying the raw file brings the DATA along
// for free.
//
// On any failure partway through the loop, every table already registered
// under newOid by this call is unwound via rollbackTemplateCopy before the
// error is returned. The caller (databaseDDLCreate) additionally drops the
// whole new database registration/directory, but rollbackTemplateCopy keeps
// the in-memory catalog's per-oid namespace map from carrying a half-copied
// table set even for that brief window.
func (s *Server) copyTemplateTables(srcOid, newOid uint32, tables []*catalog.Table) error {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		// No concrete InMemory catalog to clone into (some test/embedded
		// paths) — resolveCreateDatabaseTemplate already required this same
		// concrete type (via databaseTemplateRegistry) to produce a non-nil
		// tables slice, so this is unreachable in practice; defensive no-op
		// matches every other soft-typed-interface fallback in this file.
		return nil
	}
	for _, srcTbl := range tables {
		// Deep-copy the column slice: CreateTable copies it again internally,
		// but this local copy ensures the clone never shares backing-array
		// storage with the template's own Table.Columns, even transiently.
		cols := append([]catalog.Column(nil), srcTbl.Columns...)
		name := parser.ObjectName{Schema: srcTbl.Schema, Name: srcTbl.Name}
		newTbl, err := im.CreateTable(name, cols, newOid)
		if err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("cloning table %q: %w", srcTbl.Name, err)
		}
		oldRel := storage.RelFileNode{DBOid: srcOid, RelOid: srcTbl.OID, Fork: storage.MainFork}
		newRel := storage.RelFileNode{DBOid: newOid, RelOid: newTbl.OID, Fork: storage.MainFork}
		if err := s.copyTemplateRelationFile(oldRel, newRel); err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("copying relation file for table %q: %w", srcTbl.Name, err)
		}
		if err := s.syncCopiedTableCatalogHeap(newOid, newTbl); err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("syncing catalog heap for table %q: %w", srcTbl.Name, err)
		}
	}
	return nil
}


// copyTemplateSequences clones each of the template's sequences into newOid.
// Unlike copyTemplateTables, a sequence has no relation file and no
// catalog-heap row to copy — its durable state is a process-global registry
// entry (internal/executor's seqRegistry) snapshotted/restored via
// executor.SnapshotSequenceState/RestoreSequenceFromWAL, plus the virtual
// pg_class(relkind='S') catalog table CreateSequenceCatalogRelation already
// uses for every other sequence-creation path (CREATE SEQUENCE, WAL replay).
func (s *Server) copyTemplateSequences(srcOid, newOid uint32, sequences []executor.SeqInfo) error {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		// No concrete InMemory catalog to clone into (some test/embedded
		// paths) — resolveCreateDatabaseTemplate already required this same
		// concrete type (via databaseTemplateRegistry) to produce a non-nil
		// sequences slice, so this is unreachable in practice; defensive
		// no-op matches copyTemplateTables' identical fallback.
		return nil
	}
	for _, si := range sequences {
		seqObjName := parser.ObjectName{Schema: si.Schema, Name: si.Name}
		name := si.Name
		if si.Schema != "" && si.Schema != "public" {
			name = si.Schema + "." + si.Name
		}
		payload, ok := executor.SnapshotSequenceState(name, srcOid)
		if !ok {
			// The source-database busy guard in tryHandleDatabaseDDL makes
			// this rare (no other backend can be connected to the template
			// mid-copy), but be defensive rather than clone a stale/missing
			// sequence: a concurrent superuser DROP SEQUENCE could still
			// race the AllSequenceInfos scan in resolveCreateDatabaseTemplate.
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("cloning sequence %q: source sequence no longer exists", name)
		}
		payload.DBOid = newOid
		executor.RestoreSequenceFromWAL(payload)
		executor.CreateSequenceCatalogRelation(im, seqObjName, name, newOid)
		if s.cfg.WAL != nil {
			ectx := executor.NewContext()
			ectx.CurrentDatabaseOid = newOid
			ectx.WAL = s.cfg.WAL
			executor.WALLogSequenceState(ectx, name)
		}
	}
	return nil
}

// copyTemplateViews clones each of the template's plain (non-materialized)
// views into newOid. Unlike copyTemplateTables, a view has no relation file
// and no pg_class/pg_attribute heap dependency of its own to copy up front —
// its durable state is just its parsed SELECT AST (catalog.Table.View) plus
// the raw defining SQL text (ViewDef), exactly what execCreateView stamps on
// an ordinary CREATE VIEW. CreateView is reused directly to register the
// clone (mirrors copyTemplateTables' CreateTable reuse); syncCopiedTableCatalogHeap
// then persists the pg_class/pg_attribute rows plus the RecordKindCreateView
// WAL snapshot (internal/executor/operators_ddl.go's syncTableToCatalogHeap
// already handles the View-specific WAL record generically, keyed only on
// tbl.View/tbl.IsMatView/tbl.ViewDef — no view-specific plumbing needed
// there). The view's own AST is safe to share by reference across the two
// catalog.Table entries (source and clone): once created, a view's Query is
// only ever replaced wholesale by a later CREATE OR REPLACE VIEW on that
// same name/dbOid, never mutated in place.
func (s *Server) copyTemplateViews(newOid uint32, views []*catalog.Table) error {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		// No concrete InMemory catalog to clone into (some test/embedded
		// paths) — resolveCreateDatabaseTemplate already required this same
		// concrete type (via databaseTemplateRegistry) to produce a non-nil
		// views slice, so this is unreachable in practice; defensive no-op
		// matches copyTemplateTables' identical fallback.
		return nil
	}
	for _, srcView := range views {
		name := parser.ObjectName{Schema: srcView.Schema, Name: srcView.Name}
		cols := append([]catalog.Column(nil), srcView.Columns...)
		aliases := append([]string(nil), srcView.ViewColumnAliases...)
		newTbl, err := im.CreateView(name, cols, aliases, srcView.View, false, newOid)
		if err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("cloning view %q: %w", srcView.Name, err)
		}
		// CreateView only sets View/ViewColumnAliases — the rest of the
		// view-specific state execCreateView stamps post-creation (mirrors
		// its own ordering, operators_ddl.go's execCreateView) must be
		// copied across explicitly.
		newTbl.ViewDef = srcView.ViewDef
		newTbl.CheckOption = srcView.CheckOption
		newTbl.SecurityBarrier = srcView.SecurityBarrier
		newTbl.SecurityBarrierSet = srcView.SecurityBarrierSet
		newTbl.SecurityInvoker = srcView.SecurityInvoker
		newTbl.SecurityInvokerSet = srcView.SecurityInvokerSet
		if err := s.syncCopiedTableCatalogHeap(newOid, newTbl); err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("syncing catalog heap for view %q: %w", srcView.Name, err)
		}
	}
	return nil
}
// copyTemplateMatViews clones each of the template's materialized views into
// newOid. A materialized view combines both siblings above: like
// copyTemplateTables, it owns real heap storage (execCreateMatView's
// CreateTable call leaves Virtual: false) that must be physically copied via
// copyTemplateRelationFile; like copyTemplateViews, its defining query is a
// parsed SELECT AST (catalog.Table.View) plus raw SQL text (ViewDef) that
// CreateTable itself doesn't set and must be copied across explicitly.
// syncCopiedTableCatalogHeap's generic WAL handling already covers the
// RecordKindCreateMatView snapshot (syncTableToCatalogHeap, keyed on
// tbl.IsMatView/tbl.ViewDef, same funnel copyTemplateViews relies on for
// plain views) — no matview-specific plumbing needed there, and
// internal/initdb/matview_ddl_recovery.go's replay locates the reloaded
// table via LookupTableByOIDAllDBs, which is dbOid-agnostic and needs no
// changes for a copy living under a different dbOid than its source.
func (s *Server) copyTemplateMatViews(srcOid, newOid uint32, matViews []*catalog.Table) error {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		// No concrete InMemory catalog to clone into (some test/embedded
		// paths) — resolveCreateDatabaseTemplate already required this same
		// concrete type (via databaseTemplateRegistry) to produce a non-nil
		// matViews slice, so this is unreachable in practice; defensive
		// no-op matches copyTemplateTables' identical fallback.
		return nil
	}
	for _, srcMV := range matViews {
		cols := append([]catalog.Column(nil), srcMV.Columns...)
		name := parser.ObjectName{Schema: srcMV.Schema, Name: srcMV.Name}
		newTbl, err := im.CreateTable(name, cols, newOid)
		if err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("cloning materialized view %q: %w", srcMV.Name, err)
		}
		// CreateTable only registers an ordinary heap table — the
		// matview-specific state execCreateMatView stamps post-creation
		// (mirrors its own ordering, operators_ddl.go's execCreateMatView)
		// must be copied across explicitly, same as copyTemplateViews does
		// for a plain view's fields. The AST is safe to share by reference:
		// a matview's Query is only ever replaced wholesale by a later
		// CREATE OR REPLACE, never mutated in place (same reasoning as
		// copyTemplateViews).
		newTbl.IsMatView = true
		newTbl.IsPopulated = srcMV.IsPopulated
		newTbl.View = srcMV.View
		newTbl.ViewDef = srcMV.ViewDef
		oldRel := storage.RelFileNode{DBOid: srcOid, RelOid: srcMV.OID, Fork: storage.MainFork}
		newRel := storage.RelFileNode{DBOid: newOid, RelOid: newTbl.OID, Fork: storage.MainFork}
		if err := s.copyTemplateRelationFile(oldRel, newRel); err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("copying relation file for materialized view %q: %w", srcMV.Name, err)
		}
		if err := s.syncCopiedTableCatalogHeap(newOid, newTbl); err != nil {
			s.rollbackTemplateCopy(newOid)
			return fmt.Errorf("syncing catalog heap for materialized view %q: %w", srcMV.Name, err)
		}
	}
	return nil
}


// rollbackTemplateCopy removes every table copyTemplateTables has registered
// under newOid so far, undoing a partial copy after a mid-loop failure.
// Best-effort: DropTable's own "does not exist" error can't fire here since
// every name being dropped came straight from AllTables(newOid).
func (s *Server) rollbackTemplateCopy(newOid uint32) {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	for _, t := range im.AllTables(newOid) {
		_ = im.DropTable(parser.ObjectName{Schema: t.Schema, Name: t.Name}, newOid)
	}
	// Sequences are Virtual catalog tables (never returned by AllTables,
	// see resolveCreateDatabaseTemplate's doc comment) plus a separate
	// process-global registry entry (executor.seqRegistry) — mirror DROP
	// SEQUENCE's own pairing (operators_ddl.go) to clean up both.
	for _, si := range executor.AllSequenceInfos(newOid) {
		name := si.Name
		if si.Schema != "" && si.Schema != "public" {
			name = si.Schema + "." + si.Name
		}
		executor.DropSequence(name, newOid)
		_ = im.DropTable(parser.ObjectName{Schema: si.Schema, Name: si.Name}, newOid)
	}
	// Plain views are Virtual catalog tables too (never returned by
	// AllTables, same reason as sequences above) — clean up via AllViews +
	// DropView (mirrors DROP VIEW's own removal path, unlike the DropTable
	// pairing sequences use, since a view was registered via CreateView).
	for _, v := range im.AllViews(newOid) {
		_ = im.DropView(parser.ObjectName{Schema: v.Schema, Name: v.Name}, true, newOid)
	}
}

// copyTemplateRelationFile physically copies oldRel's MainFork data file to
// newRel's location, for CREATE DATABASE ... TEMPLATE's plain-table copy
// (copyTemplateTables). Mirrors internal/executor/operators_ddl.go's
// relocateRelationPhysicalFile copy+fsync discipline, but — unlike that
// tablespace-move helper, which retires oldRel once the move commits — never
// touches oldRel afterward: the source table must stay completely
// unaffected, since the template database keeps living (this is a COPY, not
// a move).
func (s *Server) copyTemplateRelationFile(oldRel, newRel storage.RelFileNode) error {
	if s.cfg.Pool == nil {
		// Embedded/test contexts with no cluster filesystem: nothing to copy.
		return nil
	}
	mgr := s.cfg.Pool.Manager()
	dataDir := mgr.DataDir()
	if dataDir == "" {
		return nil
	}
	if !mgr.Exists(oldRel) {
		// The template's table was never written to physically (e.g.
		// registered directly against the catalog by a test fixture that
		// bypasses the executor, or genuinely empty) — nothing to copy; the
		// new table starts with no physical file, exactly like any other
		// freshly created empty table.
		return nil
	}
	// Flush every buffered write for oldRel to disk before reading it —
	// InvalidateRel would DISCARD dirty pages instead of writing them, wrong
	// here since the copy must see the template's latest committed content.
	if err := s.cfg.Pool.FlushRel(oldRel); err != nil {
		return fmt.Errorf("flush template relation before copy: %w", err)
	}
	oldPath := filepath.Join(dataDir, mgr.RelPath(oldRel))
	newPath := filepath.Join(dataDir, mgr.RelPath(newRel))
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(newPath), err)
	}
	if err := copyRelationFileFsync(oldPath, newPath); err != nil {
		return fmt.Errorf("copy %s to %s: %w", oldPath, newPath, err)
	}
	return nil
}

// copyRelationFileFsync copies src to dst byte-for-byte and fsyncs dst before
// returning, so the copied file is durable on disk before the CREATE
// DATABASE WAL append that makes it authoritative (mirrors
// copyTemplateRelationFile's caller-side ordering). This is database_ddl.go's
// own copy of internal/executor/operators_ddl.go's copyFileFsync (unexported
// in a different package, duplicated here rather than exported solely for
// this one caller).
func copyRelationFileFsync(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// syncCopiedTableCatalogHeap persists newTbl's pg_class/pg_attribute rows
// (plus its columns' DEFAULT-expression WAL snapshot, both via
// executor.SyncTableToCatalogHeap) into newOid's own catalog heap, exactly as
// an ordinary CREATE TABLE issued over a connection to that database would —
// see M0122-0007 4e follow-up 39. copyTemplateTables has no live
// per-connection *executor.Context to reuse here (CREATE DATABASE runs
// outside any user session's transaction), so this builds a minimal one and
// opens/commits its own short-lived internal mvcc transaction — mirroring
// internal/executor/applyworker.go's identical "build a minimal Context, no
// session/planner state" pattern for heap-writing background work that also
// has no client transaction to ride.
func (s *Server) syncCopiedTableCatalogHeap(newOid uint32, tbl *catalog.Table) error {
	if s.cfg.Pool == nil || s.cfg.TxnMgr == nil {
		// No cluster filesystem / transaction manager (some test/embedded
		// paths) — nothing to persist, matching createDatabasePhysicalDirectory's
		// own nil-Pool fallback.
		return nil
	}
	ectx := executor.NewContext()
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	ectx.CurrentDatabaseOid = newOid
	if !executor.CatalogHeapSyncAvailable(ectx) {
		// pg_attribute isn't registered as a real heap-backed table in this
		// catalog (some test/embedded paths, mirrors catalogHeapSyncAvailable's
		// other callers) — nothing to persist; the in-memory catalog
		// registration from copyTemplateTables' CreateTable call already stands.
		return nil
	}
	tx, err := s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		return fmt.Errorf("begin catalog-heap sync transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.cfg.TxnMgr.Rollback(tx)
		}
	}()
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		return fmt.Errorf("snapshot for catalog-heap sync transaction: %w", err)
	}
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.Tx = tx
	ectx.Snap = snap
	ectx.WAL = s.cfg.WAL
	if err := executor.SyncTableToCatalogHeap(ectx, tbl); err != nil {
		return err
	}
	if err := s.cfg.TxnMgr.Commit(tx); err != nil {
		return fmt.Errorf("commit catalog-heap sync transaction: %w", err)
	}
	committed = true
	return nil
}

// actingRoleIsSuperuser reports whether actingRole (see resolveActingRoleOID)
// currently holds the SUPERUSER attribute — the escape hatch dropdb()'s
// object_ownercheck grants a superuser regardless of database ownership.
func (s *Server) actingRoleIsSuperuser(actingRole string) bool {
	oid := s.resolveActingRoleOID(actingRole)
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return oid == catalog.BootstrapSuperuserOID
	}
	return im.IsSuperuser(oid)
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
//
// actingRole is the session's current effective role (connTx.NonSuperuserRole,
// or "" for the bootstrap superuser — the same convention tryRecordTableGrant
// uses, see grant_ddl.go): CREATE DATABASE records it as the new database's
// datdba, and DROP DATABASE requires it to either own the target database or
// be a superuser (mirrors dbcommands.c dropdb()'s object_ownercheck call).
func (s *Server) tryHandleDatabaseDDL(sql string, liveDBName string, actingRole string, resolveCurrent currentGUCResolver) (bool, string, error) {
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
		templateName := createDatabaseTemplateName(sql)
		srcOid, tmplTables, tmplSequences, tmplViews, tmplMatViews, err := s.resolveCreateDatabaseTemplate(templateName)
		if err != nil {
			return true, "", err
		}
		if len(tmplTables) > 0 || len(tmplSequences) > 0 || len(tmplViews) > 0 || len(tmplMatViews) > 0 {
			// PG: dbcommands.c createdb(), CountOtherDBBackends(src_dboid)
			// (ERRCODE_OBJECT_IN_USE) — real PostgreSQL refuses to copy a
			// template while another backend is connected to it (mirrors the
			// DROP DATABASE busy guard further below). Only meaningful once
			// there is real template content to copy: the pre-existing
			// empty-template path (tmplTables/tmplSequences/tmplViews == nil,
			// whether the template genuinely has none or aliases DefaultDBOid)
			// needs no such guard, preserving
			// TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds's behavior.
			if s.cfg.Activity != nil {
				if n := s.cfg.Activity.CountByDatName(templateName); n > 0 {
					return true, "", &databaseDDLError{
						code: sqlstate.ObjectInUse,
						msg:  fmt.Sprintf("source database %q is being accessed by other users", templateName),
					}
				}
			}
		}
		owner := s.resolveActingRoleOID(actingRole)
		oid, err := cat.CreateDatabase(name, owner)
		if err != nil {
			if errors.Is(err, catalog.ErrDatabaseExists) {
				// PG: dbcommands.c createdb(), ERRCODE_DUPLICATE_DATABASE.
				return true, "", &databaseDDLError{
					code: sqlstate.DuplicateDatabase,
					msg:  fmt.Sprintf("database %q already exists", name),
				}
			}
			return true, "", err
		}
		// M0122-0007 physical-storage-isolation slice 2: create base/<oid>
		// (+ PG_VERSION) BEFORE the WAL append that makes the CREATE durable —
		// mirrors relocateRelationPhysicalFile's crash-safety ordering (the
		// physical artifact must exist before the operation that commits to
		// it). A failure here rolls back the catalog allocation exactly like
		// a WAL-append failure does below.
		if err := s.createDatabasePhysicalDirectory(oid); err != nil {
			_ = cat.DropDatabase(name)
			return true, "", err
		}
		if len(tmplTables) > 0 {
			// M0122-0007 4e (last item): copy the template's relation files +
			// register fresh catalog.Table entries under the new dbOid BEFORE
			// the WAL append below makes the CREATE durable — same physical-
			// artifact-before-commit ordering as createDatabasePhysicalDirectory.
			if cerr := s.copyTemplateTables(srcOid, oid, tmplTables); cerr != nil {
				s.rollbackTemplateCopy(oid)
				_ = cat.DropDatabase(name)
				s.removeDatabasePhysicalDirectory(oid)
				return true, "", cerr
			}
		}
		if len(tmplSequences) > 0 {
			// M0122-0007 4e follow-up 41: sequences have no relation file, so
			// this runs independently of the table-copy branch above (a
			// template can contain either, both, or neither).
			if cerr := s.copyTemplateSequences(srcOid, oid, tmplSequences); cerr != nil {
				s.rollbackTemplateCopy(oid)
				_ = cat.DropDatabase(name)
				s.removeDatabasePhysicalDirectory(oid)
				return true, "", cerr
			}
		}
		if len(tmplViews) > 0 {
			// M0122-0007 4e follow-up 42: plain views have no relation file
			// either, so this runs independently of the table/sequence-copy
			// branches above (a template can contain any combination).
			if cerr := s.copyTemplateViews(oid, tmplViews); cerr != nil {
				s.rollbackTemplateCopy(oid)
				_ = cat.DropDatabase(name)
				s.removeDatabasePhysicalDirectory(oid)
				return true, "", cerr
			}
		}
		if len(tmplMatViews) > 0 {
			// M0122-0007 4e follow-up 43: a materialized view needs its
			// relation file physically copied like a plain table, so this
			// runs independently of (but after) the table-copy branch above
			// — same physical-artifact-before-commit ordering.
			if cerr := s.copyTemplateMatViews(srcOid, oid, tmplMatViews); cerr != nil {
				s.rollbackTemplateCopy(oid)
				_ = cat.DropDatabase(name)
				s.removeDatabasePhysicalDirectory(oid)
				return true, "", cerr
			}
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeCreateDatabase(name, owner, oid)); werr != nil {
				// Roll back the catalog change (and any copied template
				// tables) so memory and disk agree.
				s.rollbackTemplateCopy(oid)
				_ = cat.DropDatabase(name)
				s.removeDatabasePhysicalDirectory(oid)
				return true, "", werr
			}
		}
		return true, "", nil
	case databaseDDLDrop:
		// PG's dropdb() (dbcommands.c) runs its guard checks — ownership,
		// template, currently-open, other-backends — AFTER the existence
		// lookup but BEFORE actually removing the pg_database row. goopg's
		// HasDatabase pre-check mirrors that ordering; cat.DropDatabase
		// itself only ever mutates once every guard below has passed.
		if !cat.HasDatabase(name) {
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
		// PG: dropdb(), object_ownercheck (ERRCODE_INSUFFICIENT_PRIVILEGE,
		// aclchk.c's "must be owner of database %s"). Checked immediately
		// after the existence lookup, before every other guard below —
		// matches dbcommands.c's real ordering.
		if owner := cat.DatabaseOwner(name); owner != s.resolveActingRoleOID(actingRole) && !s.actingRoleIsSuperuser(actingRole) {
			return true, "", &databaseDDLError{
				code: sqlstate.InsufficientPrivilege,
				msg:  fmt.Sprintf("must be owner of database %s", name),
			}
		}
		// PG: dropdb(), "Disallow dropping a DB that is marked istemplate"
		// (ERRCODE_WRONG_OBJECT_TYPE). goopg has no ALTER DATABASE ...
		// IS_TEMPLATE, so template0/template1 are permanently templates
		// (mirrors pg_database's own hardcoded datistemplate rendering,
		// catalog.go's pgDatabase.VirtualRows).
		if name == "template0" || name == "template1" {
			return true, "", &databaseDDLError{
				code: sqlstate.WrongObjectType,
				msg:  "cannot drop a template database",
			}
		}
		// PG: dropdb(), "Obviously can't drop my own database"
		// (ERRCODE_OBJECT_IN_USE). Checked before the other-backends
		// count below: once this passes, name != liveDBName is
		// guaranteed, so any backend CountByDatName(name) finds below is
		// necessarily a connection other than this one — no separate
		// self-exclusion bookkeeping needed.
		if name == liveDBName {
			return true, "", &databaseDDLError{
				code: sqlstate.ObjectInUse,
				msg:  "cannot drop the currently open database",
			}
		}
		// PG: dropdb(), "Attempt to terminate all existing connections to
		// the target database if the user has requested to do so"
		// (dbcommands.c, `if (force) TerminateOtherDBBackends(db_id)`). Only
		// WITH (FORCE) exercises this path; a plain DROP DATABASE keeps the
		// pre-existing single immediate busy check below unchanged — out of
		// scope here (see waitForDatabaseBackendsToDrain's doc comment).
		if dropDatabaseHasForce(sql) {
			s.terminateOtherDBBackends(name)
			s.waitForDatabaseBackendsToDrain(name)
		}
		// PG: dropdb(), CountOtherDBBackends() (ERRCODE_OBJECT_IN_USE).
		if s.cfg.Activity != nil {
			if n := s.cfg.Activity.CountByDatName(name); n > 0 {
				return true, "", &databaseDDLError{
					code: sqlstate.ObjectInUse,
					msg:  fmt.Sprintf("database %q is being accessed by other users", name),
				}
			}
		}
		droppedOwner := cat.DatabaseOwner(name)
		// Captured before DropDatabase below, which deletes the catalog's
		// name->oid mapping along with the rest of the entry.
		droppedOid := cat.DatabaseOid(name)
		if err := cat.DropDatabase(name); err != nil {
			if errors.Is(err, catalog.ErrDatabaseNotFound) {
				// Unreachable in practice (HasDatabase above already
				// confirmed existence under the same catalog mutex
				// discipline every other DDL site here uses), kept only
				// as a defensive fallback consistent with the pre-existing
				// error shape.
				return true, "", &databaseDDLError{
					code: sqlstate.UndefinedDatabase,
					msg:  fmt.Sprintf("database %q does not exist", name),
				}
			}
			return true, "", err
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeDropDatabase(name)); werr != nil {
				// Re-create the catalog entry (with its original owner) so
				// the abort is consistent. A fresh oid gets allocated here
				// rather than restoring the dropped one — acceptable for
				// this exceedingly rare double-failure edge case (the
				// DROP's own WAL append failing) since no DropDatabase
				// record was ever durably written either. The re-created
				// entry gets a fresh oid (droppedOid's directory is left
				// as a harmless orphan, matching createDatabasePhysicalDirectory's
				// own rollback tolerance) — removeDatabasePhysicalDirectory
				// is only reached below, past this early return.
				_, _ = cat.CreateDatabase(name, droppedOwner)
				return true, "", werr
			}
		}
		// M0122-0007 physical-storage-isolation slice 3: remove base/<oid>
		// now that the drop is durable (WAL-committed above, or no WAL
		// configured at all) — mirrors createDatabasePhysicalDirectory's
		// crash-safety ordering (the physical artifact is only cleaned up
		// once nothing can roll the operation back anymore). A nil Pool/
		// empty DataDir is a silent no-op, same as the CREATE-side helper.
		s.removeDatabasePhysicalDirectory(droppedOid)
		return true, "", nil
	}
	return false, "", nil
}

// terminateOtherDBBackends signals every backend currently connected to
// database name to terminate — PG's TerminateOtherDBBackends (procarray.c),
// the mechanism behind DROP DATABASE ... WITH (FORCE). Reuses the exact
// process-wide cancelReg.terminateByPID path a peer pg_terminate_backend(pid)
// call goes through (cancel.go), which fires the target connection's root
// context cancel so its serve loop tears the connection down. A backend
// whose PID no longer resolves in cancelReg (already exited) is silently
// skipped, matching upstream's tolerance for a connection racing its own
// disconnect. Unlike upstream, this applies no extra permission check beyond
// the DROP DATABASE ownership/superuser guard already gating the caller —
// the same simplification goopg's pg_terminate_backend already makes
// (expr.go), so a non-superuser DB owner can force-terminate another role's
// backend here exactly as pg_terminate_backend already lets them.
func (s *Server) terminateOtherDBBackends(name string) {
	if s.cfg.Activity == nil || s.cancelReg == nil {
		return
	}
	for _, b := range s.cfg.Activity.Snapshot() {
		if b.DatName != name {
			continue
		}
		pid, err := strconv.ParseUint(b.PID, 10, 32)
		if err != nil {
			continue
		}
		s.cancelReg.terminateByPID(uint32(pid))
	}
}

// waitForDatabaseBackendsToDrain polls CountByDatName(name) for up to 5
// seconds (50 tries * 100ms), mirroring CountOtherDBBackends' own retry loop
// (procarray.c) — the window a just-terminated backend needs to actually
// unregister itself before tryHandleDatabaseDDL's busy check (immediately
// following the caller of this function) runs. Unlike upstream, this is
// only invoked from the WITH (FORCE) path: a plain DROP DATABASE keeps its
// pre-existing single immediate check (no wait), since CountOtherDBBackends'
// unconditional 5s retry-wait is a separate, not-yet-ported behaviour change
// that isn't needed to make FORCE itself work — see M0122-0007 remaining
// items in fix_plan.md.
func (s *Server) waitForDatabaseBackendsToDrain(name string) {
	for tries := 0; tries < 50; tries++ {
		if s.cfg.Activity.CountByDatName(name) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// handleDatabaseDDLBypass runs tryHandleDatabaseDDL against sql and, when it
// applies, writes the resulting NoticeResponse/CommandComplete/ReadyForQuery
// sequence (or the mapped ErrorResponse) directly to w — the simple-query
// wire-response shape shared by both of dispatchSimpleQueryViaExecutor's two
// call sites (the DROP DATABASE pre-parse check and the CREATE/ALTER
// DATABASE parse-failure fallback). Returns handled=false when sql isn't a
// database-DDL bypass form (or the bypass degrades to legacy no-op, e.g. no
// catalog plumbed), so the caller continues its normal dispatch path.
func (s *Server) handleDatabaseDDLBypass(sql, liveDBName, actingRole string, resolveCurrent currentGUCResolver, w *protocol.FrameWriter) (handled bool, err error) {
	handled, notice, herr := s.tryHandleDatabaseDDL(sql, liveDBName, actingRole, resolveCurrent)
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
	CreateDatabase(name string, owner uint32) (uint32, error)
	DropDatabase(name string) error
	HasDatabase(name string) bool
	DatabaseOwner(name string) uint32
	DatabaseOid(name string) uint32
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

// databaseTemplateRegistry is the subset of catalog.Catalog CREATE
// DATABASE's TEMPLATE handling needs to resolve and inspect the source
// database. A separate interface from databaseRegistry for the same reason
// databaseConnLimitRegistry is separate (see its own doc comment): a catalog
// fake implementing CreateDatabase/DropDatabase/HasDatabase but not these
// two methods must not silently lose the TEMPLATE checks.
type databaseTemplateRegistry interface {
	ResolveDatabaseOid(name string) (uint32, bool)
	AllTables(dbOid ...uint32) []*catalog.Table
	// AllIndexes reports every index registered under dbOid, so
	// resolveCreateDatabaseTemplate can reject a template containing ANY
	// index outright (this bounded slice implements no index-file cloning).
	AllIndexes(dbOid ...uint32) []*catalog.Index
	// AllViews reports every plain (non-materialized) view registered under
	// dbOid — AllTables can never see one (its pg_class row is always
	// Virtual), so resolveCreateDatabaseTemplate needs this separate surface
	// to detect and collect them for copyTemplateViews (M0122-0007 4e
	// follow-up 42, sibling of AllSequenceInfos' role for sequences).
	AllViews(dbOid ...uint32) []*catalog.Table
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
