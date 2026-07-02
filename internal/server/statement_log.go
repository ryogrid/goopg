package server

import (
	"strings"
)

// logStatementLevel mirrors PostgreSQL's log_statement enum
// (none < ddl < mod < all). See
// postgres/src/backend/tcop/postgres.c:check_log_statement and
// docs/design/root-0023-statement-query-logging.md.
type logStatementLevel int

const (
	logStmtNone logStatementLevel = iota
	logStmtDDL
	logStmtMod
	logStmtAll
)

// parseLogStatementLevel maps a GOOPG_LOG_STATEMENT / log_statement string to a
// level. The second return is false for an unrecognised value (the caller
// treats that as logStmtNone and warns); an empty string is a valid "none".
func parseLogStatementLevel(s string) (logStatementLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return logStmtNone, true
	case "ddl":
		return logStmtDDL, true
	case "mod":
		return logStmtMod, true
	case "all":
		return logStmtAll, true
	default:
		return logStmtNone, false
	}
}

// leadingKeyword returns the upper-cased first SQL keyword of a statement,
// skipping leading whitespace and line/block comments. It is a cheap
// classifier good enough to bucket a statement into ddl / write-dml / other;
// exact node-tag classification (as PostgreSQL does) is unnecessary because the
// primary use (GOOPG_LOG_STATEMENT=all) logs unconditionally.
func leadingKeyword(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
				continue
			}
			return ""
		}
		break
	}
	// First whitespace- or '('-delimited token.
	end := len(s)
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '(' {
			end = i
			break
		}
	}
	return strings.ToUpper(s[:end])
}

// ddlKeywords are the statement-leading verbs PostgreSQL classifies as DDL for
// log_statement=ddl (mirrors the T_*Stmt set treated as ddl in
// check_log_statement — the common cases WordPress and pg_dump emit).
var ddlKeywords = map[string]struct{}{
	"CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {}, "COMMENT": {},
	"GRANT": {}, "REVOKE": {}, "SECURITY": {}, "IMPORT": {}, "REFRESH": {},
	"CLUSTER": {}, "REINDEX": {}, "REASSIGN": {},
}

// writeDMLKeywords are the data-modifying verbs added at log_statement=mod
// (in addition to every ddl statement). COPY is included because COPY ... FROM
// modifies data; a read-only COPY ... TO is rare from these clients.
var writeDMLKeywords = map[string]struct{}{
	"INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {}, "COPY": {},
}

// shouldLog decides whether a statement with the given leading keyword is
// logged at the configured level. mod is a superset of ddl, matching PG.
func (lvl logStatementLevel) shouldLog(kw string) bool {
	switch lvl {
	case logStmtAll:
		return true
	case logStmtMod:
		if _, ok := writeDMLKeywords[kw]; ok {
			return true
		}
		_, ok := ddlKeywords[kw]
		return ok
	case logStmtDDL:
		_, ok := ddlKeywords[kw]
		return ok
	default: // logStmtNone
		return false
	}
}

// logStatement emits a single per-statement log line when the configured
// GOOPG_LOG_STATEMENT level admits it. proto is "simple" or "extended". connTx
// may be nil; when it holds an explicit transaction with an assigned xid, the
// xid is attached so a verification run can group a client's queries by
// transaction. This is a no-op (one enum compare) when logging is disabled.
func (s *Server) logStatement(proto string, sql string, connTx *connTxState) {
	if s.logStmtLevel == logStmtNone {
		return
	}
	if s.logStmtLevel != logStmtAll && !s.logStmtLevel.shouldLog(leadingKeyword(sql)) {
		return
	}
	attrs := []any{"proto", proto, "statement", strings.TrimSpace(sql)}
	if connTx != nil && connTx.InExplicit() {
		if tx := connTx.Tx(); tx.XID != 0 {
			attrs = append(attrs, "xid", uint64(tx.XID))
		}
	}
	s.cfg.Logger.Info("statement", attrs...)
}
