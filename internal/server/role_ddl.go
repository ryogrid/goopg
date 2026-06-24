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

// splitLeadingRoleDDL splits a multi-statement batch whose FIRST statement is a
// CREATE/DROP ROLE/USER/GROUP command — the forms the parser does not yet
// recognise, so the whole batch reaches the parse-failure recovery path. It
// returns (firstStmt, remainder, true) when the leading statement is role DDL
// AND there is at least one more statement after it; otherwise (… , false).
//
// Without this, dispatch's single-statement role-DDL intercept handles the
// WHOLE batch (e.g. "CREATE ROLE x; CREATE TABLE y") as just the CREATE ROLE
// and silently drops the trailing CREATE TABLE — the failure the *-conflict
// isolation specs' setup blocks hit (their setup is one batch). M0118-0008.
func splitLeadingRoleDDL(sql string) (first, rest string, ok bool) {
	end := firstTopLevelSemicolon(sql)
	if end < 0 {
		return "", "", false // single statement; let the normal intercept handle it
	}
	first = sql[:end]
	rest = strings.TrimSpace(sql[end+1:])
	if rest == "" {
		return "", "", false // trailing ';' only — not a real second statement
	}
	norm := normalizeCompatSQL(first)
	switch {
	case strings.HasPrefix(norm, "create role "), strings.HasPrefix(norm, "create user "),
		strings.HasPrefix(norm, "create group "),
		strings.HasPrefix(norm, "drop role "), strings.HasPrefix(norm, "drop user "),
		strings.HasPrefix(norm, "drop group "):
		return first, rest, true
	}
	return "", "", false
}

// firstTopLevelSemicolon returns the byte index of the first ';' that is not
// inside a single-/double-quoted string, a dollar-quoted string, or a comment.
// Returns -1 when there is no such separator.
func firstTopLevelSemicolon(sql string) int {
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ';':
			return i
		case c == '\'' || c == '"':
			// Skip a quoted string; doubled quote is an escaped quote.
			q := c
			i++
			for i < n {
				if sql[i] == q {
					if i+1 < n && sql[i+1] == q {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// Line comment to end of line.
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			// Block comment (PostgreSQL block comments do not nest in this
			// simplified scan; role/DDL setup never relies on nesting).
			i += 2
			for i < n && !(sql[i] == '*' && i+1 < n && sql[i+1] == '/') {
				i++
			}
			i += 2
		case c == '$':
			// Dollar-quoted string: $tag$ … $tag$.
			if tag, after, isDollar := scanDollarTag(sql, i); isDollar {
				if rel := strings.Index(sql[after:], tag); rel >= 0 {
					i = after + rel + len(tag)
					continue
				}
				return -1 // unterminated dollar quote — no top-level separator
			}
			i++
		default:
			i++
		}
	}
	return -1
}

// scanDollarTag recognises a dollar-quote opening tag ($tag$ or $$) starting at
// sql[i]. On success it returns the full tag text, the index just past it, and
// true. Tags are $ + optional identifier chars + $.
func scanDollarTag(sql string, i int) (tag string, after int, ok bool) {
	n := len(sql)
	if i >= n || sql[i] != '$' {
		return "", 0, false
	}
	j := i + 1
	for j < n {
		ch := sql[j]
		if ch == '$' {
			return sql[i : j+1], j + 1, true
		}
		if ch != '_' && !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') &&
			!(ch >= '0' && ch <= '9') {
			return "", 0, false
		}
		j++
	}
	return "", 0, false
}

// roleErrorSQLState returns the SQLSTATE for role-related errors.
// PostgreSQL uses 42704 (undefined_object) for "role does not exist".
func roleErrorSQLState(err error) sqlstate.Code {
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		return sqlstate.UndefinedObject
	}
	return sqlstate.SystemError
}
