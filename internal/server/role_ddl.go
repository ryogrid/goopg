package server

// role_ddl.go — in-process CREATE ROLE / DROP ROLE handler (M0095-0006).
//
// goopg's parser does not yet include a full CREATE/DROP ROLE grammar, so
// these statements reach the wire layer as parse failures. tryHandleRoleDDL
// intercepts them before the generic compatNoopCommandTag path so that:
//   - CREATE ROLE / CREATE USER: registers the role name in the server's
//     in-memory role set (Server.roles) and returns success.
//   - DROP ROLE / DROP USER: checks the role set; returns an error if the
//     role does not exist and IF EXISTS was not specified.
//
// Role state is in-memory only for v0; it does not survive a server restart
// and is not written to the pg_auth file.

import (
	"strings"

	"github.com/goopg/goopg/internal/sqlstate"
)

// tryHandleRoleDDL returns (handled, err).
//   - handled=true, err=nil:   statement was handled successfully
//   - handled=true, err!=nil:  statement was handled but failed (e.g. role not found)
//   - handled=false, err=nil:  not a role DDL statement; caller should continue
func (s *Server) tryHandleRoleDDL(sql string) (bool, error) {
	norm := normalizeCompatSQL(sql)
	switch {
	case strings.HasPrefix(norm, "create role "), strings.HasPrefix(norm, "create user "),
		strings.HasPrefix(norm, "create group "):
		name := roleNameFromCreate(norm)
		if name == "" {
			return false, nil // malformed; let caller handle
		}
		s.registerRole(name)
		// Also register in catalog so executor-level DROP ROLE IF EXISTS can check.
		if s.cfg.Catalog != nil {
			s.cfg.Catalog.RegisterRole(name)
		}
		return true, nil

	case strings.HasPrefix(norm, "drop role "), strings.HasPrefix(norm, "drop user "),
		strings.HasPrefix(norm, "drop group "):
		name, ifExists := roleNameFromDrop(norm)
		if name == "" {
			return false, nil // malformed; let caller handle
		}
		if err := s.unregisterRole(name, ifExists); err != nil {
			return true, err
		}
		// Also unregister from catalog.
		if s.cfg.Catalog != nil {
			s.cfg.Catalog.UnregisterRole(name)
		}
		return true, nil
	}
	return false, nil
}

// roleNameFromCreate extracts the role name from a normalised CREATE ROLE/USER statement.
// The input is already lower-cased and trimmed.
func roleNameFromCreate(norm string) string {
	var rest string
	switch {
	case strings.HasPrefix(norm, "create role "):
		rest = strings.TrimSpace(norm[len("create role "):])
	case strings.HasPrefix(norm, "create user "):
		rest = strings.TrimSpace(norm[len("create user "):])
	case strings.HasPrefix(norm, "create group "):
		rest = strings.TrimSpace(norm[len("create group "):])
	default:
		return ""
	}
	// Skip optional WITH keyword.
	if rest == "with" || strings.HasPrefix(rest, "with ") {
		rest = strings.TrimSpace(rest[4:])
	}
	return extractFirstSQLIdent(norm, rest)
}

// roleNameFromDrop extracts (name, ifExists) from a normalised DROP ROLE/USER statement.
func roleNameFromDrop(norm string) (name string, ifExists bool) {
	var rest string
	switch {
	case strings.HasPrefix(norm, "drop role "):
		rest = strings.TrimSpace(norm[len("drop role "):])
	case strings.HasPrefix(norm, "drop user "):
		rest = strings.TrimSpace(norm[len("drop user "):])
	case strings.HasPrefix(norm, "drop group "):
		rest = strings.TrimSpace(norm[len("drop group "):])
	default:
		return "", false
	}
	if strings.HasPrefix(rest, "if exists ") {
		ifExists = true
		rest = strings.TrimSpace(rest[len("if exists "):])
	}
	name = extractFirstSQLIdent(norm, rest)
	return name, ifExists
}

// extractFirstSQLIdent extracts the first SQL identifier (quoted or unquoted)
// from rest.  norm is used only for error context; it is not otherwise needed.
// Returns "" on failure.
func extractFirstSQLIdent(_ string, rest string) string {
	if rest == "" {
		return ""
	}
	// Double-quoted identifier: preserves internal case after lower-casing
	// by normalizeCompatSQL, so "RegresS" becomes "regress".
	if rest[0] == '"' {
		// Find closing quote.
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return rest[1:] // unbalanced quote — take the rest
		}
		return rest[1 : end+1]
	}
	// Unquoted identifier: take up to first space/semicolon.
	end := strings.IndexAny(rest, " \t\n\r;,")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// roleErrorSQLState returns the SQLSTATE for role-related errors.
// PostgreSQL uses 42704 (undefined_object) for "role does not exist".
func roleErrorSQLState(err error) sqlstate.Code {
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		return sqlstate.UndefinedObject
	}
	return sqlstate.SystemError
}
