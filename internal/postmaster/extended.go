package postmaster

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

type extendedState struct {
	statements   map[string]*preparedStatement
	portals      map[string]*portalState
	syncRequired bool
	ProcNum      int32  // backend's ProcArray slot; forwarded to Begin calls
	DBName       string // connection's database; scopes pg_extension (M0110-0003 gap #7c)
}

type preparedStatement struct {
	Name       string
	Query      string
	ParamCount int
}

type portalState struct {
	Name      string
	Statement *preparedStatement
	Params    []boundParam
	Result    *extendedQueryResult
	RowPos    int
}

type boundParam struct {
	IsNull bool
	Text   string
}

type extendedMessageError struct {
	Code     errcodes.Code
	Message  string
	Detail   string // errdetail text; "" = omit FieldDetail
	Hint     string // errhint text; "" = omit FieldHint
	Routine  string
	Position int // 1-based byte offset; 0 = omit FieldPosition
}

type extendedQueryResult struct {
	Fields     []libpq.FieldDescription
	Rows       [][][]byte
	CommandTag string
	Empty      bool
	Notice     string // NoticeResponse text to emit before CommandComplete; "" = none
	// WarnFields is a fully-formed NoticeResponse field set to emit before
	// CommandComplete — the shape PG's "there is no transaction in progress"
	// WARNING needs (severity WARNING + SQLSTATE 25P01), which the plain
	// Notice string above cannot express. M0132-S2.
	WarnFields []libpq.ErrorField
}

type extendedQueryError struct {
	Code     errcodes.Code
	Message  string
	Detail   string // errdetail text; "" = omit FieldDetail
	Hint     string // errhint text; "" = omit FieldHint
	Position int    // 1-based byte offset; 0 = omit FieldPosition
}

func newExtendedState() *extendedState {
	return &extendedState{
		statements: map[string]*preparedStatement{},
		portals:    map[string]*portalState{},
	}
}

func (s *Server) writeExtendedMessageError(w *libpq.FrameWriter, em *extendedMessageError) error {
	routine := em.Routine
	if routine == "" {
		routine = "postmaster.runPostStartupLoop"
	}
	fields := []libpq.ErrorField{
		{Code: libpq.FieldSeverity, Value: "ERROR"},
		{Code: libpq.FieldSeverityNonLocal, Value: "ERROR"},
		{Code: libpq.FieldSQLState, Value: string(em.Code)},
		{Code: libpq.FieldMessage, Value: em.Message},
		{Code: libpq.FieldRoutine, Value: routine},
	}
	if em.Position > 0 {
		fields = append(fields, libpq.ErrorField{Code: libpq.FieldPosition, Value: strconv.Itoa(em.Position)})
	}
	if em.Detail != "" {
		fields = append(fields, libpq.ErrorField{Code: libpq.FieldDetail, Value: em.Detail})
	}
	if em.Hint != "" {
		fields = append(fields, libpq.ErrorField{Code: libpq.FieldHint, Value: em.Hint})
	}
	return w.WriteErrorResponse(fields)
}

func (s *Server) handleParseFrame(state *extendedState, payload []byte) *extendedMessageError {
	pr := payloadReader{buf: payload}
	name, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Parse message: "+err.Error(), "postmaster.handleParseFrame")
	}
	query, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Parse message: "+err.Error(), "postmaster.handleParseFrame")
	}
	nTypeOIDs, err := pr.readUint16()
	if err != nil {
		return protoViolation("invalid Parse message: "+err.Error(), "postmaster.handleParseFrame")
	}
	for i := 0; i < int(nTypeOIDs); i++ {
		if _, err := pr.readUint32(); err != nil {
			return protoViolation("invalid Parse message: "+err.Error(), "postmaster.handleParseFrame")
		}
	}
	if !pr.done() {
		return protoViolation("invalid Parse message: trailing bytes", "postmaster.handleParseFrame")
	}

	stmt := &preparedStatement{Name: name, Query: query, ParamCount: inferParamCount(query)}
	state.statements[name] = stmt
	if name == "" {
		delete(state.portals, "")
	}
	return nil
}

func (s *Server) handleBindFrame(state *extendedState, payload []byte) *extendedMessageError {
	pr := payloadReader{buf: payload}
	portalName, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
	}
	statementName, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
	}
	stmt, ok := state.statements[statementName]
	if !ok {
		return &extendedMessageError{
			Code:    errcodes.InvalidSQLStatementName,
			Message: fmt.Sprintf("prepared statement %q does not exist", statementName),
			Routine: "postmaster.handleBindFrame",
		}
	}

	nParamFormats, err := pr.readUint16()
	if err != nil {
		return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
	}
	paramFormats := make([]uint16, 0, nParamFormats)
	for i := 0; i < int(nParamFormats); i++ {
		f, err := pr.readUint16()
		if err != nil {
			return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
		}
		paramFormats = append(paramFormats, f)
	}

	nParams, err := pr.readUint16()
	if err != nil {
		return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
	}
	if len(paramFormats) != 0 && len(paramFormats) != 1 && len(paramFormats) != int(nParams) {
		return protoViolation("invalid Bind message: parameter format code count mismatch", "postmaster.handleBindFrame")
	}
	if int(nParams) != stmt.ParamCount {
		return protoViolation(
			fmt.Sprintf("bind supplies %d parameters, prepared statement requires %d", nParams, stmt.ParamCount),
			"postmaster.handleBindFrame",
		)
	}
	params := make([]boundParam, 0, nParams)
	for i := 0; i < int(nParams); i++ {
		fmtCode := bindFormatCode(paramFormats, i)
		if fmtCode != 0 {
			return &extendedMessageError{
				Code:    errcodes.FeatureNotSupported,
				Message: "binary parameter formats are not supported",
				Routine: "postmaster.handleBindFrame",
			}
		}
		vlen, err := pr.readInt32()
		if err != nil {
			return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
		}
		if vlen < -1 {
			return protoViolation("invalid Bind message: negative parameter length", "postmaster.handleBindFrame")
		}
		if vlen == -1 {
			params = append(params, boundParam{IsNull: true})
			continue
		}
		b, err := pr.readBytes(int(vlen))
		if err != nil {
			return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
		}
		params = append(params, boundParam{Text: string(b)})
	}

	nResultFormats, err := pr.readUint16()
	if err != nil {
		return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
	}
	resultFormats := make([]uint16, 0, nResultFormats)
	for i := 0; i < int(nResultFormats); i++ {
		f, err := pr.readUint16()
		if err != nil {
			return protoViolation("invalid Bind message: "+err.Error(), "postmaster.handleBindFrame")
		}
		resultFormats = append(resultFormats, f)
	}
	if len(resultFormats) != 0 && len(resultFormats) != 1 {
		return protoViolation("invalid Bind message: result format code count mismatch", "postmaster.handleBindFrame")
	}
	for _, f := range resultFormats {
		if f != 0 {
			return &extendedMessageError{
				Code:    errcodes.FeatureNotSupported,
				Message: "binary result formats are not supported",
				Routine: "postmaster.handleBindFrame",
			}
		}
	}
	if !pr.done() {
		return protoViolation("invalid Bind message: trailing bytes", "postmaster.handleBindFrame")
	}

	state.portals[portalName] = &portalState{
		Name:      portalName,
		Statement: stmt,
		Params:    params,
	}
	return nil
}

func (s *Server) handleDescribeFrame(state *extendedState, payload []byte, w *libpq.FrameWriter, sess *misc.SessionRegistry) (*extendedMessageError, error) {
	pr := payloadReader{buf: payload}
	kind, err := pr.readByte()
	if err != nil {
		return protoViolation("invalid Describe message: "+err.Error(), "postmaster.handleDescribeFrame"), nil
	}
	name, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Describe message: "+err.Error(), "postmaster.handleDescribeFrame"), nil
	}
	if !pr.done() {
		return protoViolation("invalid Describe message: trailing bytes", "postmaster.handleDescribeFrame"), nil
	}

	switch kind {
	case 'S':
		stmt, ok := state.statements[name]
		if !ok {
			return &extendedMessageError{
				Code:    errcodes.InvalidSQLStatementName,
				Message: fmt.Sprintf("prepared statement %q does not exist", name),
				Routine: "postmaster.handleDescribeFrame",
			}, nil
		}
		oids := make([]uint32, stmt.ParamCount)
		if err := w.WriteParameterDescription(oids); err != nil {
			return nil, err
		}
		fields := s.describeExtendedQuery(stmt.Query, sess, state.DBName)
		// nil = no result set (write/DDL/txn) → NoData; a non-nil but empty
		// slice is a zero-column read (`SELECT FROM t`) → RowDescription with
		// 0 fields, matching PostgreSQL.
		if fields == nil {
			if err := w.WriteNoData(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err := w.WriteRowDescription(fields); err != nil {
			return nil, err
		}
		return nil, nil
	case 'P':
		portal, ok := state.portals[name]
		if !ok {
			return &extendedMessageError{
				Code:    errcodes.InvalidCursorName,
				Message: fmt.Sprintf("portal %q does not exist", name),
				Routine: "postmaster.handleDescribeFrame",
			}, nil
		}
		fields := s.describeExtendedQuery(portal.Statement.Query, sess, state.DBName)
		// nil = no result set (write/DDL/txn) → NoData; a non-nil but empty
		// slice is a zero-column read (`SELECT FROM t`) → RowDescription with
		// 0 fields, matching PostgreSQL.
		if fields == nil {
			if err := w.WriteNoData(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err := w.WriteRowDescription(fields); err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return protoViolation("invalid Describe message: target must be 'S' or 'P'", "postmaster.handleDescribeFrame"), nil
	}
}

func (s *Server) handleExecuteFrame(ctx context.Context, state *extendedState, payload []byte, w *libpq.FrameWriter, sess *misc.SessionRegistry, connTx *connTxState) (*extendedMessageError, error) {
	pr := payloadReader{buf: payload}
	portalName, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Execute message: "+err.Error(), "postmaster.handleExecuteFrame"), nil
	}
	maxRows, err := pr.readInt32()
	if err != nil {
		return protoViolation("invalid Execute message: "+err.Error(), "postmaster.handleExecuteFrame"), nil
	}
	if !pr.done() {
		return protoViolation("invalid Execute message: trailing bytes", "postmaster.handleExecuteFrame"), nil
	}

	portal, ok := state.portals[portalName]
	if !ok {
		return &extendedMessageError{
			Code:    errcodes.InvalidCursorName,
			Message: fmt.Sprintf("portal %q does not exist", portalName),
			Routine: "postmaster.handleExecuteFrame",
		}, nil
	}

	if portal.Result == nil {
		res, qerr := s.executeExtendedQuery(ctx, sess, portal.Statement.Query, portal.Params, state.ProcNum, state.DBName, connTx)
		if qerr != nil {
			// M0132-S5: the extended message loop had no connTx.Fail() call
			// site at all — its only 'Z' write is the plain ReadyForQuery, so
			// the ReadyForQueryAfterError escape hatch that makes the simple
			// path report 'E' is unavailable here. Marking the block failed is
			// what makes both the status byte and the 25P02 gate work.
			failExplicitBlock(connTx)
			return &extendedMessageError{Code: qerr.Code, Message: qerr.Message, Detail: qerr.Detail, Hint: qerr.Hint, Position: qerr.Position, Routine: "postmaster.handleExecuteFrame"}, nil
		}
		if len(res.WarnFields) > 0 {
			if err := w.WriteNoticeResponse(res.WarnFields); err != nil {
				return nil, err
			}
		}
		if res.Notice != "" {
			if err := w.WriteNoticeResponse([]libpq.ErrorField{
				{Code: libpq.FieldSeverity, Value: "NOTICE"},
				{Code: libpq.FieldSeverityNonLocal, Value: "NOTICE"},
				{Code: libpq.FieldSQLState, Value: "00000"},
				{Code: libpq.FieldMessage, Value: res.Notice},
			}); err != nil {
				return nil, err
			}
		}
		portal.Result = res
		portal.RowPos = 0
	}

	res := portal.Result
	if res.Empty {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if len(res.Rows) == 0 {
		if err := w.WriteCommandComplete(res.CommandTag); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if portal.RowPos < 0 {
		portal.RowPos = 0
	}
	if portal.RowPos > len(res.Rows) {
		portal.RowPos = len(res.Rows)
	}
	remaining := len(res.Rows) - portal.RowPos
	if remaining <= 0 {
		if err := w.WriteCommandComplete(res.CommandTag); err != nil {
			return nil, err
		}
		return nil, nil
	}

	toSend := remaining
	if maxRows > 0 && int(maxRows) < toSend {
		toSend = int(maxRows)
	}
	for i := 0; i < toSend; i++ {
		row := res.Rows[portal.RowPos+i]
		getSetting := func(name string) (string, bool) {
			_, val, ok := sess.Get(name)
			return val, ok
		}
		row = s.maybeConvertCellsForClientEncoding(row, getSetting)
		if err := w.WriteDataRow(row); err != nil {
			return nil, err
		}
	}
	portal.RowPos += toSend
	if maxRows > 0 && portal.RowPos < len(res.Rows) {
		if err := w.WritePortalSuspended(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := w.WriteCommandComplete(res.CommandTag); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleCloseFrame(state *extendedState, payload []byte) *extendedMessageError {
	pr := payloadReader{buf: payload}
	kind, err := pr.readByte()
	if err != nil {
		return protoViolation("invalid Close message: "+err.Error(), "postmaster.handleCloseFrame")
	}
	name, err := pr.readCString()
	if err != nil {
		return protoViolation("invalid Close message: "+err.Error(), "postmaster.handleCloseFrame")
	}
	if !pr.done() {
		return protoViolation("invalid Close message: trailing bytes", "postmaster.handleCloseFrame")
	}

	switch kind {
	case 'S':
		delete(state.statements, name)
		for pname, p := range state.portals {
			if p.Statement != nil && p.Statement.Name == name {
				delete(state.portals, pname)
			}
		}
		return nil
	case 'P':
		delete(state.portals, name)
		return nil
	default:
		return protoViolation("invalid Close message: target must be 'S' or 'P'", "postmaster.handleCloseFrame")
	}
}

func (s *Server) executeExtendedQuery(ctx context.Context, sess *misc.SessionRegistry, query string, params []boundParam, procNum int32, dbName string, connTx *connTxState) (*extendedQueryResult, *extendedQueryError) {
	trimmed, matchable, upper, empty := normalizeSimpleQuery(query)
	if empty {
		return &extendedQueryResult{Empty: true}, nil
	}

	// M0132-S5: the aborted-block gate, ahead of every fast path below —
	// a failed block rejects `SELECT 1` / `SHOW` / `SET` too, and those never
	// reach the executor. The simple path gates identically in handleQuery.
	if connTx != nil && connTx.IsFailed() {
		if !allowedInAbortedBlock(upper) {
			return nil, &extendedQueryError{Code: "25P02", Message: abortedBlockMessage}
		}
		// ROLLBACK TO SAVEPOINT unwinds the failure instead of ending the
		// block, so the block becomes usable again (dispatch.go's gate does
		// the same for the simple path).
		if strings.HasPrefix(upper, "ROLLBACK TO") {
			connTx.ClearFailed()
		}
	}

	if strings.EqualFold(matchable, "SELECT 1") {
		return &extendedQueryResult{
			Fields: []libpq.FieldDescription{{
				Name:         "?column?",
				TableOID:     0,
				ColumnAttNum: 0,
				TypeOID:      oidInt4,
				TypeSize:     4,
				TypeModifier: -1,
				Format:       0,
			}},
			Rows:       [][][]byte{{[]byte("1")}},
			CommandTag: "SELECT 1",
		}, nil
	}

	switch {
	case upper == "SHOW ALL":
		rows := sess.AllDisplay()
		out := make([][][]byte, 0, len(rows))
		for _, kv := range rows {
			out = append(out, [][]byte{[]byte(kv.Name), []byte(kv.Value)})
		}
		return &extendedQueryResult{
			Fields: []libpq.FieldDescription{
				{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
				{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
			},
			Rows:       out,
			CommandTag: "SHOW",
		}, nil
	case strings.HasPrefix(upper, "SHOW "):
		name := strings.TrimSpace(matchable[len("SHOW "):])
		if strings.EqualFold(name, "ALL") {
			rows := sess.AllDisplay()
			out := make([][][]byte, 0, len(rows))
			for _, kv := range rows {
				out = append(out, [][]byte{[]byte(kv.Name), []byte(kv.Value)})
			}
			return &extendedQueryResult{
				Fields: []libpq.FieldDescription{
					{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
					{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
				},
				Rows:       out,
				CommandTag: "SHOW",
			}, nil
		}
		name = strings.Trim(name, " \"'")
		v, eff, ok := sess.GetDisplay(name)
		if !ok {
			return nil, &extendedQueryError{Code: errcodes.UndefinedObject, Message: fmt.Sprintf("unrecognized configuration parameter %q", name)}
		}
		return &extendedQueryResult{
			Fields:     []libpq.FieldDescription{{Name: v.Name, TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0}},
			Rows:       [][][]byte{{[]byte(eff)}},
			CommandTag: "SHOW",
		}, nil
	// SET LOCAL SESSION AUTHORIZATION name — must be checked before the
	// generic "SET LOCAL " case below, mirroring server/query.go's
	// simple-query handling (M0119-0004: the extended-protocol fast path
	// previously fell through to the generic case and mis-treated "SESSION"
	// as a GUC name, erroring with "unrecognized configuration parameter").
	case strings.HasPrefix(upper, "SET LOCAL SESSION AUTHORIZATION "),
		upper == "SET LOCAL SESSION AUTHORIZATION":
		setSessionAuthorizationFastPath(sess, connTx, matchable, "SET LOCAL SESSION AUTHORIZATION", true)
		return &extendedQueryResult{CommandTag: "SET"}, nil
	// SET LOCAL ROLE rolename — must be checked before the generic "SET LOCAL "
	// case below, which would otherwise mis-parse "ROLE rolename" as GUC name
	// "role" (not a config.Registry variable — SET ROLE is tracked entirely
	// via connTx.NonSuperuserRole) and fail with "unrecognized configuration
	// parameter". Mirrors server/query.go's simple-query handling. M0119-0004.
	case strings.HasPrefix(upper, "SET LOCAL ROLE "), upper == "SET LOCAL ROLE":
		setRoleFastPath(sess, connTx, matchable, "SET LOCAL ROLE", true)
		return &extendedQueryResult{CommandTag: "SET"}, nil
	case strings.HasPrefix(upper, "SET LOCAL "):
		body := matchable[len("SET LOCAL "):]
		name, value, ok := splitSet(body)
		if !ok {
			return nil, &extendedQueryError{Code: errcodes.SyntaxError, Message: fmt.Sprintf("could not parse SET statement: %q", body)}
		}
		if err := sess.Set(name, value, true); err != nil {
			return nil, &extendedQueryError{Code: errcodes.InvalidParameterValue, Message: err.Error()}
		}
		return &extendedQueryResult{CommandTag: "SET"}, nil
	// SET SESSION AUTHORIZATION name — track non-superuser role for privilege
	// checks. Must be checked before the generic "SET " case so splitSet
	// doesn't mis-parse "SESSION AUTHORIZATION name". M0119-0004.
	case strings.HasPrefix(upper, "SET SESSION AUTHORIZATION "),
		upper == "SET SESSION AUTHORIZATION":
		setSessionAuthorizationFastPath(sess, connTx, matchable, "SET SESSION AUTHORIZATION", false)
		return &extendedQueryResult{CommandTag: "SET"}, nil
	// SET ROLE rolename — track the effective role for privilege checks. Must
	// be before the generic "SET " case so "ROLE" is not passed to sess.Set as
	// a GUC name. M0119-0004.
	case strings.HasPrefix(upper, "SET ROLE "), upper == "SET ROLE":
		setRoleFastPath(sess, connTx, matchable, "SET ROLE", false)
		return &extendedQueryResult{CommandTag: "SET"}, nil
	case strings.HasPrefix(upper, "SET CONSTRAINTS "):
		// SET CONSTRAINTS is not a GUC. The extended fast path holds only the
		// GUC SessionRegistry, not the executor BasicSession where FK-deferral
		// state lives, so accept it here as a correctly-tagged no-op rather than
		// mis-parsing it as a configuration parameter. Runtime deferral via
		// SET CONSTRAINTS is supported on the simple-query protocol (the path
		// psql and the isolation harness use). 0119-0004.
		return &extendedQueryResult{CommandTag: "SET CONSTRAINTS"}, nil
	case strings.HasPrefix(upper, "SET "):
		body := matchable[len("SET "):]
		name, value, ok := splitSet(body)
		if !ok {
			return nil, &extendedQueryError{Code: errcodes.SyntaxError, Message: fmt.Sprintf("could not parse SET statement: %q", body)}
		}
		if err := sess.Set(name, value, false); err != nil {
			return nil, &extendedQueryError{Code: errcodes.InvalidParameterValue, Message: err.Error()}
		}
		return &extendedQueryResult{CommandTag: "SET"}, nil
	case upper == "RESET ALL":
		sess.ResetAll()
		return &extendedQueryResult{CommandTag: "RESET"}, nil
	// RESET SESSION AUTHORIZATION / RESET ROLE — restore the bootstrap
	// superuser's full privileges. Must be checked before the generic
	// "RESET " case. M0119-0004.
	case upper == "RESET SESSION AUTHORIZATION", upper == "RESET ROLE":
		if connTx != nil {
			connTx.NonSuperuserRole = ""
			setIsSuperuserGUC(sess, true)
		}
		return &extendedQueryResult{CommandTag: "RESET"}, nil
	case strings.HasPrefix(upper, "RESET "):
		name := strings.TrimSpace(matchable[len("RESET "):])
		if err := sess.Reset(name); err != nil {
			return nil, &extendedQueryError{Code: errcodes.UndefinedObject, Message: err.Error()}
		}
		return &extendedQueryResult{CommandTag: "RESET"}, nil
	}

	if strings.HasPrefix(upper, "COPY ") {
		return nil, &extendedQueryError{
			Code:    errcodes.FeatureNotSupported,
			Message: "COPY is only supported in the simple query protocol",
		}
	}
	if s.cfg.hasStorage() {
		return s.executeExtendedQueryViaExecutor(ctx, sess, trimmed, params, procNum, dbName, connTx)
	}

	if len(params) > 0 {
		return nil, &extendedQueryError{Code: errcodes.FeatureNotSupported, Message: "bind parameters are not supported yet (storage not configured)"}
	}
	return nil, &extendedQueryError{
		Code: errcodes.FeatureNotSupported,
		Message: fmt.Sprintf("query not supported by goopg v0: %q "+
			"(only SELECT 1 / SHOW / SET / RESET are recognised until storage is wired via -D)", trimmed),
	}
}

func (s *Server) describeExtendedQuery(query string, sess *misc.SessionRegistry, dbName string) []libpq.FieldDescription {
	_, matchable, upper, empty := normalizeSimpleQuery(query)
	if empty {
		return nil
	}
	if strings.EqualFold(matchable, "SELECT 1") {
		return []libpq.FieldDescription{{
			Name:         "?column?",
			TableOID:     0,
			ColumnAttNum: 0,
			TypeOID:      oidInt4,
			TypeSize:     4,
			TypeModifier: -1,
			Format:       0,
		}}
	}
	switch {
	case upper == "SHOW ALL":
		return []libpq.FieldDescription{
			{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
			{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		}
	case strings.HasPrefix(upper, "SHOW "):
		name := strings.TrimSpace(matchable[len("SHOW "):])
		name = strings.Trim(name, " \"'")
		if strings.EqualFold(name, "ALL") {
			return []libpq.FieldDescription{
				{Name: "name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
				{Name: "setting", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
			}
		}
		if name == "" {
			name = "?column?"
		}
		return []libpq.FieldDescription{{Name: name, TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0}}
	}
	if s.cfg.hasStorage() {
		// Plan the statement to learn its output schema. Errors here
		// are non-fatal — we just fall back to NoData and let the
		// follow-up Execute surface a real error.
		if fields, ok := s.describeViaPlanner(query, sess, dbName); ok {
			return fields
		}
	}
	return nil
}

// describeViaPlanner parses+plans the query and converts the
// planner's output schema into a wire-protocol RowDescription. The
// caller dispatches based on whether the result is non-empty: an
// empty schema (write-only statement, transaction verb, etc.) is
// signalled to the wire layer as NoData. Errors return ok=false; the
// real error will surface during Execute.
func (s *Server) describeViaPlanner(query string, sess *misc.SessionRegistry, dbName string) ([]libpq.FieldDescription, bool) {
	// M0132-S13: read the output schema from the cross-session plan cache when
	// possible, so a per-iteration Describe (pgbench's PQexecPrepared sends a
	// Describe Portal per execution) no longer re-parses+re-plans — the -S hot
	// path (S11's O-XP-1 profile: 13.4% cum). The cache is DDL-invalidated by
	// the Execute path exactly as it is here. Using sessionPlanCatalog (instead
	// of the bare s.cfg.Catalog this function used before) makes Describe and
	// Execute plan on the SAME catalog, so a Describe describes the plan the
	// following Execute actually runs — the previous divergence meant a session
	// with search_path/enable_seqscan/temp overrides could see the two disagree.
	connDBOid := resolveConnDBOid(s.cfg.Catalog, dbName)
	var node optimizer.Node
	if s.pc != nil && !sessionTempInheritanceActive(s.cfg.Catalog) && !partitionDetachPending(s.cfg.Catalog) && !inheritanceChangePending(s.cfg.Catalog) {
		if cached, ok := s.pc.Get(planCacheKey(query, connDBOid)); ok {
			node = cached
		} else {
			stmts, err := parser.Parse(query)
			if err != nil || len(stmts) != 1 {
				return nil, false
			}
			var perr error
			node, perr = optimizer.Plan(stmts[0], sessionPlanCatalog(sess, s.cfg.Catalog, connDBOid))
			if perr != nil {
				return nil, false
			}
			if planCacheIsCacheable(node) {
				s.pc.Put(planCacheKey(query, connDBOid), node)
			}
		}
	} else {
		stmts, err := parser.Parse(query)
		if err != nil || len(stmts) != 1 {
			return nil, false
		}
		var perr error
		node, perr = optimizer.Plan(stmts[0], sessionPlanCatalog(sess, s.cfg.Catalog, connDBOid))
		if perr != nil {
			return nil, false
		}
	}
	schema := node.Output()
	if schema == nil {
		// Write-only / DDL / transaction — no result set at all → NoData.
		return nil, true
	}
	// A non-nil but zero-length schema is a zero-column read (e.g.
	// `SELECT FROM t`, `SELECT;`): PostgreSQL Describe replies with a
	// RowDescription carrying 0 fields, NOT NoData. Returning a non-nil
	// empty slice signals that to describeExtendedQuery's callers.
	fields := make([]libpq.FieldDescription, len(schema))
	for i, sc := range schema {
		fields[i] = libpq.FieldDescription{
			Name:         sc.Name,
			TypeOID:      typeOIDFor(sc.Type),
			TypeSize:     -1,
			TypeModifier: -1,
			Format:       0,
		}
	}
	return fields, true
}

// setSessionAuthorizationFastPath applies a `SET [LOCAL] SESSION
// AUTHORIZATION name` statement's role change to connTx/sess, shared by the
// extended-query protocol's SET fast path (executeExtendedQuery). prefix is
// the exact keyword sequence to strip ("SET SESSION AUTHORIZATION" or "SET
// LOCAL SESSION AUTHORIZATION"); matchable is the ';'-stripped statement
// text. Mirrors server/query.go's handleQuery SET SESSION AUTHORIZATION
// cases. M0119-0004.
func setSessionAuthorizationFastPath(sess *misc.SessionRegistry, connTx *connTxState, matchable, prefix string, local bool) {
	if connTx == nil {
		return
	}
	role := strings.TrimSpace(matchable[len(prefix):])
	role = strings.Trim(role, `"'`)
	connTx.SnapshotLocalRoleIfNeeded(local)
	switch strings.ToUpper(role) {
	case "", "DEFAULT", "RESET", "POSTGRES":
		connTx.NonSuperuserRole = ""
	default:
		connTx.NonSuperuserRole = role
	}
	setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
}

// setRoleFastPath is setSessionAuthorizationFastPath's SET ROLE / SET LOCAL
// ROLE sibling: identical shape, but "NONE" (not "RESET") is SET ROLE's
// keyword for restoring superuser status. M0119-0004.
func setRoleFastPath(sess *misc.SessionRegistry, connTx *connTxState, matchable, prefix string, local bool) {
	if connTx == nil {
		return
	}
	role := strings.TrimSpace(matchable[len(prefix):])
	role = strings.Trim(role, `"'`)
	connTx.SnapshotLocalRoleIfNeeded(local)
	switch strings.ToUpper(role) {
	case "", "DEFAULT", "NONE", "POSTGRES":
		connTx.NonSuperuserRole = ""
	default:
		connTx.NonSuperuserRole = role
	}
	setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
}

func normalizeSimpleQuery(query string) (trimmed string, matchable string, upper string, empty bool) {
	trimmed = strings.TrimSpace(query)
	if trimmed == "" {
		return "", "", "", true
	}
	matchable = strings.TrimSpace(strings.TrimRight(trimmed, ";"))
	upper = strings.ToUpper(matchable)
	return trimmed, matchable, upper, false
}

func inferParamCount(query string) int {
	maxNum := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '$' {
			continue
		}
		j := i + 1
		n := 0
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			n = n*10 + int(query[j]-'0')
			j++
		}
		if j == i+1 {
			continue
		}
		if n > maxNum {
			maxNum = n
		}
		i = j - 1
	}
	return maxNum
}

func bindFormatCode(codes []uint16, idx int) uint16 {
	if len(codes) == 0 {
		return 0
	}
	if len(codes) == 1 {
		return codes[0]
	}
	return codes[idx]
}

func protoViolation(msg, routine string) *extendedMessageError {
	return &extendedMessageError{Code: errcodes.ProtocolViolation, Message: msg, Routine: routine}
}

type payloadReader struct {
	buf []byte
	off int
}

func (pr *payloadReader) readByte() (byte, error) {
	if pr.off >= len(pr.buf) {
		return 0, fmt.Errorf("unexpected end of payload")
	}
	v := pr.buf[pr.off]
	pr.off++
	return v, nil
}

func (pr *payloadReader) readUint16() (uint16, error) {
	if pr.off+2 > len(pr.buf) {
		return 0, fmt.Errorf("unexpected end of payload")
	}
	v := binary.BigEndian.Uint16(pr.buf[pr.off : pr.off+2])
	pr.off += 2
	return v, nil
}

func (pr *payloadReader) readUint32() (uint32, error) {
	if pr.off+4 > len(pr.buf) {
		return 0, fmt.Errorf("unexpected end of payload")
	}
	v := binary.BigEndian.Uint32(pr.buf[pr.off : pr.off+4])
	pr.off += 4
	return v, nil
}

func (pr *payloadReader) readInt32() (int32, error) {
	v, err := pr.readUint32()
	if err != nil {
		return 0, err
	}
	return int32(v), nil
}

func (pr *payloadReader) readBytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("negative byte length")
	}
	if pr.off+n > len(pr.buf) {
		return nil, fmt.Errorf("unexpected end of payload")
	}
	v := pr.buf[pr.off : pr.off+n]
	pr.off += n
	return v, nil
}

func (pr *payloadReader) readCString() (string, error) {
	if pr.off >= len(pr.buf) {
		return "", fmt.Errorf("unexpected end of payload")
	}
	start := pr.off
	for pr.off < len(pr.buf) && pr.buf[pr.off] != 0 {
		pr.off++
	}
	if pr.off >= len(pr.buf) {
		return "", fmt.Errorf("unterminated C string")
	}
	s := string(pr.buf[start:pr.off])
	pr.off++
	return s, nil
}

func (pr *payloadReader) done() bool { return pr.off == len(pr.buf) }
