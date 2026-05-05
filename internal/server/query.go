package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// oidInt4 / oidText are pg_type.oids pinned to upstream's pg_type.dat
// entries (postgres/src/include/catalog/pg_type.dat).
const (
	oidInt4 = 23
	oidText = 25
)

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
func (s *Server) handleQuery(ctx context.Context, w *protocol.FrameWriter, sess *config.SessionRegistry, payload []byte) error {
	q, err := extractCString(payload)
	if err != nil {
		return s.writeQueryError(w, sqlstate.ProtocolViolation,
			fmt.Sprintf("malformed Query message: %v", err))
	}
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return err
		}
		return w.WriteReadyForQuery(protocol.TxStatusIdle)
	}

	matchable := strings.TrimRight(trimmed, ";")
	matchable = strings.TrimSpace(matchable)

	if strings.EqualFold(matchable, "SELECT 1") {
		return s.respondSelectOne(w)
	}

	upper := strings.ToUpper(matchable)
	switch {
	case upper == "SHOW ALL":
		return s.handleShowAll(w, sess)
	case strings.HasPrefix(upper, "SHOW "):
		name := strings.TrimSpace(matchable[len("SHOW "):])
		if strings.EqualFold(name, "ALL") {
			return s.handleShowAll(w, sess)
		}
		return s.handleShow(w, sess, name)
	case strings.HasPrefix(upper, "SET LOCAL "):
		return s.handleSet(w, sess, matchable[len("SET LOCAL "):], true)
	case strings.HasPrefix(upper, "SET "):
		return s.handleSet(w, sess, matchable[len("SET "):], false)
	case upper == "RESET ALL":
		sess.ResetAll()
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.WriteReadyForQuery(protocol.TxStatusIdle)
	case strings.HasPrefix(upper, "RESET "):
		name := strings.TrimSpace(matchable[len("RESET "):])
		if err := sess.Reset(name); err != nil {
			return s.writeQueryError(w, sqlstate.UndefinedObject, err.Error())
		}
		if err := w.WriteCommandComplete("RESET"); err != nil {
			return err
		}
		return w.WriteReadyForQuery(protocol.TxStatusIdle)
	}

	if s.cfg.hasStorage() {
		return s.dispatchSimpleQueryViaExecutor(ctx, w, sess, trimmed)
	}

	return s.writeQueryError(w, sqlstate.FeatureNotSupported,
		fmt.Sprintf("query not supported by goopg v0: %q "+
			"(only SELECT 1 / SHOW / SET / RESET are recognised until storage is wired via -D)", trimmed))
}

// handleShow returns the value of one variable as a single text column
// named after the variable. Matches upstream's SHOW behaviour.
func (s *Server) handleShow(w *protocol.FrameWriter, sess *config.SessionRegistry, name string) error {
	name = strings.Trim(name, " \"'")
	v, eff, ok := sess.Get(name)
	if !ok {
		return s.writeQueryError(w, sqlstate.UndefinedObject,
			fmt.Sprintf("unrecognized configuration parameter %q", name))
	}
	if err := w.WriteRowDescription([]protocol.FieldDescription{{
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
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// handleShowAll returns every variable as (name, setting). Upstream also
// emits a description column; we include name + setting only and leave
// description for milestone 5 (catalog) work.
func (s *Server) handleShowAll(w *protocol.FrameWriter, sess *config.SessionRegistry) error {
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	rows := sess.All()
	for _, kv := range rows {
		if err := w.WriteDataRow([][]byte{[]byte(kv.Name), []byte(kv.Value)}); err != nil {
			return err
		}
	}
	if err := w.WriteCommandComplete("SHOW"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// handleSet applies a SET / SET LOCAL statement. Body is the text after
// the keyword: "name = value", "name TO value", or "name value".
func (s *Server) handleSet(w *protocol.FrameWriter, sess *config.SessionRegistry, body string, isLocal bool) error {
	name, value, ok := splitSet(body)
	if !ok {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			fmt.Sprintf("could not parse SET statement: %q", body))
	}
	if err := sess.Set(name, value, isLocal); err != nil {
		return s.writeQueryError(w, sqlstate.InvalidParameterValue, err.Error())
	}
	if err := w.WriteCommandComplete("SET"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
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

func (s *Server) respondSelectOne(w *protocol.FrameWriter) error {
	if err := w.WriteRowDescription([]protocol.FieldDescription{{
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
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// writeQueryError emits an ErrorResponse with the given SQLSTATE plus a
// trailing ReadyForQuery, matching how upstream finishes a failed simple
// Query (the parse error is reported and the connection stays open).
// extra fields (e.g. FieldPosition) are appended after the standard set.
func (s *Server) writeQueryError(w *protocol.FrameWriter, code sqlstate.Code, msg string, extra ...protocol.ErrorField) error {
	fields := []protocol.ErrorField{
		{Code: protocol.FieldSeverity, Value: "ERROR"},
		{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
		{Code: protocol.FieldSQLState, Value: string(code)},
		{Code: protocol.FieldMessage, Value: msg},
		{Code: protocol.FieldRoutine, Value: "server.handleQuery"},
	}
	fields = append(fields, extra...)
	if err := w.WriteErrorResponse(fields); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
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
