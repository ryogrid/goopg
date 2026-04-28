package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// oidInt4 is pg_type.oid for int4. Pinned to upstream's pg_type.dat entry
// (postgres/src/include/catalog/pg_type.dat: int4 oid => 23).
const oidInt4 = 23

// handleQuery implements the simple Query path for v0. The real parser
// arrives in milestone 6; until then we recognise exactly two shapes:
//
//   - SELECT 1                    → single int4 column "?column?", value "1"
//   - <empty / whitespace only>   → EmptyQueryResponse
//
// Anything else returns a SyntaxError ErrorResponse, mirroring how upstream
// behaves when the parser rejects a statement. Either way we finish with
// ReadyForQuery('I') so the client can send the next message.
func (s *Server) handleQuery(w *protocol.FrameWriter, payload []byte) error {
	// payload is "<query string>\0" per protocol.h:Query message format.
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

	// Strip a single trailing semicolon for the v0 matcher.
	matchable := strings.TrimRight(trimmed, ";")
	matchable = strings.TrimSpace(matchable)
	if strings.EqualFold(matchable, "SELECT 1") {
		return s.respondSelectOne(w)
	}

	return s.writeQueryError(w, sqlstate.FeatureNotSupported,
		fmt.Sprintf("query not supported by goopg v0: %q "+
			"(only \"SELECT 1\" is recognised until the parser lands)", trimmed))
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
func (s *Server) writeQueryError(w *protocol.FrameWriter, code sqlstate.Code, msg string) error {
	if err := w.WriteErrorResponse([]protocol.ErrorField{
		{Code: protocol.FieldSeverity, Value: "ERROR"},
		{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
		{Code: protocol.FieldSQLState, Value: string(code)},
		{Code: protocol.FieldMessage, Value: msg},
		{Code: protocol.FieldRoutine, Value: "server.handleQuery"},
	}); err != nil {
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
