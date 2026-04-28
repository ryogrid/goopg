package server

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

type copyInState struct {
	rows       int64
	partialRow bool
}

func (s *Server) handleQueryOrCopy(w *protocol.FrameWriter, sess *config.SessionRegistry, payload []byte) (*copyInState, error) {
	q, err := extractCString(payload)
	if err != nil {
		if err := s.writeQueryError(w, sqlstate.ProtocolViolation,
			fmt.Sprintf("malformed Query message: %v", err)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	_, matchable, upper, empty := normalizeSimpleQuery(q)
	if empty || !strings.HasPrefix(upper, "COPY ") {
		if err := s.handleQuery(w, sess, payload); err != nil {
			return nil, err
		}
		return nil, nil
	}

	switch {
	case strings.HasSuffix(upper, "TO STDOUT"):
		return s.handleCopyToStdoutQuery(w, matchable)
	case strings.Contains(upper, " FROM STDIN"):
		if err := w.WriteCopyInResponse(0, nil); err != nil {
			return nil, err
		}
		return &copyInState{}, nil
	default:
		if err := s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("COPY shape not supported by goopg v0: %q", matchable)); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func (s *Server) handleCopyToStdoutQuery(w *protocol.FrameWriter, matchable string) (*copyInState, error) {
	if !strings.EqualFold(matchable, "COPY (SELECT 1) TO STDOUT") {
		if err := s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("COPY TO STDOUT shape not supported by goopg v0: %q", matchable)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := w.WriteCopyOutResponse(0, nil); err != nil {
		return nil, err
	}
	if err := w.WriteCopyData([]byte("1\n")); err != nil {
		return nil, err
	}
	if err := w.WriteCopyDone(); err != nil {
		return nil, err
	}
	if err := w.WriteCommandComplete("COPY 1"); err != nil {
		return nil, err
	}
	if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleCopyInFrame(w *protocol.FrameWriter, st *copyInState, f protocol.Frame) (bool, error) {
	switch f.Type {
	case protocol.MsgCopyData:
		st.consumeTextCopyData(f.Payload)
		return false, nil
	case protocol.MsgCopyDone:
		if st.partialRow {
			st.rows++
			st.partialRow = false
		}
		if err := w.WriteCommandComplete(fmt.Sprintf("COPY %d", st.rows)); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	case protocol.MsgCopyFail:
		msg, err := extractCString(f.Payload)
		if err != nil || msg == "" {
			msg = "COPY from stdin aborted by frontend"
		}
		if err := w.WriteErrorResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "ERROR"},
			{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
			{Code: protocol.FieldSQLState, Value: string(sqlstate.QueryCanceled)},
			{Code: protocol.FieldMessage, Value: msg},
			{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
		}); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	case protocol.MsgFlush:
		return false, nil
	case protocol.MsgTerminate:
		return true, nil
	default:
		if err := w.WriteErrorResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "ERROR"},
			{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
			{Code: protocol.FieldSQLState, Value: string(sqlstate.ProtocolViolation)},
			{Code: protocol.FieldMessage, Value: fmt.Sprintf("unexpected message %q in COPY FROM STDIN", f.Type)},
			{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
		}); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	}
}

func (st *copyInState) consumeTextCopyData(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	for _, b := range chunk {
		if b == '\n' {
			st.rows++
		}
	}
	st.partialRow = chunk[len(chunk)-1] != '\n'
}
