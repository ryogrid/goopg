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
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// databaseDDLKind is the result of inspecting a SQL string for a
// CREATE / DROP DATABASE prefix.
type databaseDDLKind int

const (
	databaseDDLNone databaseDDLKind = iota
	databaseDDLCreate
	databaseDDLDrop
)

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
// for a CREATE/DROP DATABASE statement. Mirrors upstream's tag.
func databaseDDLCommandTag(sql string) string {
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

// tryHandleDatabaseDDL returns (handled, err). When handled is true
// the dispatch path should NOT fall through to compatNoopCommandTag.
//
//   - handled=true,  err=nil   → CommandComplete should be written
//   - handled=true,  err!=nil  → an ErrorResponse should be written
//   - handled=false, err=nil   → not a database DDL; continue dispatch
//
// The catalog mutation happens BEFORE the WAL append; if the WAL
// append fails the catalog mutation is rolled back so the on-disk
// state and in-memory state stay consistent.
func (s *Server) tryHandleDatabaseDDL(sql string) (bool, error) {
	kind, name := classifyDatabaseDDL(sql)
	if kind == databaseDDLNone {
		return false, nil
	}
	if name == "" {
		return true, errors.New("missing database name")
	}
	if s.cfg.Catalog == nil {
		// No catalog plumbed (some test/embedded paths). Fall back to
		// the legacy no-op so behaviour is unchanged.
		return false, nil
	}
	cat, ok := s.cfg.Catalog.(databaseRegistry)
	if !ok {
		// Catalog implementation does not expose the database
		// registry surface yet — preserve legacy no-op behaviour.
		return false, nil
	}
	switch kind {
	case databaseDDLCreate:
		if err := cat.CreateDatabase(name); err != nil {
			if errors.Is(err, catalog.ErrDatabaseExists) {
				// PostgreSQL returns "database already exists" with
				// SQLSTATE 42P04. Surface the same error text but
				// route through the generic system-error path; the
				// caller wraps SQLSTATE.
				return true, err
			}
			return true, err
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeCreateDatabase(name)); werr != nil {
				// Roll back the catalog change so memory and disk agree.
				_ = cat.DropDatabase(name)
				return true, werr
			}
		}
		return true, nil
	case databaseDDLDrop:
		if err := cat.DropDatabase(name); err != nil {
			if errors.Is(err, catalog.ErrDatabaseNotFound) {
				// IF EXISTS branch was already accepted by the prefix
				// match; the executor must not surface "not found"
				// when the user said IF EXISTS. Inspect the SQL again.
				lower := strings.ToLower(strings.TrimSpace(sql))
				if strings.HasPrefix(lower, "drop database if exists ") {
					return true, nil
				}
			}
			return true, err
		}
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeDropDatabase(name)); werr != nil {
				// Re-create the catalog entry so the abort is consistent.
				_ = cat.CreateDatabase(name)
				return true, werr
			}
		}
		return true, nil
	}
	return false, nil
}

// databaseRegistry is the subset of catalog.Catalog the database-DDL
// handler needs. catalog.InMemory satisfies this interface; alternate
// implementations (e.g. tests) opt in by exposing the same methods.
type databaseRegistry interface {
	CreateDatabase(name string) error
	DropDatabase(name string) error
	HasDatabase(name string) bool
}
