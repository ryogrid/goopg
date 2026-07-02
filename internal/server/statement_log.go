package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/config"
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

// effectiveLogStatementLevel is the louder of the global GOOPG_LOG_STATEMENT
// toggle (s.logStmtLevel) and the session's `log_statement` GUC (a client
// `SET log_statement = 'all'`), mirroring PostgreSQL's single log_statement
// GUC by treating the env var as this server's boot-time default for it. sess
// may be nil (e.g. a connection not yet past startup); the env level alone
// then applies.
func (s *Server) effectiveLogStatementLevel(sess *config.SessionRegistry) logStatementLevel {
	lvl := s.logStmtLevel
	if sess == nil {
		return lvl
	}
	if _, eff, ok := sess.Get("log_statement"); ok {
		if sessLvl, valid := parseLogStatementLevel(eff); valid && sessLvl > lvl {
			lvl = sessLvl
		}
	}
	return lvl
}

// logStatement emits a single per-statement log line when the configured
// GOOPG_LOG_STATEMENT level or the session's `log_statement` GUC (whichever
// is louder) admits it. proto is "simple" or "extended". connTx may be nil;
// when it holds an explicit transaction with an assigned xid, the xid is
// attached so a verification run can group a client's queries by
// transaction. This is a no-op (one enum compare, plus one session lookup)
// when logging is disabled. The return value is whether a "statement: ..."
// line was actually emitted — logDuration's was_logged parameter, so a
// later duration line doesn't repeat the statement text PG already logged.
func (s *Server) logStatement(proto string, sql string, sess *config.SessionRegistry, connTx *connTxState) bool {
	lvl := s.effectiveLogStatementLevel(sess)
	if lvl == logStmtNone {
		return false
	}
	if lvl != logStmtAll && !lvl.shouldLog(leadingKeyword(sql)) {
		return false
	}
	attrs := []any{"proto", proto, "statement", strings.TrimSpace(sql)}
	if connTx != nil && connTx.InExplicit() {
		if tx := connTx.Tx(); tx.XID != 0 {
			attrs = append(attrs, "xid", uint64(tx.XID))
		}
	}
	s.cfg.Logger.Info("statement", attrs...)
	return true
}

// sessionLogMinDurationStatement reads the effective `log_min_duration_statement`
// GUC from the session and returns it in milliseconds. Unlike
// sessionStatementTimeout (dispatch.go), 0 is a meaningful value here ("log
// every statement's duration") distinct from disabled, so the sentinel for
// disabled/missing/unparseable is -1 — matching the GUC's own BootVal
// (internal/config/defaults.go).
func sessionLogMinDurationStatement(sess *config.SessionRegistry) int64 {
	if sess == nil {
		return -1
	}
	_, eff, ok := sess.Get("log_min_duration_statement")
	if !ok {
		return -1
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || ms < 0 {
		return -1
	}
	return ms
}

// exceedsLogMinDuration mirrors PostgreSQL's check_log_duration threshold
// comparison (postgres/src/backend/tcop/postgres.c) for
// log_min_duration_statement: thresholdMs<0 disables duration logging
// entirely (already filtered by sessionLogMinDurationStatement's -1
// sentinel, but kept explicit here since this is the semantic boundary),
// thresholdMs==0 logs every statement's duration, and thresholdMs>0 requires
// the statement to have run at least that many milliseconds.
func exceedsLogMinDuration(elapsedMs float64, thresholdMs int64) bool {
	if thresholdMs < 0 {
		return false
	}
	return thresholdMs == 0 || elapsedMs >= float64(thresholdMs)
}

// logDuration emits PostgreSQL's `log_min_duration_statement` duration line
// (check_log_duration, postgres.c) after a statement finishes. wasLogged is
// logStatement's return value for the same statement: when true, only a bare
// "duration: X ms" line is emitted (the statement text was already logged);
// when false, a combined "duration: X ms  statement: Y" line is emitted
// instead, exactly as PG avoids double-printing the statement text. A no-op
// when log_min_duration_statement is unset (default -1) or not yet
// exceeded.
func (s *Server) logDuration(start time.Time, wasLogged bool, proto string, sql string, sess *config.SessionRegistry, connTx *connTxState) {
	threshold := sessionLogMinDurationStatement(sess)
	if threshold < 0 {
		return
	}
	elapsedMs := float64(time.Since(start)) / float64(time.Millisecond)
	if !exceedsLogMinDuration(elapsedMs, threshold) {
		return
	}
	attrs := []any{"proto", proto, "duration_ms", elapsedMs}
	if !wasLogged {
		attrs = append(attrs, "statement", strings.TrimSpace(sql))
	}
	if connTx != nil && connTx.InExplicit() {
		if tx := connTx.Tx(); tx.XID != 0 {
			attrs = append(attrs, "xid", uint64(tx.XID))
		}
	}
	s.cfg.Logger.Info("duration", attrs...)
}
