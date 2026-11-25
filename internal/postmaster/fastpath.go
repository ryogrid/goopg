package postmaster

// Fast-path function calls: the frontend FunctionCall message ('F') and the
// backend FunctionCallResponse ('V'). Mirrors postgres/src/backend/tcop/
// fastpath.c (HandleFunctionRequest, parse_fcall_arguments,
// SendFunctionResult) and the PqMsg_FunctionCall arm of PostgresMain.
//
// libpq's large-object client API (lo_open / loread / lowrite / lo_close /
// lo_creat / lo_unlink / lo_lseek[64] / lo_tell[64] / lo_truncate[64], which
// psql's \lo_import, \lo_export and \lo_unlink drive) is the main user: it
// resolves the function OIDs from pg_proc once (lo_initialize) and then
// calls them with PQfn, i.e. this message.
//
// PG calls the function directly through fmgr. goopg has no fmgr, so the call
// is lowered to `SELECT pg_catalog.<fn>(<typed literals>)` and run through the
// ordinary simple-query path against a capture writer. That path already
// owns what HandleFunctionRequest relies on: the implicit-transaction /
// transaction-block state (start_xact_command / finish_xact_command), the
// "current transaction is aborted" refusal, snapshots, and error / notice
// reporting. The captured stream is then rewritten: notices and errors pass
// through unchanged, the single result cell becomes the 'V' payload (in the
// requested format), and the captured ReadyForQuery ends the exchange.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// MsgFunctionCall / MsgFunctionCallResponse are PqMsg_FunctionCall and
// PqMsg_FunctionCallResponse (postgres/src/include/libpq/protocol.h).
const (
	msgFunctionCall         byte = 'F'
	msgFunctionCallResponse byte = 'V'
)

// fastpathCall is a decoded FunctionCall message.
type fastpathCall struct {
	fnOID   uint32
	formats []int16  // per-argument format codes, already expanded to nargs
	args    [][]byte // nil = SQL NULL
	rformat int16
}

// parseFastpathCall decodes the FunctionCall payload (parse_fcall_arguments):
// Int32 function OID, Int16 format-code count + codes, Int16 argument count,
// then per argument Int32 length (-1 = NULL) + bytes, and the Int16 result
// format. A format-code count must be 0 (all text), 1 (one code for every
// argument) or equal to the argument count.
func parseFastpathCall(p []byte) (*fastpathCall, error) {
	rd := func(n int) ([]byte, error) {
		if len(p) < n {
			return nil, fmt.Errorf("insufficient data left in message")
		}
		b := p[:n]
		p = p[n:]
		return b, nil
	}
	b, err := rd(4)
	if err != nil {
		return nil, err
	}
	c := &fastpathCall{fnOID: binary.BigEndian.Uint32(b)}
	if b, err = rd(2); err != nil {
		return nil, err
	}
	nfmt := int(int16(binary.BigEndian.Uint16(b)))
	if nfmt < 0 {
		return nil, fmt.Errorf("invalid argument format count %d", nfmt)
	}
	fmts := make([]int16, nfmt)
	for i := range fmts {
		if b, err = rd(2); err != nil {
			return nil, err
		}
		fmts[i] = int16(binary.BigEndian.Uint16(b))
	}
	if b, err = rd(2); err != nil {
		return nil, err
	}
	nargs := int(int16(binary.BigEndian.Uint16(b)))
	if nargs < 0 {
		return nil, fmt.Errorf("invalid argument count %d", nargs)
	}
	if nfmt > 1 && nfmt != nargs {
		return nil, fmt.Errorf("function call message contains %d argument formats but %d arguments", nfmt, nargs)
	}
	c.formats = make([]int16, nargs)
	c.args = make([][]byte, nargs)
	for i := 0; i < nargs; i++ {
		switch nfmt {
		case 0:
		case 1:
			c.formats[i] = fmts[0]
		default:
			c.formats[i] = fmts[i]
		}
		if b, err = rd(4); err != nil {
			return nil, err
		}
		n := int32(binary.BigEndian.Uint32(b))
		if n == -1 {
			continue
		}
		if n < 0 {
			return nil, fmt.Errorf("invalid argument size %d in function call message", n)
		}
		if b, err = rd(int(n)); err != nil {
			return nil, err
		}
		c.args[i] = append([]byte{}, b...)
	}
	if b, err = rd(2); err != nil {
		return nil, err
	}
	c.rformat = int16(binary.BigEndian.Uint16(b))
	if len(p) != 0 {
		return nil, fmt.Errorf("invalid message format")
	}
	return c, nil
}

// fastpathArgLiteral renders one argument as a SQL literal cast to its
// declared type. Text-format arguments go through the type's input function
// (the cast); binary-format ones are decoded here with the typreceive rules
// of the types the fast-path callers use.
func fastpathArgLiteral(typ string, format int16, v []byte) (string, error) {
	cast := "::" + typ
	if v == nil {
		return "NULL" + cast, nil
	}
	switch format {
	case 0:
		return pgQuoteLiteralText(string(v)) + cast, nil
	case 1:
	default:
		return "", &fastpathError{code: errcodes.InvalidParameterValue, msg: fmt.Sprintf("unsupported format code: %d", format)}
	}
	bad := &fastpathError{code: errcodes.InvalidBinaryRepresentation, msg: fmt.Sprintf("incorrect binary data format in function argument %s", "%d")}
	switch typ {
	case "int2":
		if len(v) != 2 {
			return "", bad
		}
		return strconv.Itoa(int(int16(binary.BigEndian.Uint16(v)))) + cast, nil
	case "int4":
		if len(v) != 4 {
			return "", bad
		}
		return strconv.Itoa(int(int32(binary.BigEndian.Uint32(v)))) + cast, nil
	case "int8":
		if len(v) != 8 {
			return "", bad
		}
		return strconv.FormatInt(int64(binary.BigEndian.Uint64(v)), 10) + cast, nil
	case "oid":
		if len(v) != 4 {
			return "", bad
		}
		return strconv.FormatUint(uint64(binary.BigEndian.Uint32(v)), 10) + cast, nil
	case "bool":
		if len(v) != 1 {
			return "", bad
		}
		if v[0] != 0 {
			return "true", nil
		}
		return "false", nil
	case "float4":
		if len(v) != 4 {
			return "", bad
		}
		return pgQuoteLiteralText(strconv.FormatFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(v))), 'g', -1, 32)) + cast, nil
	case "float8":
		if len(v) != 8 {
			return "", bad
		}
		return pgQuoteLiteralText(strconv.FormatFloat(math.Float64frombits(binary.BigEndian.Uint64(v)), 'g', -1, 64)) + cast, nil
	case "bytea":
		return "'\\x" + hex.EncodeToString(v) + "'" + cast, nil
	case "text", "varchar", "name", "cstring":
		return pgQuoteLiteralText(string(v)) + cast, nil
	}
	return "", &fastpathError{code: errcodes.FeatureNotSupported,
		msg: fmt.Sprintf("binary fast-path arguments of type %s are not supported", typ)}
}

// pgQuoteLiteralText quotes s as a standard-conforming SQL string literal
// (E'' form when it contains a backslash, as quote_literal does).
func pgQuoteLiteralText(s string) string {
	q := "'" + strings.ReplaceAll(s, "'", "''") + "'"
	if strings.Contains(s, `\`) {
		return "E" + strings.ReplaceAll(q, `\`, `\\`)
	}
	return q
}

// fastpathResult converts the function's text-output cell into the
// FunctionCallResponse payload for rformat (SendFunctionResult): text format
// is the output function's string; binary is the type's send function, here
// for the types the fast-path callers return.
func fastpathResult(typOID uint32, text []byte, rformat int16) ([]byte, error) {
	if rformat == 0 {
		return text, nil
	}
	if rformat != 1 {
		return nil, &fastpathError{code: errcodes.InvalidParameterValue, msg: fmt.Sprintf("unsupported format code: %d", rformat)}
	}
	s := string(text)
	switch typOID {
	case 21, 23, 20, 26: // int2, int4, int8, oid
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, err
		}
		out := make([]byte, map[uint32]int{21: 2, 23: 4, 20: 8, 26: 4}[typOID])
		switch len(out) {
		case 2:
			binary.BigEndian.PutUint16(out, uint16(n))
		case 4:
			binary.BigEndian.PutUint32(out, uint32(n))
		default:
			binary.BigEndian.PutUint64(out, uint64(n))
		}
		return out, nil
	case 16: // bool
		if s == "t" {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case 17: // bytea: the text cell is byteaout's hex or escape form
		return decodeByteaText(s)
	case 25, 1043, 19, 2275: // text, varchar, name, cstring
		return text, nil
	case 2278: // void: void_send sends no bytes
		return []byte{}, nil
	}
	return nil, &fastpathError{code: errcodes.FeatureNotSupported,
		msg: fmt.Sprintf("binary fast-path results of type %d are not supported", typOID)}
}

// decodeByteaText parses byteaout's output (hex `\x…` or the escape form).
func decodeByteaText(s string) ([]byte, error) {
	if strings.HasPrefix(s, `\x`) {
		return hex.DecodeString(s[2:])
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\\' {
			out = append(out, '\\')
			i++
			continue
		}
		if i+3 >= len(s) {
			return nil, fmt.Errorf("invalid bytea escape")
		}
		n, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return nil, err
		}
		out = append(out, byte(n))
		i += 3
	}
	return out, nil
}

type fastpathError struct {
	code errcodes.Code
	msg  string
}

func (e *fastpathError) Error() string { return e.msg }

// fastpathSQL resolves the function (fetch_fp_info: 42883 "function with OID
// %u does not exist") and builds the lowered SELECT.
func (s *Server) fastpathSQL(c *fastpathCall) (string, error) {
	var routines *catalog.Routines
	if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
		routines = im.Routines()
	}
	schema, name, argTypes, ok := catalog.RegprocedureNameParts(c.fnOID, routines)
	if !ok {
		return "", &fastpathError{code: errcodes.UndefinedFunction, msg: fmt.Sprintf("function with OID %d does not exist", c.fnOID)}
	}
	if len(argTypes) != len(c.args) {
		// parse_fcall_arguments: "function call message contains %d arguments
		// but function requires %d".
		return "", &fastpathError{code: errcodes.ProtocolViolation,
			msg: fmt.Sprintf("function call message contains %d arguments but function requires %d", len(c.args), len(argTypes))}
	}
	lits := make([]string, len(c.args))
	for i, a := range c.args {
		lit, err := fastpathArgLiteral(argTypes[i].Name, c.formats[i], a)
		if err != nil {
			if fe, ok := err.(*fastpathError); ok && strings.Contains(fe.msg, "%d") {
				fe.msg = fmt.Sprintf(fe.msg, i+1)
			}
			return "", err
		}
		lits[i] = lit
	}
	if schema == "" {
		schema = "pg_catalog"
	}
	return fmt.Sprintf("SELECT %s.%s(%s)", quoteIdentSimple(schema), quoteIdentSimple(name), strings.Join(lits, ", ")), nil
}

func quoteIdentSimple(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// handleFunctionCall answers one FunctionCall message. It always ends the
// exchange with ReadyForQuery; a non-nil error means the connection is lost.
func (s *Server) handleFunctionCall(ctx context.Context, r *libpq.FrameReader, w *libpq.FrameWriter, sess *misc.SessionRegistry, payload []byte, connTx *connTxState, prepStmts *preparedStatements) error {
	// An error before the call runs aborts the transaction like any failed
	// statement: an explicit block turns failed (25P02 afterwards).
	writeErr := func(code errcodes.Code, msg string) error {
		if err := s.writeQueryError(w, code, msg); err != nil && !errors.Is(err, errQueryErrorSent) {
			return err
		}
		if connTx.InExplicit() {
			connTx.Fail()
		}
		return nil
	}
	c, err := parseFastpathCall(payload)
	if err != nil {
		return writeErr(errcodes.ProtocolViolation, err.Error())
	}
	if connTx.IsFailed() {
		return writeErr(errcodes.InFailedSQLTransaction,
			"current transaction is aborted, commands ignored until end of transaction block")
	}
	sql, err := s.fastpathSQL(c)
	if err != nil {
		if fe, ok := err.(*fastpathError); ok {
			return writeErr(fe.code, fe.msg)
		}
		return writeErr(errcodes.InternalError, err.Error())
	}

	var buf bytes.Buffer
	cw := libpq.NewFrameWriter(&buf)
	cw.TxStatusFn = w.TxStatusFn
	cw.ClientMinMessagesFn = w.ClientMinMessagesFn
	q := append([]byte(sql), 0)
	if _, err := s.handleQueryOrCopy(ctx, r, cw, sess, q, connTx, prepStmts); err != nil && !errors.Is(err, errQueryErrorSent) {
		// Forward whatever reached the capture before the failure.
		_ = cw.Flush()
		_ = w.WriteRaw(buf.Bytes())
		return err
	}
	if err := cw.Flush(); err != nil {
		return err
	}
	// SendFunctionResult encodes with the function's prorettype. Builtins take
	// it from the pg_proc seed; a user routine falls back to the captured
	// RowDescription's column type.
	retTyp, _ := catalog.ProcResultType(c.fnOID)
	return s.rewriteFastpathStream(w, buf.Bytes(), c.rformat, retTyp)
}

// rewriteFastpathStream turns the captured simple-query response into the
// fast-path one: RowDescription / CommandComplete are dropped, notices,
// notifications and errors are forwarded, the DataRow cell becomes 'V', and
// ReadyForQuery is forwarded last.
func (s *Server) rewriteFastpathStream(w *libpq.FrameWriter, stream []byte, rformat int16, retTyp uint32) error {
	resTyp := retTyp
	var cell []byte
	haveRow, sawError := false, false
	for len(stream) >= 5 {
		typ := stream[0]
		n := int(binary.BigEndian.Uint32(stream[1:5]))
		if n < 4 || 1+n > len(stream) {
			break
		}
		frame := stream[:1+n]
		body := stream[5 : 1+n]
		stream = stream[1+n:]
		switch typ {
		case 'T': // RowDescription: Int16 n, then name\0 + 18 bytes per field
			if i := bytes.IndexByte(body[2:], 0); i >= 0 && len(body) >= 2+i+1+18 {
				f := body[2+i+1:]
				if retTyp == 0 {
					resTyp = binary.BigEndian.Uint32(f[6:10])
				}
			}
		case 'D':
			if len(body) >= 6 {
				l := int32(binary.BigEndian.Uint32(body[2:6]))
				haveRow = true
				if l >= 0 && int(6+l) <= len(body) {
					cell = append([]byte{}, body[6:6+l]...)
				} else {
					cell = nil
				}
			}
		case 'C':
		case 'E':
			sawError = true
			if err := w.WriteRaw(frame); err != nil {
				return err
			}
		case 'Z':
			if !sawError {
				if err := s.writeFunctionCallResponse(w, haveRow, cell, resTyp, rformat); err != nil {
					return err
				}
			}
			if err := w.WriteRaw(frame); err != nil {
				return err
			}
		default: // 'N' notices, 'A' notifications, 'S' parameter status
			if err := w.WriteRaw(frame); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) writeFunctionCallResponse(w *libpq.FrameWriter, haveRow bool, cell []byte, resTyp uint32, rformat int16) error {
	var payload []byte
	if !haveRow || cell == nil {
		payload = []byte{0xff, 0xff, 0xff, 0xff} // NULL result
	} else {
		v, err := fastpathResult(resTyp, cell, rformat)
		if err != nil {
			code := errcodes.InternalError
			if fe, ok := err.(*fastpathError); ok {
				code = fe.code
			}
			return w.WriteErrorResponse([]libpq.ErrorField{
				{Code: libpq.FieldSeverity, Value: "ERROR"},
				{Code: libpq.FieldSeverityNonLocal, Value: "ERROR"},
				{Code: libpq.FieldSQLState, Value: string(code)},
				{Code: libpq.FieldMessage, Value: err.Error()},
			})
		}
		payload = make([]byte, 4, 4+len(v))
		binary.BigEndian.PutUint32(payload, uint32(len(v)))
		payload = append(payload, v...)
	}
	return w.WriteFrame(msgFunctionCallResponse, payload)
}
