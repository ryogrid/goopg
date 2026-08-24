package executor

import (
	"bytes"
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math"
	"math/big"
	"math/bits"
	mathrand "math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/datetime"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser/sqlkeywords"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/mb"
)

// sessionPRNG is the per-process random-number generator used by random(),
// setseed() and random_normal(). Protected by sessionPRNGMu. M0097-0071.
var (
	sessionPRNG   = mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	sessionPRNGMu sync.Mutex
)

// ExecError is the executor's structured error. Code is a SQLSTATE
// value the wire-protocol path forwards to ErrorResponse.
type ExecError struct {
	Code          string
	Message       string
	Detail        string // optional DETAIL message for wire protocol. M0097-0003.
	Hint          string // optional HINT message for wire protocol. M0097-0004.
	Context       string // optional CONTEXT (WHERE) message for wire protocol. M0097-0022.
	Pos           int
	ConditionName string // set for RAISE condition_name; used for exception matching. M0097-0003.
}

func (e *ExecError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("executor error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

// evalExpr resolves a planner expression against the input row.
// Operators that produce no input pass nil for `row`; in that case
// any ColumnRef resolution is an internal error.
//
// Thin wrapper over evalExprSlot — Row callers continue to work
// unchanged. New slot-aware sites (NLI/MHJ predicate eval) call
// evalExprSlot directly so VirtualSlot composition can read via
// slot.Get(col) without materializing a Row per emitted match.
// oidToBuiltinTypeName converts a PostgreSQL built-in type OID to its canonical
// type name (e.g. 16 → "boolean"). Returns "" if the OID is unknown.
func oidToBuiltinTypeName(oid uint32) string {
	switch oid {
	case 16:
		return "boolean"
	case 17:
		return "bytea"
	case 18:
		return "\"char\""
	case 19:
		return "name"
	case 20:
		return "bigint"
	case 21:
		return "smallint"
	case 23:
		return "integer"
	case 25:
		return "text"
	case 26:
		return "oid"
	case 22:
		return "int2vector"
	case 30:
		return "oidvector"
	case 700:
		return "real"
	case 701:
		return "double precision"
	case 1043:
		return "character varying"
	case 1082:
		return "date"
	case 1083:
		return "time without time zone"
	case 1114:
		return "timestamp without time zone"
	case 1184:
		return "timestamp with time zone"
	case 650:
		return "cidr"
	case 774:
		return "macaddr8"
	case 829:
		return "macaddr"
	case 869:
		return "inet"
	case 600:
		return "point"
	case 601:
		return "lseg"
	case 602:
		return "path"
	case 603:
		return "box"
	case 604:
		return "polygon"
	case 628:
		return "line"
	case 718:
		return "circle"
	case 3614:
		return "tsvector"
	case 3615:
		return "tsquery"
	case 142:
		return "xml"
	case 790:
		return "money"
	case 1560:
		return "bit"
	case 1562:
		return "bit varying"
	case 1186:
		return "interval"
	case 1266:
		return "time with time zone"
	case 1700:
		return "numeric"
	case 2278:
		return "void"
	case 2279:
		return "trigger"
	case 2281:
		return "internal"
	case 2950:
		return "uuid"
	case 3220:
		return "pg_lsn"
	case 2970:
		return "txid_snapshot"
	case 5038:
		return "pg_snapshot"
	case 5069:
		return "xid8"
	case 27:
		// tid: tuple identifier, bare name. DU-002 slice 79.
		return "tid"
	case 28:
		// xid: 32-bit transaction id, bare name. Slice 79.
		return "xid"
	case 29:
		// cid: 32-bit command id, bare name. Slice 79.
		return "cid"
	// DU-002 slice 80: the OID-reference ("reg*") family, bare names.
	case 24:
		return "regproc"
	case 2202:
		return "regprocedure"
	case 2203:
		return "regoper"
	case 2204:
		return "regoperator"
	case 2205:
		return "regclass"
	case 2206:
		return "regtype"
	case 3734:
		return "regconfig"
	case 3769:
		return "regdictionary"
	case 4089:
		return "regnamespace"
	case 4096:
		return "regrole"
	case 4191:
		return "regcollation"
	case 4072:
		// jsonpath: SQL/JSON path, varlena, bare name. DU-002 slice 84.
		return "jsonpath"
	case 1790:
		// refcursor: cursor-name reference, varlena, bare name. DU-002 slice 85.
		return "refcursor"
	case 1033:
		// aclitem: access-control-list item, fixed-length, bare name. DU-002 slice 86.
		return "aclitem"
	// Array types
	case 1000:
		return "boolean[]"
	case 1001:
		return "bytea[]"
	case 1002:
		return "\"char\"[]"
	case 1003:
		return "name[]"
	case 1005:
		return "smallint[]"
	case 1007:
		return "integer[]"
	case 1009:
		return "text[]"
	case 1015:
		return "character varying[]"
	case 1016:
		return "bigint[]"
	case 1021:
		return "real[]"
	case 1022:
		return "double precision[]"
	case 1028:
		return "oid[]"
	case 1115:
		return "timestamp without time zone[]"
	case 1182:
		return "date[]"
	case 1183:
		return "time without time zone[]"
	case 1270:
		return "time with time zone[]"
	case 1185:
		return "timestamp with time zone[]"
	case 651:
		return "cidr[]"
	case 775:
		return "macaddr8[]"
	case 1040:
		return "macaddr[]"
	case 1041:
		return "inet[]"
	case 1017:
		return "point[]"
	case 1018:
		return "lseg[]"
	case 1019:
		return "path[]"
	case 1020:
		return "box[]"
	case 1027:
		return "polygon[]"
	case 629:
		return "line[]"
	case 719:
		return "circle[]"
	case 3643:
		return "tsvector[]"
	case 3645:
		return "tsquery[]"
	case 143:
		return "xml[]"
	case 791:
		return "money[]"
	case 1561:
		return "bit[]"
	case 1563:
		return "bit varying[]"
	case 1187:
		return "interval[]"
	case 3221:
		return "pg_lsn[]"
	case 2949:
		return "txid_snapshot[]"
	case 5039:
		return "pg_snapshot[]"
	case 271:
		return "xid8[]"
	case 1010:
		return "tid[]"
	case 1011:
		return "xid[]"
	case 1012:
		return "cid[]"
	// DU-002 slice 80: the OID-reference ("reg*") array types, bare names + [].
	case 1008:
		return "regproc[]"
	case 2207:
		return "regprocedure[]"
	case 2208:
		return "regoper[]"
	case 2209:
		return "regoperator[]"
	case 2210:
		return "regclass[]"
	case 2211:
		return "regtype[]"
	case 3735:
		return "regconfig[]"
	case 3770:
		return "regdictionary[]"
	case 4090:
		return "regnamespace[]"
	case 4097:
		return "regrole[]"
	case 4192:
		return "regcollation[]"
	case 1231:
		return "numeric[]"
	case 2951:
		return "uuid[]"
	case 4073:
		// _jsonpath: jsonpath has no typmod, bare element name + []. Slice 84.
		return "jsonpath[]"
	case 2201:
		// _refcursor: refcursor has no typmod, bare element name + []. Slice 85.
		return "refcursor[]"
	case 1034:
		// _aclitem: aclitem has no typmod, bare element name + []. Slice 86.
		return "aclitem[]"
	default:
		return ""
	}
}

func evalExpr(e optimizer.Expr, row Row, ctx *Context) (Datum, error) {
	var slot SlotView
	if row != nil {
		slot = rowSlotView(row)
	}
	return evalExprSlot(e, slot, ctx)
}

// evalExprSlot is the slot-aware sibling of evalExpr. ColumnRef
// reads via slot.Get(col); helpers that push ctx.OuterRows
// (Subquery/In/Exists/Extract/FuncCall/CaseExpr) reach back to
// Row via slotToRow — those paths are out of scope for the M0071
// keystone refactor and stay on Row to limit blast radius.
//
// nil slot is permitted (mirrors the M0054 contract for valuesOp /
// limitOp / DML operators that have no input row).
//
// M0074-0001: ColumnRef is the dominant case in Q5 (predicate +
// projection refs over filtered lineitem rows). Hoisted to a
// fast-path early-return ahead of the type switch — saves the
// 12-arm type-test sequence on the hot path. evalExprSlot cum
// CPU at M0073-final was 68.68 % cum; this hoist trims dispatch.
func evalExprSlot(e optimizer.Expr, slot SlotView, ctx *Context) (Datum, error) {
	// Fast path: ColumnRef. M0074-0001 hoist.
	if cref, ok := e.(*optimizer.ColumnRef); ok {
		if slot == nil {
			return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d on nil slot", cref.Name, cref.Index)}
		}
		if rs, ok := slot.(rowSlotView); ok {
			if cref.Index < 0 || cref.Index >= len(rs) {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of range", cref.Name, cref.Index)}
			}
		}
		if vs, ok := slot.(*VirtualSlot); ok {
			if cref.Index < 0 || cref.Index >= vs.Width() {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of VirtualSlot range %d (chained-NLI?)", cref.Name, cref.Index, vs.Width())}
			}
		}
		// Catch-all bounds check. The two guards above cover only
		// rowSlotView and *VirtualSlot; *MaterializedSlot (slot.go
		// Get is a bare `s.row[col]`) and *Slot were unchecked, so a
		// stale planner index panicked raw instead of raising an
		// error. That panic surfaced during the hash-join build-side
		// drain, which gatherOp.Open runs in the LEADER goroutine —
		// outside ParallelGroup.Go's recover — so it escaped to
		// serveConn, which logs and closes the socket. The client saw
		// "connection lost" and the harness restarted the server
		// (TPC-DS Q8: "index out of range [57] with length 1").
		// PostgreSQL's contract is that an ERROR kills the statement,
		// not the backend. SlotView itself carries only Get/IsNull, so
		// the check is written per concrete type rather than widening
		// the interface on this hot path.
		if ms, ok := slot.(*MaterializedSlot); ok {
			if w := ms.Width(); cref.Index < 0 || cref.Index >= w {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of MaterializedSlot range %d", cref.Name, cref.Index, w)}
			}
		}
		if sl, ok := slot.(*Slot); ok {
			if w := sl.Width(); cref.Index < 0 || cref.Index >= w {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of Slot range %d", cref.Name, cref.Index, w)}
			}
		}
		return slot.Get(cref.Index), nil
	}
	switch x := e.(type) {
	case *optimizer.OuterColumnRef:
		// Look up the row from the lexical-scope stack pushed
		// by evalSubquery/evalInExpr/evalExistsExpr before the
		// inner plan runs. Level 1 is the immediate parent.
		idx := len(ctx.OuterRows) - x.Level
		if idx < 0 || idx >= len(ctx.OuterRows) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/level=%d out of range (depth=%d)", x.Name, x.Level, len(ctx.OuterRows))}
		}
		outer := ctx.OuterRows[idx]
		if x.Index < 0 || x.Index >= len(outer) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/idx=%d out of range (width=%d)", x.Name, x.Index, len(outer))}
		}
		return outer[x.Index], nil
	case *optimizer.ExecParamRef:
		// PARAM_EXEC slot read (D4.1). The enclosing sublink's eval
		// site bound the slot via bindSubPlanParams before running
		// this plan; position-independent, no scope stack involved.
		// An unset read is a lowering bug, never a NULL.
		if ctx == nil || x.ID < 0 || x.ID >= len(ctx.ParamExec) || !ctx.ParamSet[x.ID] {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("SubPlan parameter $%d read before assignment", x.ID)}
		}
		return ctx.ParamExec[x.ID], nil
	case *optimizer.CaseExpr:
		return evalCaseExpr(x, slotToRow(slot), ctx)
	case *optimizer.SubqueryExpr:
		return evalSubquery(x, slotToRow(slot), ctx)
	case *optimizer.ArraySubqueryExpr:
		return evalArraySubquery(x, slotToRow(slot), ctx)
	case *optimizer.CollateExpr:
		// Pass-through: evaluate operand and ignore collation at runtime. M0097-0127.
		return evalExprSlot(x.Operand, slot, ctx)
	case *optimizer.MultiAssignSubqElem:
		return evalMultiAssignSubqElem(x, slotToRow(slot), ctx)
	case *optimizer.LikeEscapePattern:
		return evalLikeEscapePattern(x, slot, ctx)
	case *optimizer.InExpr:
		return evalInExpr(x, slot, ctx)
	case *optimizer.ExistsExpr:
		return evalExistsExpr(x, slotToRow(slot), ctx)
	case *optimizer.RowExpr:
		return evalRowExpr(x, slot, ctx)
	case *optimizer.TypedStringLit:
		return evalTypedStringLit(x, ctx)
	case *optimizer.IntervalLit:
		return evalIntervalLit(x)
	case *optimizer.ExtractExpr:
		return evalExtract(x, slotToRow(slot), ctx)
	case *optimizer.IntegerConst:
		return Datum{Kind: KindInt, Int: x.Value}, nil
	case *optimizer.TableOidExpr:
		// `tableoid` system column for a non-partitioned base
		// relation: the binding's table OID is fixed at plan time
		// (resolveTableoidForBinding). Partitioned bindings instead
		// resolve through a real ColumnRef into the per-leaf
		// `tableoid` slot added by the partition-union wrapper.
		// M0100-0005y.
		return Datum{Kind: KindInt, Int: int64(x.TableOID)}, nil
	case *optimizer.MergeActionExpr:
		// MERGE RETURNING merge_action() — returns the action text for this row. M0100-0007.
		switch ctx.MergeAction {
		case optimizer.MergeActionInsert:
			return NewStringDatum("INSERT"), nil
		case optimizer.MergeActionUpdate:
			return NewStringDatum("UPDATE"), nil
		case optimizer.MergeActionDelete:
			return NewStringDatum("DELETE"), nil
		}
		return Datum{}, nil
	case *optimizer.MergeWholeRowRef:
		// MERGE RETURNING old/new composite. nil row → true NULL. M0100-0007.
		var row Row
		if x.IsOld {
			row = ctx.MergeOldRow
		} else {
			row = ctx.MergeNewRow
		}
		if row == nil {
			return Datum{}, nil
		}
		return evalMergeWholeRow(row), nil
	case *optimizer.CTIDExpr:
		// `ctid` system column: per-row TID injected by seqScanOp
		// into MaterializedSlot.hasCTID. M0097-0038.
		// M0097-0062: also handle opnode *Slot which propagates ctid
		// from MaterializedSlot via fillFromTupleSlot.
		switch s := slot.(type) {
		case *MaterializedSlot:
			if s.hasCTID {
				return NewStringDatum(fmt.Sprintf("(%d,%d)", s.ctidBlock, s.ctidOff)), nil
			}
		case *Slot:
			if s.hasCTID {
				return NewStringDatum(fmt.Sprintf("(%d,%d)", s.ctidBlock, s.ctidOff)), nil
			}
		}
		return NullDatum, nil
	case *optimizer.NumericConst:
		m, s, err := parseNumeric(x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(), Message: err.Error()}
		}
		return newNumeric(m, int(s)), nil
	case *optimizer.StringConst:
		return NewStringDatum(x.Value), nil
	case *optimizer.NullConst:
		return NullDatum, nil
	case *optimizer.BooleanConst:
		return NewBoolDatum(x.Value), nil
	case *optimizer.ParamRef:
		if x.Number < 1 || x.Number > len(ctx.Params) {
			return Datum{}, &ExecError{Code: "08P01", Pos: x.Pos(), Message: fmt.Sprintf("parameter $%d not bound", x.Number)}
		}
		return ctx.Params[x.Number-1], nil
	// ColumnRef handled by the M0074-0001 fast-path above.
	case *optimizer.UnaryOp:
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(x.Op, operand, x.Pos())
	case *optimizer.CastExpr:
		v, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// `CAST(x AS interval <qualifier>)` / `x::interval <qualifier>` with an
		// interval typmod (a field qualifier and/or SECOND precision): the low
		// field changes the bare-magnitude default unit during parsing, so it
		// cannot be applied as a post-hoc truncation of an already-parsed value.
		// unimplemented_feat #5(d-iv).
		if x.Typmod != 0 && strings.EqualFold(x.TargetType, "interval") {
			return applyIntervalCastTypmod(v, x.Typmod, x.Pos())
		}
		// `::regclass` is catalog-aware in both directions:
		//   - `<oid>::regclass` renders as the relation name (PG's regclassout)
		//   - `<text>::regclass` resolves the relation name to its numeric OID
		// The latter is the exact pgbench probe shape:
		//   `... WHERE oid = $1::pg_catalog.regclass`.
		// `::regtype` resolves a type name to its OID (string→oid).
		// `::regproc` resolves a function name to its OID (string→oid).
		if strings.EqualFold(x.TargetType, "regproc") || strings.EqualFold(x.TargetType, "regprocedure") {
			isProcedure := strings.EqualFold(x.TargetType, "regprocedure")
			if v.Kind == KindString && ctx != nil && ctx.Catalog != nil {
				funcName := strings.TrimSpace(v.StringValue())
				raw := funcName
				// regprocedurein (regproc.c:244) splits the arg list off the name
				// (parseNameAndArgTypes) before parsing; regprocin does not. Both
				// feed the name through stringToQualifiedNameList, so a quoted
				// routine name must be unquoted before the lookups, and the 42883
				// keeps the RAW input (regprocin/regprocedurein's errmsg). This is
				// the input half of RegOut's quote-emission (the 71st slice) — the
				// two casts are sibling renderers and must agree (Hard-won Rule
				// #2). M0119-0006 (72nd slice, deferral row 1341).
				if isProcedure {
					funcName = regprocedureNamePart(funcName)
				}
				schema, name, nameOK := splitRegQualifiedName(funcName)
				if !nameOK {
					return NullDatum, &ExecError{Code: "42602", Pos: x.Pos(), Message: "invalid name syntax"}
				}
				if rs := ctx.Catalog.Routines(); rs != nil {
					// Thread the connection's dbOid like regIdentifierInput's
					// regproc/regprocedure arm does — an unscoped LookupByName
					// resolves DefaultDBOid and leaks another database's
					// same-named routine (deferral row 1348). Builtins fall
					// through to the global pg_proc index below (pg_catalog is
					// implicitly visible everywhere).
					candidates := rs.LookupByName(parser.ObjectName{Schema: schema, Name: name}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
					if len(candidates) > 0 {
						return NewIntDatum(int64(candidates[0].OID)), nil
					}
				}
				// Not a user-created CREATE FUNCTION routine — fall back to the
				// curated builtin pg_proc table (the same two-tier lookup
				// resolveOpClassFunction uses for CREATE OPERATOR CLASS's own
				// FUNCTION clause), so `'int4eq'::regproc` resolves like real PG
				// instead of erroring on any bare builtin name. M0119-0006
				// (005_opclass_damage UPDATE-path prerequisite).
				if bp, found := catalog.LookupBuiltinProc(name); found {
					return NewIntDatum(int64(bp.OID)), nil
				}
				// CREATE AGGREGATE routines never land in the Routines()
				// registry (they live in their own userAggregates map — see
				// InMemory.RegisterUserAggregate) even though
				// writeAggregateCatalogRows gives them a real pg_proc heap
				// row with prokind='a'. Sibling of regIdentifierInput's
				// identical fallback (Hard-won Rule #2) — without it
				// `'myavg'::regproc` misses despite the pg_proc row being
				// visible to a plain SELECT. M0134-0108.
				if agg, ok := ctx.Catalog.LookupUserAggregateByName(name); ok {
					return NewIntDatum(int64(agg.OID)), nil
				}
				return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("function %q does not exist", raw)}
			}
			// An OID input (oid→regproc/regprocedure) renders via
			// regprocout/regprocedureout: InvalidOid (0) becomes "-", matching
			// PG's regproc.c. pg_dump's getForeignDataWrappers (and
			// amhandler/opclass getters) cast `<col>::regproc` and compare the
			// result to "-" to decide whether to emit a HANDLER/VALIDATOR
			// clause; a bare "0" would spuriously emit one. DU-002 slice 375.
			if v.Kind == KindInt {
				oid := v.Int
				if oid == 0 {
					return NewStringDatum("-"), nil
				}
				// regprocedure additionally renders the INPUT argument-type
				// list ("name(argtype1,argtype2)", format_procedure) so an
				// overloaded name is disambiguated — pg_dump relies on this
				// for pg_amproc/pg_cast/pg_transform function-OID columns.
				// DU-002 (M0119-0004), combined follow-up to the deferred
				// pg_amop/pg_amproc member store (backlog item regarding
				// regoper/regoperator/regprocedure resolution).
				if isProcedure {
					var routines *catalog.Routines
					if ctx != nil && ctx.Catalog != nil {
						routines = ctx.Catalog.Routines()
					}
					// format_procedure (regproc.c:326) qualifies only the NAME —
					// quote_qualified_identifier(schema,name)(arglist) when the
					// routine's schema is off the effective search_path, else
					// quote_identifier(name)(arglist). Rendered through the SAME
					// regOutQualified rule RegOut's regprocedure arm uses, so the
					// ::regprocedure cast and a regprocedure-typed column cannot
					// drift apart (Hard-won Rule #2). Previously this prefixed the
					// WHOLE signature (`schema + "." + sig`), which skipped the
					// quote_identifier on a mixed-case/quoted routine name.
					// M0119-0006 (71st slice, deferral row 1338).
					if schema, name, argTypes, ok := catalog.RegprocedureNameParts(uint32(oid), routines); ok {
						// format_procedure_extended's format_type_be per-arg
						// qualification (deferral row 1342): an input arg type
						// whose namespace is off the session's search_path renders
						// schema-qualified. Same regprocedureArglist the column/
						// COPY renderers use, so the cast cannot drift from them.
						qualify := !RegObjectSchemaVisible(ctx, schema)
						argVisible := func(s string) bool { return RegObjectSchemaVisible(ctx, s) }
						return NewStringDatum(regOutQualified(schema, name, qualify) + "(" + regprocedureArglist(argTypes, argVisible) + ")"), nil
					}
					return v, nil
				}
				// A non-zero OID resolves to the function name (built-in via
				// catalog.RegprocName's generated pg_proc.dat index, or a
				// CREATE FUNCTION via the live routine registry) — previously
				// this fell through to `return v, nil`, leaving the cast a
				// no-op that rendered the raw OID instead of a name at
				// output time. Falls back to the raw datum (still tagged
				// regproc for downstream formatting) only if neither source
				// resolves it.
				if name, ok := catalog.RegprocName(uint32(oid)); ok {
					return NewStringDatum(name), nil
				}
				if ctx != nil && ctx.Catalog != nil {
					if r := ctx.Catalog.Routines().LookupByOID(uint32(oid)); r != nil {
						// Schema-qualify exactly like the regprocedure/
						// regoperator branches below: pg_dump's own connection
						// always runs with search_path='', so
						// ProcedureIsVisible never finds an unqualified
						// user-defined function visible. Confirmed via a live
						// PG 18.3 diff for pg_event_trigger.evtfoid::regproc
						// (dumpEventTrigger emits `public.et_func()`, not
						// `et_func()`, for a public-schema trigger function).
						// DU-002 (M0119-0004).
						if !RegObjectSchemaVisible(ctx, r.Schema) {
							return NewStringDatum(r.Schema + "." + r.Name), nil
						}
						return NewStringDatum(r.Name), nil
					}
				}
			}
			return v, nil
		}
		if strings.EqualFold(x.TargetType, "regoper") || strings.EqualFold(x.TargetType, "regoperator") {
			// An OID input renders via regoperout/regoperatorout: InvalidOid
			// (0) becomes the literal "0" (unlike regproc/regtype's "-" —
			// regproc.c's regoperout/regoperatorout both special-case
			// InvalidOid to "0"). regoperator additionally renders the
			// (lefttype,righttype) pair ("NONE" for a missing/unary side,
			// format_operator) so dumpOpclass/dumpOpfamily's
			// amopopr::pg_catalog.regoperator cast resolves to a real name
			// instead of a bare OID. Only user-defined operators are
			// resolvable (goopg has no builtin-operator catalog — deferred,
			// see the ledger). DU-002 (M0119-0004) slice 411.
			// A `'name(left,right)'::regoperator` STRING input is the reverse
			// direction — regoperatorin (regproc.c) — and must resolve to the
			// operator's numeric OID (not a rendered name string) so
			// `WHERE objid = '===(bool,bool)'::regoperator` compares
			// correctly against an oid-typed column; mirrors the `regclass`
			// CastExpr arm's identical asymmetry (KindInt to name string,
			// KindString to raw OID), confirmed against a live PG 18.3 oracle
			// via alter_operator.sql. Only the 2-arg `name(a,b)` shape is
			// handled (regoperatorNameAndArgs) — a bare name with no parens
			// falls through to the unresolved `return v, nil` below
			// unchanged, same as before this fix. `regoper` (no arg list at
			// all) is NOT handled here; it stays on the pre-fix pass-through
			// path — a bare-name lookup would still need ambiguity detection
			// this slice doesn't add. M0134-0089.
			if v.Kind == KindString && strings.EqualFold(x.TargetType, "regoperator") && ctx != nil && ctx.Catalog != nil {
				raw := strings.TrimSpace(v.StringValue())
				if oid, perr := strconv.ParseUint(raw, 10, 32); perr == nil {
					return NewIntDatum(int64(oid)), nil
				}
				if name, leftType, rightType, ok := regoperatorNameAndArgs(raw); ok {
					if im, ok2 := ctx.Catalog.(*catalog.InMemory); ok2 {
						var leftOID, rightOID uint32
						if leftType != "" {
							leftOID = catalog.TypeNameToOID(leftType)
						}
						if rightType != "" {
							rightOID = catalog.TypeNameToOID(rightType)
						}
						if op, found := im.LookupUserOperatorByNameAndTypeOIDs(name, leftOID, rightOID); found {
							return NewIntDatum(int64(op.OID)), nil
						}
					}
					if bop, found := catalog.LookupBuiltinOperator(name, leftType, rightType); found {
						return NewIntDatum(int64(bop.OID)), nil
					}
					return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("operator does not exist: %s", raw)}
				}
			}
			if v.Kind == KindInt {
				oid := v.Int
				if oid == 0 {
					return NewStringDatum("0"), nil
				}
				if ctx != nil && ctx.Catalog != nil {
					if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
						if strings.EqualFold(x.TargetType, "regoperator") {
							if schema, sig, found := im.RegoperatorNameAndSchema(uint32(oid)); found {
								if !RegObjectSchemaVisible(ctx, schema) {
									return NewStringDatum(schema + "." + sig), nil
								}
								return NewStringDatum(sig), nil
							}
						} else if op := im.LookupUserOperatorByOID(uint32(oid)); op != nil {
							return NewStringDatum(op.Name), nil
						} else if bop, found := catalog.LookupBuiltinOperatorByOID(uint32(oid)); found {
							return NewStringDatum(bop.Name), nil
						}
					}
				}
			}
			return v, nil
		}
		if strings.EqualFold(x.TargetType, "regtype") && ctx != nil && ctx.Catalog != nil {
			switch v.Kind {
			case KindString:
				typName := strings.ToLower(strings.TrimSpace(v.StringValue()))
				// Check for oidvector (space-separated OIDs like "25 1082").
				// Parser strips [] from ::regtype[], so oidvectors hit this branch.
				if strings.ContainsRune(typName, ' ') {
					parts := strings.Fields(typName)
					names := make([]string, len(parts))
					for i, p := range parts {
						oid, parseErr := strconv.ParseInt(p, 10, 64)
						if parseErr != nil {
							names[i] = p
							continue
						}
						if name := oidToBuiltinTypeName(uint32(oid)); name != "" {
							names[i] = name
						} else {
							names[i] = p
						}
					}
					result := fmt.Sprintf("[0:%d]={%s}", len(names)-1, strings.Join(names, ","))
					return NewStringDatum(result), nil
				}
				// Empty oidvector → empty array
				if typName == "" {
					return NewStringDatum("{}"), nil
				}
				// Try as an OID integer string (e.g. "16" → "boolean").
				// InvalidOid (0) renders as "-", matching PG's regtypeout
				// (src/backend/utils/adt/regproc.c) — e.g. pg_opclass.opckeytype
				// is 0 when no STORAGE clause was given.
				if oid, parseErr := strconv.ParseInt(typName, 10, 64); parseErr == nil {
					if oid == 0 {
						return NewStringDatum("-"), nil
					}
					if name := oidToBuiltinTypeName(uint32(oid)); name != "" {
						return NewStringDatum(name), nil
					}
					if uname, ok := userTypeNameForOID(ctx.Catalog, uint32(oid), func(s string) bool { return !RegObjectSchemaVisible(ctx, s) }); ok {
						return NewStringDatum(uname), nil
					}
					return NewStringDatum(typName), nil
				}
				// Try as a type name → return OID, across all four
				// user-type kinds (enum/domain/composite/range/multirange),
				// mirroring userTypeNameForOID's reverse direction.
				if oid, ok := userTypeOIDForName(ctx.Catalog, typName); ok {
					return NewIntDatum(int64(oid)), nil
				}
				// Built-in type name → return itself
				return v, nil
			case KindInt:
				// OID integer → type name; InvalidOid (0) renders as "-" (see above).
				if v.Int == 0 {
					return NewStringDatum("-"), nil
				}
				if name := oidToBuiltinTypeName(uint32(v.Int)); name != "" {
					return NewStringDatum(name), nil
				}
				// A user-defined enum/domain/composite/range/multirange
				// type's pg_type OID is dynamically allocated; oidToBuiltinTypeName
				// only knows PG's static OIDs, so resolve it here — previously
				// this rendered the bare numeric OID (e.g. `atttypid::regtype`
				// for a range-typed column showed "16422" instead of
				// "myrange"), diverging from PG's regtypeout (regproc.c). The
				// name is schema-qualified (with the type's ACTUAL schema) when
				// that schema is not visible on the effective search_path — a
				// per-schema predicate, unlike the fixed-"public" check the
				// regproc/regoperator casts still use. DU-002 (M0110-0001)
				// regtype/format_type unification follow-up; M0119-0006 slice B.
				if uname, ok := userTypeNameForOID(ctx.Catalog, uint32(v.Int), func(s string) bool { return !RegObjectSchemaVisible(ctx, s) }); ok {
					return NewStringDatum(uname), nil
				}
				return NewStringDatum(fmt.Sprintf("%d", v.Int)), nil
			}
		}
		if strings.EqualFold(x.TargetType, "regtype[]") && ctx != nil {
			// oidvector (space-separated OID strings) → type name array like [0:1]={text,date}
			// Single-element oidvectors may arrive as KindInt (TypedVirtualCell
			// parses single-OID oidvector columns to IntegerConst for numeric sort).
			var oidParts []string
			switch v.Kind {
			case KindString:
				sv := v.StringValue()
				// A brace-delimited array LITERAL (name→OID direction, e.g.
				// '{int4}'::regtype[]) is not an oidvector — it must fall
				// through to evalCastTyped/evalCast's "regtype[]" arm below,
				// which resolves each element via regIdentifierInput. Only
				// genuine oidvector text (space-separated OIDs, no braces)
				// takes this OID→name branch. M0134-0005 S07.
				if len(sv) == 0 || sv[0] != '{' {
					oidParts = strings.Fields(sv)
				}
			case KindInt:
				oidParts = []string{fmt.Sprintf("%d", v.Int)}
			}
			if oidParts != nil {
				if len(oidParts) == 0 {
					return NewStringDatum("{}"), nil
				}
				names := make([]string, len(oidParts))
				for i, p := range oidParts {
					oid, err := strconv.ParseInt(p, 10, 64)
					if err != nil {
						names[i] = p
						continue
					}
					if name := oidToBuiltinTypeName(uint32(oid)); name != "" {
						names[i] = name
					} else {
						names[i] = p
					}
				}
				result := fmt.Sprintf("[0:%d]={%s}", len(names)-1, strings.Join(names, ","))
				return NewStringDatum(result), nil
			}
		}
		if strings.EqualFold(x.TargetType, "regclass") && ctx != nil && ctx.Catalog != nil {
			// Scope every lookup below to the connection's own database
			// namespace — mirrors every other per-dbOid site (e.g.
			// operators_fk.go, operators_sequence.go). Without this, both cast
			// directions silently resolved against DefaultDBOid only,
			// regardless of which database the connection was actually on: an
			// `<oid>::regclass` for a table in a distinct CREATE DATABASE'd
			// dbOid rendered the bare numeric OID instead of the relation
			// name, and `'name'::regclass` couldn't find that table's OID at
			// all. M0122-0007 4e follow-up 33.
			connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
			switch v.Kind {
			case KindInt:
				// InvalidOid (0) renders as "-", matching PG's regclassout
				// (src/backend/utils/adt/regproc.c). Without this guard a
				// `reltoastrelid::regclass` for a table with no TOAST relation
				// (reltoastrelid = 0) matches the first virtual relation whose
				// OID is unset (also 0), e.g. information_schema.routines.
				// M0118-0008 (reindex-concurrently-toast probing).
				if v.Int == 0 {
					return NewStringDatum("-"), nil
				}
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if tbl, found := im.LookupTableByOID(uint32(v.Int), connDBOid); found && tbl != nil {
						return NewStringDatum(tbl.Name), nil
					}
					// Synthetic TOAST relation OIDs (parent OID + 100M) live only in
					// the virtual pg_class builder, not c.tables, so reconstruct the
					// schema-qualified pg_toast.pg_toast_<oid> name PG's regclassout
					// would emit. M0118-0008 TOAST-exposure slice 2 (0118-0084).
					if name, found := im.ToastRelName(uint32(v.Int), connDBOid); found {
						return NewStringDatum(name), nil
					}
				}
				// Also resolve index OIDs to index names. M0097-0023.
				for _, idx := range ctx.Catalog.AllIndexes(connDBOid) {
					if idx.OID == uint32(v.Int) {
						return NewStringDatum(idx.Name), nil
					}
				}
			case KindString:
				// Shared SplitIdentifierString port: a quoted relation name
				// (`'"My Table"'::regclass`) must be unquoted before the catalog
				// lookup, and a syntax error raises regclassin's 42602.
				// M0119-0006 (72nd slice, deferral row 1341) — the input half of
				// RegOut's quote-emission; sibling of regIdentifierInput.
				schema, rel, nameOK := splitRegQualifiedName(v.StringValue())
				if !nameOK {
					return NullDatum, &ExecError{Code: "42602", Pos: x.Pos(), Message: "invalid name syntax"}
				}
				objName := parser.ObjectName{Schema: schema, Name: rel}
				if tbl, found := ctx.Catalog.LookupTable(objName, connDBOid); found && tbl != nil {
					return NewIntDatum(int64(tbl.OID)), nil
				}
				// Also resolve index names: 'idx_name'::regclass returns the index OID.
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if idx, found := im.LookupIndex(objName, connDBOid); found && idx != nil {
						return NewIntDatum(int64(idx.OID)), nil
					}
				}
			}
		}
		if strings.EqualFold(x.TargetType, "regdictionary") && ctx != nil && ctx.Catalog != nil {
			// int → regdictionary resolves a pg_ts_dict OID to its bare name
			// (no schema qualification, mirroring the regclass branch above).
			// pg_dump's dumpTSConfig casts pg_ts_config_map.mapdict this way to
			// re-emit ADD MAPPING's `WITH <dictname>` clause. DU-002 slice 446
			// (M0119-0004).
			if v.Kind == KindInt {
				oid := uint32(v.Int)
				for name, builtinOID := range catalog.BuiltinTSDictOID {
					if builtinOID == oid {
						return NewStringDatum(name), nil
					}
				}
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					for _, ud := range im.ListUserTSDicts() {
						if ud.OID == oid {
							return NewStringDatum(ud.Name), nil
						}
					}
				}
			}
		}
		// ── Enum cast validation ─────────────────────────────────────────
		// If the target type is a user-defined enum and the input is a
		// non-NULL, non-array string, verify the value is a valid enum label.
		// Guards: skip array types (target ends with []), skip array literals
		// (value starts with {), skip NULL values. M0097-0063.
		if ctx != nil && ctx.Catalog != nil && !v.IsNull() &&
			!strings.HasSuffix(x.TargetType, "[]") {
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
				if et, isEnum := im.LookupEnum(x.TargetType); isEnum {
					strVal := v.StringValue()
					// Skip array literals (e.g. '{red,green,blue}'::rainbow[]).
					if len(strVal) == 0 || strVal[0] != '{' {
						var matchedSort float64
						found := false
						for _, label := range et.Values {
							if strings.EqualFold(label.Label, strVal) {
								matchedSort = label.SortOrder
								found = true
								break
							}
						}
						if !found {
							return NullDatum, &ExecError{
								Code:    "22P02",
								Pos:     x.Pos(),
								Message: fmt.Sprintf("invalid input value for enum %s: %q", et.Name, strVal),
							}
						}
						// Reject unsafe values added in the current uncommitted transaction.
						if isUnsafeEnumValue(ctx, x.TargetType, strVal) {
							return Datum{}, enumUnsafeError(strVal, et.Name, x.Pos())
						}
						// Return KindEnum for correct ORDER BY semantics.
						return NewEnumDatum(matchedSort, strVal), nil
					}
				}
			}
		}
		// Bare `char`/CHARACTER (no explicit length) is grammar-synthesized to
		// TargetType=="char" with Typmod==1 by the planner (mirroring bpchar's
		// implicit length-1 default), which is indistinguishable at the string
		// level from the quoted `"char"` identifier (pg_type OID 18, Typmod==0,
		// a genuinely different fixed 1-byte internal type). Rename the former
		// to "bpchar" for this call only so evalCast's OID-18-specific
		// octal-escape/truncation branch fires solely for the true OID-18 form.
		// M0122-0005.
		castTargetType := x.TargetType
		if castTargetType == "char" && x.Typmod > 0 {
			castTargetType = "bpchar"
		}
		result, err := evalCastTyped(v, castTargetType, x.SourceType, x.Pos(), ctx)
		if err != nil {
			return Datum{}, err
		}
		// Domain CHECK constraint enforcement: VALUE IN (...). M0097-domain-check.
		// A domain may carry several CHECK (VALUE IN (...)) constraints; each must
		// admit the value. DU-002 slice 385 (multi-CHECK).
		if ctx != nil && ctx.Catalog != nil {
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
				if dom, isDomain := im.LookupDomain(x.TargetType, ctx.CurrentDatabaseOid); isDomain {
					// Get the string label of the value being cast. Format()
					// (not StringValue(), which only extracts KindString's Buf
					// payload) renders every Kind's canonical text form, e.g.
					// KindInt's Int field as a decimal string. M0122-0005.
					label := result.Format()
					for _, ck := range dom.Checks {
						if len(ck.InValues) == 0 {
							continue
						}
						found := false
						for _, allowed := range ck.InValues {
							if strings.EqualFold(label, allowed) {
								found = true
								break
							}
						}
						if !found {
							return Datum{}, &ExecError{
								Code:    "23514",
								Pos:     x.Pos(),
								Message: fmt.Sprintf("value for domain %s violates check constraint %q", strings.ToLower(dom.Name), ck.Name),
							}
						}
					}
				}
			}
		}
		// Apply varchar(n)/bpchar(n)/char(n) typmod: truncate to the declared
		// length. PostgreSQL's explicit (::) cast truncates silently rather
		// than raising 22001 (that error is assignment/INSERT-coercion-only,
		// already enforced separately by codec.go's coerceTextLikeDatum) —
		// verified against real PG 18.3: `'abcdef'::varchar(3)` → 'abc', no
		// error. bpchar/char additionally right-pad short values with spaces
		// in real PG; goopg's Datum has no distinct padded representation for
		// bpchar (coerceTextLikeDatum stores it trimmed too), so padding is a
		// separate, broader gap left deferred. castTargetType (not
		// x.TargetType) is used so the bare-`char`-synthesized-to-"bpchar"
		// rename above is truncated too, while the quoted OID-18 `"char"`
		// form (Typmod==0, already handled above) is unaffected. M0122-0005.
		if x.Typmod > 0 && result.Kind == KindString {
			switch castTargetType {
			case "varchar", "bpchar", "char", "character":
				n := int(x.Typmod)
				runes := []rune(result.StringValue())
				if len(runes) > n {
					result = NewStringDatum(string(runes[:n]))
				}
			}
		}
		// Apply numeric(P,S) typmod: round to S decimal places.
		// Typmod is encoded as (P<<16)|S by the planner's encodeTypmod.
		if x.Typmod > 0 {
			switch strings.ToLower(x.TargetType) {
			case "numeric", "decimal":
				scale := int16(x.Typmod & 0xFFFF)
				if scale >= 0 && scale <= 38 {
					result = roundNumericToScale(result, scale)
				}
			}
		}
		// Apply typmod precision for time/timetz casts (e.g., ::timetz(4)).
		// Upstream time_in/timetz_in round the fractional seconds half away from
		// zero via AdjustTimeForTypmod (date.c:1710), so `'23:59:59.999999'::time(2)`
		// is 24:00:00, not the truncated 23:59:59.99 the old ns-division here
		// produced. M0119-0006 (62nd slice).
		if x.Typmod > 0 {
			switch x.TargetType {
			case "time", "timetz", "time with time zone":
				result = roundTimeDatumToPrecision(result, x.Typmod)
			}
		}
		return result, nil
	case *optimizer.BinaryOp:
		// Row-to-row comparisons: element-wise with proper NULL propagation.
		// (a,b) OP (c,d): compare element by element; NULL in any element → NULL.
		// This implements SQL row-comparison semantics (ISO SQL §8.7). M0097-0023.
		if lRow, ok := x.Left.(*optimizer.RowExpr); ok {
			if rRow, ok := x.Right.(*optimizer.RowExpr); ok {
				return evalRowToRowComparison(x.Op, lRow, rRow, slot, ctx)
			}
		}
		// Special case: row-constructor comparison with multi-column scalar subquery.
		// ROW(a, b) = (SELECT x, y FROM ...) → element-wise comparison.
		// ROW(a,b) is planned as FuncCall{Name:"row",...} not RowExpr. M0097-0020.
		if x.Op == parser.OpEq || x.Op == parser.OpNe {
			if rowFc, ok := x.Left.(*optimizer.FuncCall); ok && strings.EqualFold(rowFc.Name, "row") {
				if sqOp, ok := x.Right.(*optimizer.SubqueryExpr); ok {
					return evalRowFuncCallVsSubqueryExpr(x.Op, rowFc.Args, sqOp, slot, ctx)
				}
			}
			if rowFc, ok := x.Right.(*optimizer.FuncCall); ok && strings.EqualFold(rowFc.Name, "row") {
				if sqOp, ok := x.Left.(*optimizer.SubqueryExpr); ok {
					return evalRowFuncCallVsSubqueryExpr(x.Op, rowFc.Args, sqOp, slot, ctx)
				}
			}
		}
		left, err := evalExprSlot(x.Left, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// Short-circuit: AND returns FALSE immediately when left is FALSE;
		// OR returns TRUE immediately when left is TRUE. Matches PostgreSQL.
		if x.Op == parser.OpAnd {
			if left.Kind == KindBool && !left.BoolValue() {
				return left, nil // FALSE AND _ = FALSE
			}
		} else if x.Op == parser.OpOr {
			if left.Kind == KindBool && left.BoolValue() {
				return left, nil // TRUE OR _ = TRUE
			}
		}
		right, err := evalExprSlot(x.Right, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// When the declared result type is float8/float4, perform the arithmetic in
		// float64 to match PostgreSQL's float8 semantics (approximate, not exact).
		// This prevents exact big.Int arithmetic from producing 200-digit numbers
		// when float64 would stay in scientific notation. M0097-0003.
		// The float-vs-integer decision is isFloatResultType (exprnode.go), not
		// an inline list. It used to be both, and the two lists had already
		// drifted by one spelling ("double"): the compiled twin routes a
		// ResultType this predicate accepts to ExprAdapter — i.e. back to HERE
		// — so if the two disagreed, the fallback would land in the branch it
		// was diverted to avoid. M0127-PS6.2 sibling audit.
		if isFloatResultType(x.ResultType) {
			var lf, rf float64
			if left.Kind == KindNumeric {
				lf, _ = strconv.ParseFloat(left.Format(), 64)
			} else if left.Kind == KindString {
				// String-formatted float (e.g. from random()). M0097-0042.
				lf, _ = strconv.ParseFloat(left.StringValue(), 64)
			} else {
				lf = float64(left.Int)
			}
			if right.Kind == KindNumeric {
				rf, _ = strconv.ParseFloat(right.Format(), 64)
			} else if right.Kind == KindString {
				// String-formatted float. M0097-0042.
				rf, _ = strconv.ParseFloat(right.StringValue(), 64)
			} else {
				rf = float64(right.Int)
			}
			var fResult float64
			switch x.Op {
			case parser.OpAdd:
				fResult = lf + rf
			case parser.OpSub:
				fResult = lf - rf
			case parser.OpMul:
				fResult = lf * rf
			case parser.OpDiv:
				if rf == 0 {
					// No Pos: pure runtime evaluation (float8_div,
					// postgres/src/backend/utils/adt/float.c) — PG never
					// attaches errposition to row-by-row arithmetic, only to
					// parse-time literal coercion. M0134-0070.
					return Datum{}, &ExecError{Code: "22012", Message: "division by zero"}
				}
				fResult = lf / rf
			default:
				// Fall through to normal evaluation for unsupported ops.
				goto normalBinaryOp
			}
			// Format the float64 result using PostgreSQL's float8out format (%.15g).
			// Return as a string datum — the dispatch layer's appendFloat8Text will
			// re-parse it as float64 for proper scientific notation display. M0097-0003.
			fs := strconv.FormatFloat(fResult, 'g', 15, 64)
			// For integer-valued results like -1 or 1, parseNumericFast gives the clean
			// representation (no trailing ".0"). For scientific notation or fractional
			// values, keep as string for float-format display.
			if m, s, ok := parseNumericFast(fs); ok {
				return Datum{Kind: KindNumeric, Int: m, Scale: s}, nil
			}
			// Scientific notation or fractional float: keep as string so dispatch
			// can format it with strconv.FormatFloat rather than big.Int decimal expansion.
			return NewStringDatum(fs), nil
		}
		// pg_lsn arithmetic/comparison: detect KindString "X/Y" pattern.
		if (left.Kind == KindString && looksLikePgLSN(left.StringValue())) ||
			(right.Kind == KindString && looksLikePgLSN(right.StringValue())) {
			res, handled, lsnErr := evalPgLSNBinary(x.Op, left, right, x.Pos())
			if lsnErr != nil {
				return Datum{}, lsnErr
			}
			if handled {
				return res, nil
			}
		}
	normalBinaryOp:
		result, err := evalBinary(x.Op, left, right, x.Pos(), ctx)
		if err != nil {
			return Datum{}, err
		}
		// Overflow checks for integer arithmetic (M0097-0003).
		//
		// The int2/int4 decision is overflowCodeForType (exprnode.go), shared
		// with the compiled twin, which precomputes it into ExprNode.payload[1]
		// at build time. It used to be this switch AND that function, and they
		// disagreed: this one compared exact strings, that one folds case, so
		// a ResultType of "INT4" raised 22003 on one evaluator and returned
		// 2147483648 on the other. The planner lowercases every type name it
		// emits (planner.go: `strings.ToLower(x.Type.Name)`), so the spelling
		// is not reachable from SQL today — but "unreachable" is a property of
		// the planner, not of these two evaluators, and it is not what keeps
		// them in agreement. One function is. M0127-PS6.2 sibling audit.
		//
		// int8/bigint stays unchecked on both twins (overflowCodeForType
		// returns ovfNone): Go wraps int64 silently and no wrap detection is
		// implemented. Ledgered.
		if result.Kind == KindInt {
			switch overflowCodeForType(x.ResultType) {
			case ovfInt2:
				if result.Int < -32768 || result.Int > 32767 {
					// No Pos: pure runtime evaluation (int2pl/int2mi/int2mul,
					// postgres/src/backend/utils/adt/int.c, has no
					// errposition call). M0134-0070.
					return Datum{}, &ExecError{Code: "22003", Message: "smallint out of range"}
				}
			case ovfInt4:
				if result.Int < -2147483648 || result.Int > 2147483647 {
					// No Pos: pure runtime evaluation (int4pl/int4mi/int4mul,
					// postgres/src/backend/utils/adt/int.c, has no
					// errposition call). M0134-0070.
					return Datum{}, &ExecError{Code: "22003", Message: "integer out of range"}
				}
			}
		}
		return result, nil
	case *optimizer.FuncCall:
		return evalFuncCall(x, slot, ctx)
	case *optimizer.IsNullExpr:
		// IS [NOT] NULL never propagates NULL — it always returns a boolean.
		// A row-valued operand follows SQL/PG row null semantics: `row IS NULL`
		// is true iff every field is null, `row IS NOT NULL` iff every field is
		// non-null. These are NOT inverses — a row mixing null and non-null
		// fields is false for both — so the RowExpr case cannot reuse the scalar
		// path below (a constructed RowExpr is never itself a NULL Datum, which
		// would make `whole_row IS NOT NULL` wrongly true for an outer-join NULL
		// row). M0110-0003 (pg_amcheck AC-002 gap #7a).
		if re, ok := x.Operand.(*optimizer.RowExpr); ok {
			res, err := evalRowNullTest(re, x.Negated, slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			return NewBoolDatum(res), nil
		}
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		isNull := operand.IsNull()
		if x.Negated {
			return NewBoolDatum(!isNull), nil // IS NOT NULL
		}
		return NewBoolDatum(isNull), nil // IS NULL
	case *optimizer.IsBoolExpr:
		// IS [NOT] TRUE/FALSE/UNKNOWN. Always returns boolean. M0097-0003.
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		var result bool
		if x.TestTrue {
			// IS TRUE: must be non-null and boolean true
			result = !operand.IsNull() && operand.Kind == KindBool && operand.Int != 0
		} else if x.TestFalse {
			// IS FALSE: must be non-null and boolean false
			result = !operand.IsNull() && operand.Kind == KindBool && operand.Int == 0
		} else {
			// IS UNKNOWN: must be null
			result = operand.IsNull()
		}
		if x.Negated {
			result = !result
		}
		return NewBoolDatum(result), nil
	case *optimizer.IsDistinctFromExpr:
		// IS [NOT] DISTINCT FROM — null-safe equality. Always returns boolean.
		//   a IS DISTINCT FROM b     = NOT (a = b OR (a IS NULL AND b IS NULL))
		//   a IS NOT DISTINCT FROM b = (a = b OR (a IS NULL AND b IS NULL))
		lv, err := evalExprSlot(x.Left, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		rv, err := evalExprSlot(x.Right, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalIsDistinctFrom(lv, rv, x.Negated)
	}
	return Datum{}, &ExecError{Code: "XX000", Pos: e.Pos(), Message: fmt.Sprintf("unsupported expression %T", e)}
}

// evalIsDistinctFrom implements a IS [NOT] DISTINCT FROM b.
//
//	IS DISTINCT FROM     = NOT (a = b OR (a IS NULL AND b IS NULL))
//	IS NOT DISTINCT FROM = (a = b OR (a IS NULL AND b IS NULL))
func evalIsDistinctFrom(lv, rv Datum, negated bool) (Datum, error) {
	var equal bool
	if lv.IsNull() && rv.IsNull() {
		equal = true
	} else if lv.IsNull() || rv.IsNull() {
		equal = false
	} else {
		cmp, err := compareDatum(lv, rv, 0)
		if err != nil {
			equal = false
		} else {
			equal = cmp == 0
		}
	}
	if negated {
		return NewBoolDatum(equal), nil // IS NOT DISTINCT FROM
	}
	return NewBoolDatum(!equal), nil // IS DISTINCT FROM
}

// evalUnary handles -, +, NOT.
func evalUnary(op parser.OpCode, d Datum, pos int) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case parser.OpUnaryNeg:
		switch d.Kind {
		case KindInt:
			if d.Int == math.MinInt64 {
				// No Pos: pure runtime evaluation (int8um,
				// postgres/src/backend/utils/adt/int8.c, has no
				// errposition call). M0134-0070.
				return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
			}
			return Datum{Kind: KindInt, Int: -d.Int}, nil
		case KindNumeric:
			// Negate a numeric/float value. M0097-0003.
			if d.Flags&flagBigNumeric != 0 {
				neg := new(big.Int).Neg(d.NumericBigValue())
				return newBigNumericInCtx(mmgr.Perm(), neg, d.Scale), nil
			}
			return Datum{Kind: KindNumeric, Int: -d.Int, Scale: d.Scale}, nil
		case KindInterval:
			// Unary interval negation (interval_um). (unimplemented_feat #5(d-iv))
			return negateInterval(d, pos)
		default:
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary - requires integer or numeric"}
		}
	case parser.OpUnaryPos:
		switch d.Kind {
		case KindInt, KindNumeric:
			return d, nil
		default:
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary + requires integer or numeric"}
		}
	case parser.OpNot:
		if d.Kind != KindBool {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator NOT requires boolean"}
		}
		return NewBoolDatum(!d.BoolValue()), nil
	case parser.OpBitNot:
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator ~ requires integer operand"}
		}
		return Datum{Kind: KindInt, Int: ^d.Int}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown unary operator %s", op)}
}

// looksLikePgLSN reports whether s is in "X/Y" format (1–8 uppercase hex digits each).
func looksLikePgLSN(s string) bool {
	slash := strings.IndexByte(s, '/')
	if slash < 1 || slash > 8 {
		return false
	}
	hexLow := s[slash+1:]
	if len(hexLow) < 1 || len(hexLow) > 8 {
		return false
	}
	for _, c := range s[:slash] + hexLow {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// pgLSNParseDelta extracts a numeric delta from a datum for pg_lsn arithmetic.
// Returns (absValue uint64, isNegative bool, isNaN bool, ok bool).
// isNegative=true means subtract absValue; false means add absValue.
// isNaN=true means caller must error (NaN operand).
// M0097-pg_lsn: use uint64 to avoid sign overflow for large pg_lsn differences.
func pgLSNParseDelta(d Datum) (uint64, bool, bool, bool) {
	parseStr := func(s string) (uint64, bool, bool, bool) {
		if s == "NaN" {
			return 0, false, true, true
		}
		if strings.HasPrefix(s, "-") {
			if v, err := strconv.ParseUint(s[1:], 10, 64); err == nil {
				return v, true, false, true
			}
			return 0, false, false, false
		}
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			return v, false, false, true
		}
		return 0, false, false, false
	}
	switch d.Kind {
	case KindInt:
		if d.Int < 0 {
			return uint64(-d.Int), true, false, true
		}
		return uint64(d.Int), false, false, true
	case KindNumeric:
		return parseStr(d.Format())
	case KindString:
		s := d.StringValue()
		if looksLikePgLSN(s) {
			return 0, false, false, false
		}
		return parseStr(s)
	}
	return 0, false, false, false
}

// evalPgLSNBinary handles pg_lsn comparison and arithmetic operators.
// Returns (result, true, nil) when handled, (zero, false, nil) to fall through.
func evalPgLSNBinary(op parser.OpCode, left, right Datum, pos int) (Datum, bool, error) {
	// Parse one or both sides as pg_lsn uint64.
	parseLSNDatum := func(d Datum) (uint64, bool) {
		if d.Kind == KindString {
			u, err := parsePgLSN(d.StringValue())
			if err == nil {
				return u, true
			}
		}
		return 0, false
	}

	switch op {
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		if !lok || !rok {
			return Datum{}, false, nil
		}
		var result bool
		switch op {
		case parser.OpEq:
			result = lu == ru
		case parser.OpNe:
			result = lu != ru
		case parser.OpLt:
			result = lu < ru
		case parser.OpLe:
			result = lu <= ru
		case parser.OpGt:
			result = lu > ru
		case parser.OpGe:
			result = lu >= ru
		}
		return NewBoolDatum(result), true, nil
	case parser.OpSub:
		// pg_lsn - pg_lsn → numeric (unsigned difference as decimal string)
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		if lok && rok {
			if lu >= ru {
				return NewStringDatum(strconv.FormatUint(lu-ru, 10)), true, nil
			}
			return NewStringDatum("-" + strconv.FormatUint(ru-lu, 10)), true, nil
		}
		// pg_lsn - numeric → pg_lsn
		if lok {
			abs, isNeg, isNaN, ok := pgLSNParseDelta(right)
			if ok {
				if isNaN {
					return Datum{}, true, &ExecError{Code: "0A000", Pos: pos,
						Message: "cannot subtract NaN from pg_lsn"}
				}
				if isNeg {
					// pg_lsn - (-N) = pg_lsn + N
					result := lu + abs
					if result < lu {
						// No Pos: pure runtime evaluation (pg_lsn_mi,
						// postgres/src/backend/utils/adt/pg_lsn.c, has no
						// errposition call). M0134-0070.
						return Datum{}, true, &ExecError{Code: "22003", Message: "pg_lsn out of range"}
					}
					return NewStringDatum(formatPgLSN(result)), true, nil
				}
				if abs > lu {
					return Datum{}, true, &ExecError{Code: "22003", Message: "pg_lsn out of range"}
				}
				return NewStringDatum(formatPgLSN(lu - abs)), true, nil
			}
		}
	case parser.OpAdd:
		// pg_lsn + numeric → pg_lsn
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		var lsnVal uint64
		var numericDatum Datum
		if lok && !rok {
			lsnVal = lu
			numericDatum = right
		} else if rok && !lok {
			lsnVal = ru
			numericDatum = left
		} else {
			return Datum{}, false, nil
		}
		abs, isNeg, isNaN, ok := pgLSNParseDelta(numericDatum)
		if ok {
			if isNaN {
				return Datum{}, true, &ExecError{Code: "0A000", Pos: pos,
					Message: "cannot add NaN to pg_lsn"}
			}
			if isNeg {
				// pg_lsn + (-N) = pg_lsn - N. No Pos: pure runtime evaluation
				// (pg_lsn_pli, postgres/src/backend/utils/adt/pg_lsn.c, has
				// no errposition call). M0134-0070.
				if abs > lsnVal {
					return Datum{}, true, &ExecError{Code: "22003", Message: "pg_lsn out of range"}
				}
				return NewStringDatum(formatPgLSN(lsnVal - abs)), true, nil
			}
			result := lsnVal + abs
			if result < lsnVal {
				return Datum{}, true, &ExecError{Code: "22003", Message: "pg_lsn out of range"}
			}
			return NewStringDatum(formatPgLSN(result)), true, nil
		}
	}
	return Datum{}, false, nil
}

// dpow implements PostgreSQL's `^` exponentiation operator (float8 ^ float8),
// ported verbatim from postgres/src/backend/utils/adt/float.c:dpow. The NaN
// and infinity handling is explicit because platform pow(3) gets several of
// these cases wrong; math.Pow is trusted only for the residual case at the
// bottom. M0134-0019b.
func dpow(a, b float64, pos int) (float64, *ExecError) {
	// POSIX: NaN ^ 0 = 1, 1 ^ NaN = 1; every other NaN input yields NaN.
	if math.IsNaN(a) {
		if math.IsNaN(b) || b != 0.0 {
			return math.NaN(), nil
		}
		return 1.0, nil
	}
	if math.IsNaN(b) {
		if a != 1.0 {
			return math.NaN(), nil
		}
		return 1.0, nil
	}

	// The SQL spec requires a specific SQLSTATE for these domain errors —
	// not the divide-by-zero code — for 0 ^ negative and negative ^
	// non-integer.
	if a == 0 && b < 0 {
		return 0, &ExecError{Code: "22023", Pos: pos, Message: "zero raised to a negative power is undefined"}
	}
	if a < 0 && math.Floor(b) != b {
		return 0, &ExecError{Code: "22023", Pos: pos, Message: "a negative number raised to a non-integer power yields a complex result"}
	}

	// Infinite exponent: handle before infinite base so it doesn't matter
	// whether the base is also infinite.
	if math.IsInf(b, 0) {
		absx := math.Abs(a)
		switch {
		case absx == 1.0:
			return 1.0, nil
		case b > 0.0: // y = +Inf
			if absx > 1.0 {
				return b, nil
			}
			return 0.0, nil
		default: // y = -Inf
			if absx > 1.0 {
				return 0.0, nil
			}
			return -b, nil
		}
	}
	if math.IsInf(a, 0) {
		switch {
		case b == 0.0:
			return 1.0, nil
		case a > 0.0: // x = +Inf
			if b > 0.0 {
				return a, nil
			}
			return 0.0, nil
		default: // x = -Inf
			// The domain check above already established b is an integer
			// (since a < 0). It's odd iff b/2 is not also an integer.
			halfy := b / 2
			yisoddinteger := math.Floor(halfy) != halfy
			if b > 0.0 {
				if yisoddinteger {
					return a, nil
				}
				return -a, nil
			}
			if yisoddinteger {
				return math.Copysign(0, -1), nil
			}
			return 0.0, nil
		}
	}

	return math.Pow(a, b), nil
}

// evalBinary handles arithmetic, comparison, and boolean operators.
// SQL three-valued logic: NULL operand on most operators yields NULL;
// AND/OR follow Kleene's rules.
func evalBinary(op parser.OpCode, left, right Datum, pos int, ctx *Context) (Datum, error) {
	if op.IsBoolean() {
		switch op {
		case parser.OpAnd:
			return evalAnd(left, right), nil
		case parser.OpOr:
			return evalOr(left, right), nil
		}
	}
	if left.IsNull() || right.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case parser.OpAdd, parser.OpSub:
		// timestamp/date ± interval and interval + timestamp/date
		// route through the time-arithmetic path before falling
		// back to integer arithmetic. v0 doesn't support
		// interval - timestamp (upstream rejects it too) or
		// timestamp - timestamp (returns interval upstream;
		// scope-deferred until the type system).
		if left.Kind == KindTime && right.Kind == KindInterval {
			return addTimeInterval(left, right, op == parser.OpSub, pos)
		}
		if op == parser.OpAdd && left.Kind == KindInterval && right.Kind == KindTime {
			return addTimeInterval(right, left, false, pos)
		}
		// timestamp − timestamp → interval; date − date → integer days.
		// Mirrors upstream timestamp_mi / date_mi (timestamp.c / date.c):
		// the microsecond difference is justified into whole 24h days
		// (interval_justify_hours), while a pure date pair yields an int4
		// day count instead of an interval.
		if op == parser.OpSub && left.Kind == KindTime && right.Kind == KindTime {
			return subTimeTime(left, right, pos)
		}
		// interval ± interval → interval (component-wise), matching
		// interval_pl / interval_mi.
		if left.Kind == KindInterval && right.Kind == KindInterval {
			return addIntervalInterval(left, right, op == parser.OpSub, pos)
		}
		// date ± integer → date (days arithmetic).
		// Mirrors upstream date_pli / date_mi: the integer operand is
		// treated as a day count.  Requires the left datum to carry
		// TimeSubDate (a bare timestamp+int would be ambiguous).
		if left.IsDate() && right.Kind == KindInt {
			return addDateTimeInt(left, right, op == parser.OpSub, pos)
		}
		if op == parser.OpAdd && left.Kind == KindInt && right.IsDate() {
			return addDateTimeInt(right, left, false, pos)
		}
		// NUMERIC ± NUMERIC, NUMERIC ± INT, INT ± NUMERIC: promote
		// the int side to KindNumeric{scale=0} and reuse the same
		// scale-aligning helpers.  Also try to parse string
		// operands as numeric (columns loaded via INSERT may be
		// stored as strings before the type system enforces types).
		if left.Kind == KindString {
			if m, s, err := parseNumeric(left.StringValue()); err == nil {
				left = newNumeric(m, int(s))
			}
		}
		if right.Kind == KindString {
			if m, s, err := parseNumeric(right.StringValue()); err == nil {
				right = newNumeric(m, int(s))
			}
		}
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			if op == parser.OpAdd {
				return numericAdd(a, b)
			}
			return numericSub(a, b)
		}
		fallthrough
	case parser.OpMul, parser.OpDiv, parser.OpMod:
		// String operands can be parsed as numeric (same as in OpAdd/OpSub above).
		// Handles cases like random()*0 where random() returns a string-formatted float. M0097-0042.
		if left.Kind == KindString {
			if m, s, err := parseNumeric(left.StringValue()); err == nil {
				left = newNumeric(m, int(s))
			}
		}
		if right.Kind == KindString {
			if m, s, err := parseNumeric(right.StringValue()); err == nil {
				right = newNumeric(m, int(s))
			}
		}
		// interval * numeric/int/float and interval / numeric/int/float
		// route through interval_mul / interval_div (timestamp.c) before
		// the generic numeric path below, mirroring the established
		// KindInterval && KindInterval arm for OpAdd/OpSub above. PG has
		// no interval-modulo operator, so OpMod on an interval falls
		// through to the int-only error below unchanged. M0134-0035.
		if left.Kind == KindInterval && (op == parser.OpMul || op == parser.OpDiv) {
			factor, ok := datumToFloat64(right)
			if !ok {
				return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator does not exist: interval %s %s", op, pgKindTypeName(right.Kind))}
			}
			if op == parser.OpMul {
				return intervalMul(left, factor, pos)
			}
			return intervalDiv(left, factor, pos)
		}
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			switch op {
			case parser.OpMul:
				return numericMul(a, b)
			case parser.OpDiv:
				return numericDiv(a, b, pos)
			}
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s not supported on numeric", op)}
		}
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		return arithmetic(op, left.Int, right.Int, pos)
	case parser.OpPow:
		a, aok := datumToFloat64(left)
		b, bok := datumToFloat64(right)
		if !aok || !bok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires numeric operands", op)}
		}
		result, err := dpow(a, b, pos)
		if err != nil {
			return Datum{}, err
		}
		return floatTextDatum(PGFloatOut(result, 64)), nil
	case parser.OpConcat:
		// || requires at least one string-typed operand. When one side is text
		// (or string-like), the other side is coerced to text. When both sides
		// are non-string (e.g. integer || numeric), PostgreSQL raises
		// "operator does not exist" — match that behaviour. M0097-0063.
		if left.IsNull() || right.IsNull() {
			return NullDatum, nil
		}
		leftIsStr := left.Kind == KindString || left.Kind == KindBytes
		rightIsStr := right.Kind == KindString || right.Kind == KindBytes
		if !leftIsStr && !rightIsStr {
			// Neither operand is string-like → PG-compatible error.
			return Datum{}, &ExecError{Code: "42883", Pos: pos,
				Message: fmt.Sprintf("operator does not exist: %s || %s",
					pgKindTypeName(left.Kind), pgKindTypeName(right.Kind)),
				Hint: "No operator matches the given name and argument types. You might need to add explicit type casts."}
		}
		// Array concatenation: if both operands look like PostgreSQL arrays
		// ({v1,v2,...}), merge their elements rather than text-concat.
		// Also handles array || element and element || array (append/prepend).
		// M0097-0065. Non-array text rendering honors the session DateStyle
		// GUC for DATE/TIMESTAMP/TIMESTAMPTZ operands (formatDatumDateStyle),
		// matching the already-fixed SELECT/COPY/CAST output paths.
		// byteacat (varlena.c): bytea || bytea is BYTEA, not text. The
		// unknown-type literal in `<bytea> || '\x00'` is coerced through
		// byteain first, exactly as PG's operator resolution would; a string
		// that is not valid bytea input falls through to the text path below
		// rather than failing the query. M0125-0021.
		if left.Kind == KindBytes || right.Kind == KindBytes {
			lb, lok := byteaOperand(left)
			rb, rok := byteaOperand(right)
			if lok && rok {
				out := make([]byte, 0, len(lb)+len(rb))
				out = append(out, lb...)
				out = append(out, rb...)
				return NewBytesDatum(out), nil
			}
		}
		ls := formatDatumDateStyle(left, ctx)
		rs := formatDatumDateStyle(right, ctx)
		lsIsArr := len(ls) >= 2 && ls[0] == '{' && ls[len(ls)-1] == '}'
		rsIsArr := len(rs) >= 2 && rs[0] == '{' && rs[len(rs)-1] == '}'
		if lsIsArr && rsIsArr {
			// array || array: merge inner elements.
			leftInner := ls[1 : len(ls)-1]
			rightInner := rs[1 : len(rs)-1]
			var inner string
			switch {
			case leftInner == "" && rightInner == "":
				inner = ""
			case leftInner == "":
				inner = rightInner
			case rightInner == "":
				inner = leftInner
			default:
				inner = leftInner + "," + rightInner
			}
			return NewStringDatum("{" + inner + "}"), nil
		}
		if lsIsArr && !rsIsArr {
			// array || element: append element to array.
			inner := ls[1 : len(ls)-1]
			if inner == "" {
				return NewStringDatum("{" + rs + "}"), nil
			}
			return NewStringDatum("{" + inner + "," + rs + "}"), nil
		}
		if rsIsArr && !lsIsArr {
			// element || array: prepend element.
			inner := rs[1 : len(rs)-1]
			if inner == "" {
				return NewStringDatum("{" + ls + "}"), nil
			}
			return NewStringDatum("{" + ls + "," + inner + "}"), nil
		}
		return NewStringDatum(ls + rs), nil
	case parser.OpBitAnd, parser.OpBitOr, parser.OpBitXor, parser.OpBitShiftLeft, parser.OpBitShiftRight:
		// Geometric point operators reuse the << / >> spellings: `point << point`
		// (strictly left of) and `point >> point` (strictly right of) compare the
		// X coordinates and yield bool. goopg backs `point` with its text form, so
		// detect the literal shape of both operands here. Used by predicate-gist.
		if op == parser.OpBitShiftLeft || op == parser.OpBitShiftRight {
			if left.Kind == KindString && right.Kind == KindString {
				if lp, lok := parsePointText(left.StringValue()); lok {
					if rp, rok := parsePointText(right.StringValue()); rok {
						if op == parser.OpBitShiftLeft {
							return NewBoolDatum(lp[0] < rp[0]), nil
						}
						return NewBoolDatum(lp[0] > rp[0]), nil
					}
				}
			}
		}
		// Bitwise operators: require integer operands. M0097-0003.
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		switch op {
		case parser.OpBitAnd:
			return Datum{Kind: KindInt, Int: left.Int & right.Int}, nil
		case parser.OpBitOr:
			return Datum{Kind: KindInt, Int: left.Int | right.Int}, nil
		case parser.OpBitXor:
			return Datum{Kind: KindInt, Int: left.Int ^ right.Int}, nil
		case parser.OpBitShiftLeft:
			return Datum{Kind: KindInt, Int: left.Int << uint(right.Int)}, nil
		case parser.OpBitShiftRight:
			return Datum{Kind: KindInt, Int: left.Int >> uint(right.Int)}, nil
		}
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "unknown bitwise operator"}
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
		cmp, err := compareDatum(left, right, pos)
		if err != nil {
			return Datum{}, err
		}
		return NewBoolDatum(cmpResult(op, cmp)), nil
	case parser.OpLike, parser.OpNotLike, parser.OpILike, parser.OpNotILike:
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands (got left.Kind=%d right.Kind=%d)", op, left.Kind, right.Kind)}
		}
		var matched bool
		if op == parser.OpILike || op == parser.OpNotILike {
			matched = matchSQLLike(strings.ToLower(ls), strings.ToLower(rs))
		} else {
			matched = matchSQLLike(ls, rs)
		}
		if op == parser.OpNotLike || op == parser.OpNotILike {
			matched = !matched
		}
		return NewBoolDatum(matched), nil
	case parser.OpRegexMatch, parser.OpRegexIMatch, parser.OpRegexNoMatch, parser.OpRegexINoMatch:
		// POSIX regex operators. M0097-0011.
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands", op)}
		}
		matched, err := evalPOSIXRegex(ls, rs, op == parser.OpRegexIMatch || op == parser.OpRegexINoMatch)
		if err != nil {
			// No Pos: pure runtime evaluation (RE_compile_and_cache,
			// postgres/src/backend/utils/adt/regexp.c, has no errposition
			// call). M0134-0070.
			return Datum{}, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", err)}
		}
		if op == parser.OpRegexNoMatch || op == parser.OpRegexINoMatch {
			matched = !matched
		}
		return NewBoolDatum(matched), nil
	case parser.OpContainedBy, parser.OpContains, parser.OpOverlap:
		// Geometric box operators: <@ (contained by), @> (contains), && (overlap). M0097-0023.
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires box operands", op)}
		}
		// anyarray containment/overlap: when both operands are array literals
		// ({...}) the operator carries set-membership semantics, not box
		// geometry (predicate-gin, design 0118-0139). PG dispatches @>/<@/&&
		// to arraycontains/arraycontained/arrayoverlap by operand type.
		if isArrayLiteralText(ls) && isArrayLiteralText(rs) {
			return NewBoolDatum(evalArraySetOp(op, ls, rs)), nil
		}
		aur, all, aok := parseBoxText(ls)
		if !aok {
			// point <@ box (point_box.c: on_pb / box_contain_pt dispatch by
			// operand type; goopg has no separate point/box Datum kinds, so
			// fall back from box-vs-box to a degenerate box{pt,pt} when the
			// left operand parses as a bare point instead. M0134-0111.
			if pt, pok := parsePointText(ls); pok {
				aur, all, aok = pt, pt, true
			}
		}
		bur, bll, bok := parseBoxText(rs)
		if !bok {
			if pt, pok := parsePointText(rs); pok {
				bur, bll, bok = pt, pt, true
			}
		}
		if !aok || !bok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s: invalid box value", op)}
		}
		var result bool
		switch op {
		case parser.OpContainedBy:
			// a <@ b: b contains a; b.ll <= a.ll AND a.ur <= b.ur (both axes)
			result = bll[0] <= all[0] && aur[0] <= bur[0] && bll[1] <= all[1] && aur[1] <= bur[1]
		case parser.OpContains:
			// a @> b: a contains b
			result = all[0] <= bll[0] && bur[0] <= aur[0] && all[1] <= bll[1] && bur[1] <= aur[1]
		case parser.OpOverlap:
			// a && b: boxes overlap (share area or touch on closed boundary)
			result = !(aur[0] < bll[0] || bur[0] < all[0] || aur[1] < bll[1] || bur[1] < all[1])
		}
		return NewBoolDatum(result), nil
	case parser.OpJSONGet, parser.OpJSONGetText:
		// json/jsonb -> int|text  →  element/field (json), and ->> → text.
		// goopg carries json/jsonb as KindString; NULL operands already
		// returned NullDatum above. M0118-0009 (horizons enabler).
		return evalJSONArrow(op, left, right, pos)
	case parser.OpJSONPathGet, parser.OpJSONPathGetText:
		// json/jsonb #> text[]  →  value at path (json), and #>> → text.
		// M0134-0039.
		return evalJSONPathGet(op, left, right, pos)
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown operator %s", op)}
}

// evalJSONArrow evaluates the JSON accessor operators -> and ->>.
//
//	json -> int   → array element at index (negative counts from the end), as json
//	json -> text  → object field by key, as json
//	json ->> int  → array element as text
//	json ->> text → object field as text
//
// A non-array left operand with an int key, a non-object left operand with a
// text key, or a missing index/key yields SQL NULL — matching PostgreSQL. The
// left operand must be syntactically valid JSON (else 22P02). Numbers are
// decoded via json.Number so integer/exponent formatting round-trips exactly
// (e.g. the EXPLAIN-FORMAT-json "Heap Fetches" the horizons spec inspects).
//
// goopg has no distinct json vs jsonb storage, so -> re-encodes the navigated
// element as canonical JSON (jsonb-style: whitespace-collapsed, keys sorted by
// the encoder). The final scalar surface form is identical to PostgreSQL; only
// object/array key-order fidelity of the `json` (text) type differs — noted in
// design 0118-0100.
func evalJSONArrow(op parser.OpCode, left, right Datum, pos int) (Datum, error) {
	ls, ok := datumAsString(left)
	if !ok {
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("operator %s requires a json left operand", op)}
	}
	dec := json.NewDecoder(strings.NewReader(ls))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return Datum{}, &ExecError{Code: "22P02", Pos: pos,
			Message: "invalid input syntax for type json"}
	}

	var elem any
	var found bool
	switch right.Kind {
	case KindInt:
		arr, isArr := doc.([]any)
		if !isArr {
			return NullDatum, nil
		}
		idx := int(right.Int)
		if idx < 0 {
			idx = len(arr) + idx
		}
		if idx < 0 || idx >= len(arr) {
			return NullDatum, nil
		}
		elem, found = arr[idx], true
	case KindString, KindBytes:
		obj, isObj := doc.(map[string]any)
		if !isObj {
			return NullDatum, nil
		}
		key, _ := datumAsString(right)
		elem, found = obj[key]
	default:
		// Any other key type: PG has no matching operator; treat as NULL.
		return NullDatum, nil
	}
	if !found {
		return NullDatum, nil
	}

	if op == parser.OpJSONGetText {
		// ->> : a JSON null element is SQL NULL; scalars become their bare
		// text; objects/arrays become their compact JSON text. Shared with
		// json_extract_path_text/jsonb_extract_path_text (M0134-0037).
		return jsonElemAsTextDatum(elem), nil
	}
	// -> : return the element re-encoded as JSON (a JSON null → the text
	// "null"). Shared with json_extract_path/jsonb_extract_path.
	return jsonElemAsJSONDatum(elem), nil
}

// evalJSONPathGet evaluates the JSON path-extraction operators #> and #>>.
//
//	json #> text[]   → value at path (json)
//	json #>> text[]  → value at path (text)
//
// Operator aliases for jsonb_extract_path(jsonb, text[]) /
// jsonb_extract_path_text(jsonb, text[]) per
// postgres/src/include/catalog/pg_operator.dat; PG oracle:
// postgres/src/backend/utils/adt/jsonfuncs.c get_path_all. The left operand
// is decoded exactly like evalJSONArrow (22P02 on invalid JSON); the right
// operand is a text[] path, walked left-to-right via the existing
// jsonPathStep helper (shared with json[b]_extract_path[_text], M0134-0037).
// A path element that doesn't resolve yields SQL NULL immediately. An empty
// path array is the identity (returns the original document unchanged).
func evalJSONPathGet(op parser.OpCode, left, right Datum, pos int) (Datum, error) {
	ls, ok := datumAsString(left)
	if !ok {
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("operator %s requires a json left operand", op)}
	}
	dec := json.NewDecoder(strings.NewReader(ls))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return Datum{}, &ExecError{Code: "22P02", Pos: pos,
			Message: "invalid input syntax for type json"}
	}

	switch right.Kind {
	case KindString, KindBytes:
	default:
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("operator %s requires a text[] path operand", op)}
	}
	rs, ok := datumAsString(right)
	if !ok {
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("operator %s requires a text[] path operand", op)}
	}
	path := parseTextArray(rs)

	var elem any = doc
	for _, key := range path {
		v, found := jsonPathStep(elem, key)
		if !found {
			return NullDatum, nil
		}
		elem = v
	}

	if op == parser.OpJSONPathGetText {
		return jsonElemAsTextDatum(elem), nil
	}
	return jsonElemAsJSONDatum(elem), nil
}

// jsonPathStep performs one step of a json{b}_extract_path[_text] path walk:
// unlike -> / ->> (whose array-vs-object dispatch is fixed by the operator's
// static right-operand type, evalJSONArrow above), get_path_all's per-element
// dispatch is driven by the actual JSON structure encountered at each level
// (postgres/src/backend/utils/adt/jsonfuncs.c get_array_start/get_object_field_start,
// via a single text[] path threaded as both tpath/ipath). A numeric-string key
// against an array indexes it (negative counts from the end); any key against
// an object looks up that field; anything else — including a non-numeric key
// against an array — is "not found" (SQL NULL), matching PG's INT_MIN sentinel
// for a non-numeric path element that will never match an index.
func jsonPathStep(doc any, key string) (any, bool) {
	switch v := doc.(type) {
	case []any:
		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, false
		}
		if idx < 0 {
			idx += len(v)
		}
		if idx < 0 || idx >= len(v) {
			return nil, false
		}
		return v[idx], true
	case map[string]any:
		elem, found := v[key]
		return elem, found
	default:
		return nil, false
	}
}

// jsonElemAsJSONDatum re-encodes a decoded JSON element (any/json.Number/
// map[string]any/[]any/bool/nil) as its compact JSON text form — the shared
// tail of -> and json_extract_path/jsonb_extract_path.
func jsonElemAsJSONDatum(elem any) Datum {
	b, err := json.Marshal(elem)
	if err != nil {
		return NullDatum
	}
	return NewStringDatum(string(b))
}

// jsonElemAsTextDatum unwraps a decoded JSON element to its ->> text form: a
// JSON null is SQL NULL, scalars become their bare text, and objects/arrays
// become their compact JSON text — the shared tail of ->> and
// json_extract_path_text/jsonb_extract_path_text.
func jsonElemAsTextDatum(elem any) Datum {
	if elem == nil {
		return NullDatum
	}
	switch x := elem.(type) {
	case string:
		return NewStringDatum(x)
	case json.Number:
		return NewStringDatum(x.String())
	case bool:
		if x {
			return NewStringDatum("true")
		}
		return NewStringDatum("false")
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return NullDatum
		}
		return NewStringDatum(string(b))
	}
}

// datumAsString returns d's character payload as a Go string when
// the value is text-like (KindString or KindBytes). Used by LIKE so
// a varchar value that arrives as bytes still evaluates correctly,
// mirroring `compareDatum`'s cross-Kind tolerance for character
// data.
func datumAsString(d Datum) (string, bool) {
	switch d.Kind {
	case KindString:
		return d.StringValue(), true
	case KindBytes:
		return string(d.BytesValue()), true
	}
	return "", false
}

// datumAsFloat64 extracts a numeric value from a Datum for geometric operations.
func datumAsFloat64(d Datum) (float64, bool) {
	switch d.Kind {
	case KindInt:
		return float64(d.Int), true
	case KindNumeric:
		f, err := strconv.ParseFloat(d.Format(), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case KindString:
		f, err := strconv.ParseFloat(d.StringValue(), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// parsePointText parses a PostgreSQL point literal "(x,y)" or "x,y" into [x,y].
func parsePointText(s string) ([2]float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return [2]float64{}, false
	}
	x, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	y, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return [2]float64{}, false
	}
	return [2]float64{x, y}, true
}

// parseBoxText parses a PostgreSQL box literal "(x1,y1),(x2,y2)" into upper-right, lower-left.
func parseBoxText(s string) (ur, ll [2]float64, ok bool) {
	s = strings.TrimSpace(s)
	// Find the comma between the two coordinate pairs.
	depth := 0
	split := -1
	for i, c := range s {
		if c == '(' {
			depth++
		} else if c == ')' {
			depth--
		} else if c == ',' && depth == 0 {
			split = i
			break
		}
	}
	if split < 0 {
		return
	}
	p1, ok1 := parsePointText(s[:split])
	p2, ok2 := parsePointText(s[split+1:])
	if !ok1 || !ok2 {
		return
	}
	return p1, p2, true
}

// parseBoxLiteral reproduces box_in's parsing (via path_decode/pair_decode,
// postgres/src/backend/utils/adt/geo_ops.c) closely enough to give the same
// accept/reject verdict and the same corner values on every case exercised by
// box.sql: it accepts two coordinate pairs with an optional outer wrapper
// paren (single, e.g. "(x1,y1,x2,y2)", or doubled, e.g.
// "((x1,y1),(x2,y2))") and per-point optional parens, requires the ENTIRE
// string to be consumed (box_in passes a nil endptr, so trailing garbage like
// "(1,2,3,4) x" is rejected), and reorders the two points into (high, low) —
// PG swaps per-axis so high >= low on each of x and y independently, which is
// NOT simply "swap the two points" when the box is degenerate/crossed.
// M0134-0094: this is the single chokepoint a box(n) column's assignment
// coercion, a `box '...'` typed literal, and pg_input_is_valid('...','box')
// all share; distinct from parseBoxText above, which parses an
// already-canonical "(x,y),(x,y)" string for the exclusion-constraint path
// and does not validate or reorder.
func parseBoxLiteral(s string) (hx, hy, lx, ly float64, ok bool) {
	skipWS := func(t string) string {
		return strings.TrimLeft(t, " \t\n\r\v\f")
	}
	s = skipWS(s)
	if s == "" {
		return
	}
	// '<' is the open-path delimiter; box never accepts it (opentype=false).
	if s[0] == '<' {
		return
	}
	depth := 0
	if s[0] == '(' {
		cp := skipWS(s[1:])
		if cp != "" && cp[0] == '(' {
			depth++
			s = cp
		} else if strings.Count(s, "(") == 1 {
			depth++
			s = cp
		}
	}

	var pts [2][2]float64
	for i := 0; i < 2; i++ {
		x, y, rest, okPair := pairDecode(s)
		if !okPair {
			return
		}
		s = rest
		if s != "" && s[0] == ',' {
			s = s[1:]
		}
		pts[i] = [2]float64{x, y}
	}

	for depth > 0 {
		if s != "" && s[0] == ')' {
			depth--
			s = skipWS(s[1:])
		} else {
			return
		}
	}
	if s != "" {
		return
	}

	hx, lx = pts[0][0], pts[1][0]
	if lx > hx {
		hx, lx = lx, hx
	}
	hy, ly = pts[0][1], pts[1][1]
	if ly > hy {
		hy, ly = ly, hy
	}
	return hx, hy, lx, ly, true
}

// pairDecode parses one "(x,y)" or "x,y" coordinate pair (pair_decode in
// geo_ops.c), returning the unconsumed remainder of s.
func pairDecode(s string) (x, y float64, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	hasDelim := s != "" && s[0] == '('
	if hasDelim {
		s = s[1:]
	}
	x, s, ok = singleDecode(s)
	if !ok {
		return 0, 0, "", false
	}
	if s == "" || s[0] != ',' {
		return 0, 0, "", false
	}
	s = s[1:]
	y, s, ok = singleDecode(s)
	if !ok {
		return 0, 0, "", false
	}
	if hasDelim {
		if s == "" || s[0] != ')' {
			return 0, 0, "", false
		}
		s = strings.TrimLeft(s[1:], " \t\n\r\v\f")
	}
	return x, y, s, true
}

// singleDecode parses the longest valid float8 prefix of s (single_decode in
// geo_ops.c, which itself calls float8in_internal — strtod plus PG's
// NaN/Infinity/Inf fallback spellings, float.c:395-511), skips trailing
// whitespace after the number (float8in_internal does the same before
// returning its endptr — geo_ops.sql's whitespace-padded circle literals
// like " 1 , 3 , 5 " rely on this), and returns the unconsumed remainder.
func singleDecode(s string) (float64, string, bool) {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	// C99 NaN/Infinity/Inf spellings, case-insensitive, with optional sign
	// on the Infinity/Inf forms (float8in_internal's strtod-failure
	// fallback path).
	for _, kw := range []struct {
		text string
		val  float64
	}{
		{"NaN", math.NaN()},
		{"+Infinity", math.Inf(1)},
		{"-Infinity", math.Inf(-1)},
		{"Infinity", math.Inf(1)},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
		{"Inf", math.Inf(1)},
	} {
		if len(s) >= len(kw.text) && strings.EqualFold(s[:len(kw.text)], kw.text) {
			rest := strings.TrimLeft(s[len(kw.text):], " \t\n\r\v\f")
			return kw.val, rest, true
		}
	}
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digitsBefore := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	digitsBefore = i - digitsBefore
	digitsAfter := 0
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		digitsAfter = i - start
	}
	if digitsBefore == 0 && digitsAfter == 0 {
		return 0, "", false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j {
			i = k
		}
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, "", false
	}
	rest := strings.TrimLeft(s[i:], " \t\n\r\v\f")
	return v, rest, true
}

// boxCanonicalText formats a box's high/low corners exactly as box_out does
// (path_encode(PATH_NONE, 2, &high) — "(hx,hy),(lx,ly)", each coordinate via
// float8out's Ryu-based shortest-roundtrip formatting). M0134-0094.
func boxCanonicalText(hx, hy, lx, ly float64) string {
	return fmt.Sprintf("(%s,%s),(%s,%s)",
		PGFloatOut(hx, 64), PGFloatOut(hy, 64), PGFloatOut(lx, 64), PGFloatOut(ly, 64))
}

// parseCircleLiteral reproduces circle_in's parsing (geo_ops.c) closely
// enough to give the same accept/reject verdict and the same center/radius
// values on every case exercised by circle.sql: the canonical
// "<(x,y),r>" form, an un-bracketed "(x,y),r" / "((x,y),r)" form, and the
// "quick entry" flat "x,y,r" form (no wrapping delimiters at all) are all
// accepted; a negative radius (but NOT NaN — PG accepts NaN, since `NaN < 0`
// is false under IEEE comparison) is rejected, and the ENTIRE string must be
// consumed (circle_in passes a nil endptr, so trailing garbage like
// "<(100,200),10> x" is rejected) just like parseBoxLiteral above, whose
// pairDecode/singleDecode helpers this reuses. M0134-0098.
func parseCircleLiteral(s string) (x, y, radius float64, ok bool) {
	skipWS := func(t string) string {
		return strings.TrimLeft(t, " \t\n\r\v\f")
	}
	s = skipWS(s)
	if s == "" {
		return
	}
	depth := 0
	if s[0] == '<' {
		depth++
		s = s[1:]
	} else if s[0] == '(' {
		// If there are two left parens, consume the first one.
		cp := skipWS(s[1:])
		if cp != "" && cp[0] == '(' {
			depth++
			s = cp
		}
	}

	// pair_decode will consume parens around the pair, if any.
	var okPair bool
	x, y, s, okPair = pairDecode(s)
	if !okPair {
		return 0, 0, 0, false
	}
	if s != "" && s[0] == ',' {
		s = s[1:]
	}

	var okRadius bool
	radius, s, okRadius = singleDecode(s)
	if !okRadius {
		return 0, 0, 0, false
	}
	// We have to accept NaN — `radius < 0.0` is false when radius is NaN.
	if radius < 0.0 {
		return 0, 0, 0, false
	}

	for depth > 0 {
		if s != "" && (s[0] == ')' || (s[0] == '>' && depth == 1)) {
			depth--
			s = skipWS(s[1:])
		} else {
			return 0, 0, 0, false
		}
	}
	if s != "" {
		return 0, 0, 0, false
	}
	return x, y, radius, true
}

// circleCanonicalText formats a circle's center/radius exactly as
// circle_out does — "<(x,y),r>" — each field via pair_encode/single_encode
// (float8out's Ryu-based shortest-roundtrip formatting). M0134-0098.
func circleCanonicalText(x, y, radius float64) string {
	return fmt.Sprintf("<(%s,%s),%s>", PGFloatOut(x, 64), PGFloatOut(y, 64), PGFloatOut(radius, 64))
}

// evalLikeEscapePattern evaluates a LikeEscapePattern's Pattern and Escape
// operands and rewrites Pattern into PostgreSQL's standard backslash-escape
// convention, so the caller (evalBinary's LIKE/ILIKE arm, via
// datumAsString+matchSQLLike) never has to know about ESCAPE at all — a
// LikeEscapePattern only ever appears as a BinaryOp's Right operand, so this
// is the single place the rewrite happens. M0134-0070.
func evalLikeEscapePattern(x *optimizer.LikeEscapePattern, slot SlotView, ctx *Context) (Datum, error) {
	pat, err := evalExprSlot(x.Pattern, slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if pat.IsNull() {
		return NullDatum, nil
	}
	esc, err := evalExprSlot(x.Escape, slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if esc.IsNull() {
		return NullDatum, nil
	}
	patStr, patOK := datumAsString(pat)
	escStr, escOK := datumAsString(esc)
	if !patOK || !escOK {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "ESCAPE requires string operands"}
	}
	rewritten, err := likeEscapeRewrite(patStr, escStr, pat.Kind != KindBytes, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	if pat.Kind == KindBytes {
		return NewBytesDatum([]byte(rewritten)), nil
	}
	return NewStringDatum(rewritten), nil
}

// likeEscapeRewrite implements PostgreSQL's do_like_escape (PG oracle:
// postgres/src/backend/utils/adt/like_match.c:392-486): rewrite pattern to
// use the standard backslash-escape convention given an explicit ESCAPE
// string.
//
//   - escape length 0: double every literal '\' in pattern so it stays
//     literal against the always-backslash matcher.
//   - escape length 1 and the char IS '\': pattern unchanged.
//   - escape length 1 and char is anything else: substitute each occurrence
//     of the escape char with '\', and double each literal '\' not
//     immediately preceded by a substituted escape.
//   - escape length > 1: ERROR 22025 invalid_escape_sequence.
//
// runeMode selects character-based (text) vs byte-based (bytea) counting
// and iteration, matching PG's MB-aware text path vs single-byte bytea
// path. M0134-0070.
func likeEscapeRewrite(pattern, escape string, runeMode bool, pos int) (string, error) {
	invalidEscapeErr := func() error {
		return &ExecError{Code: "22025", Pos: pos, Message: "invalid escape string",
			Hint: "Escape string must be empty or one character."}
	}
	if runeMode {
		switch utf8.RuneCountInString(escape) {
		case 0:
			var b strings.Builder
			for _, r := range pattern {
				if r == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(r)
			}
			return b.String(), nil
		case 1:
			escRune, _ := utf8.DecodeRuneInString(escape)
			if escRune == '\\' {
				return pattern, nil
			}
			var b strings.Builder
			afterEscape := false
			for _, r := range pattern {
				switch {
				case r == escRune && !afterEscape:
					b.WriteByte('\\')
					afterEscape = true
				case r == '\\':
					b.WriteByte('\\')
					if !afterEscape {
						b.WriteByte('\\')
					}
					afterEscape = false
				default:
					b.WriteRune(r)
					afterEscape = false
				}
			}
			return b.String(), nil
		default:
			return "", invalidEscapeErr()
		}
	}
	// Byte mode (bytea): PG's bytea_like_escape counts and iterates raw bytes.
	switch len(escape) {
	case 0:
		var b strings.Builder
		for i := 0; i < len(pattern); i++ {
			if pattern[i] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(pattern[i])
		}
		return b.String(), nil
	case 1:
		escByte := escape[0]
		if escByte == '\\' {
			return pattern, nil
		}
		var b strings.Builder
		afterEscape := false
		for i := 0; i < len(pattern); i++ {
			c := pattern[i]
			switch {
			case c == escByte && !afterEscape:
				b.WriteByte('\\')
				afterEscape = true
			case c == '\\':
				b.WriteByte('\\')
				if !afterEscape {
					b.WriteByte('\\')
				}
				afterEscape = false
			default:
				b.WriteByte(c)
				afterEscape = false
			}
		}
		return b.String(), nil
	default:
		return "", invalidEscapeErr()
	}
}

// matchSQLLike implements SQL LIKE pattern semantics: '%' matches
// any (possibly empty) sequence, '_' matches exactly one character,
// every other byte matches itself. An ESCAPE clause is handled upstream by
// evalLikeEscapePattern/likeEscapeRewrite, which rewrite the pattern into
// this function's standard-backslash convention before it ever runs — this
// function itself always uses the default backslash escape. M0134-0070.
// The implementation is the standard recursive-descent matcher (no regex
// translation, so embedded special chars in the input never interact with
// regex syntax).
func matchSQLLike(s, pat string) bool {
	si, pi := 0, 0
	starS, starP := -1, -1
	for si < len(s) {
		if pi < len(pat) {
			c := pat[pi]
			switch c {
			case '\\':
				// Escape: next pattern byte matches literally.
				if pi+1 < len(pat) && pat[pi+1] == s[si] {
					pi += 2
					si++
					continue
				}
			case '%':
				starP = pi
				starS = si
				pi++
				continue
			case '_':
				pi++
				si++
				continue
			default:
				if c == s[si] {
					pi++
					si++
					continue
				}
			}
		}
		if starP >= 0 {
			pi = starP + 1
			starS++
			si = starS
			continue
		}
		return false
	}
	for pi < len(pat) && pat[pi] == '%' {
		pi++
	}
	return pi == len(pat)
}

// pgPatternToGoRE2 translates PostgreSQL-specific regex escapes that are not
// supported by Go's RE2 engine into their RE2 equivalents.
// Currently handles: \m (word-start) and \M (word-end) → \b. M0097-0073.
func pgPatternToGoRE2(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			switch pattern[i+1] {
			case 'm', 'M':
				b.WriteString(`\b`)
				i++
				continue
			}
		}
		b.WriteByte(pattern[i])
	}
	return b.String()
}

// evalPOSIXRegex evaluates a POSIX extended regex match.
// caseInsensitive applies the (?i) flag. M0097-0011.
func evalPOSIXRegex(s, pattern string, caseInsensitive bool) (bool, error) {
	pattern = pgPatternToGoRE2(pattern)
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(s), nil
}

// addDateTimeInt adds or subtracts an integer number of days to a DATE
// datum.  It mirrors upstream date_pli / date_mi: the integer is interpreted
// as a day count, and the result is a DATE.  ∞-day spans are not supported
// (PG rejects infinity date arithmetic).
func addDateTimeInt(dt, days Datum, subtract bool, pos int) (Datum, error) {
	if dt.IsTimestampNotFinite() {
		return NullDatum, timestampOutOfRange(pos)
	}
	t := time.Unix(0, dt.Int).UTC()
	n := int(days.Int)
	if subtract {
		n = -n
	}
	return NewDateDatum(t.AddDate(0, 0, n)), nil
}

// addTimeInterval applies an interval to a time value. When
// `subtract` is true the interval is negated first. Months are
// applied via time.AddDate (which carries year/month overflow
// the way upstream PG does for `timestamp + interval '1 month'`);
// days are added via the same call.
// addTimeInterval implements `timestamp + interval` (and, with subtract, the
// `timestamp - interval` form that upstream routes through timestamp_mi_interval
// → interval_um_internal → timestamp_pl_interval). It line-ports
// timestamp_pl_interval's ±infinity handling (postgres/src/backend/utils/adt/
// timestamp.c:3107): a ±infinity interval forces the result to the same-signed
// infinite timestamp, EXCEPT "infinity − infinity", which has no NaN analogue
// for the timestamp type and errors; a finite interval added to an already
// infinite timestamp passes the timestamp through unchanged.
// (unimplemented_feat #5(d-iv))
func addTimeInterval(t, iv Datum, subtract bool, pos int) (Datum, error) {
	// timestamp_mi_interval negates the span first (interval_um_internal swaps
	// the ±infinity sentinels), so fold subtract into the sentinel test.
	spanNoBegin := iv.IsIntervalNoBegin() // interval == -infinity
	spanNoEnd := iv.IsIntervalNoEnd()     // interval == +infinity
	if subtract {
		spanNoBegin, spanNoEnd = spanNoEnd, spanNoBegin
	}
	if spanNoBegin { // span is -infinity
		if t.IsTimestampPosInf() {
			return NullDatum, timestampOutOfRange(pos)
		}
		return NewTimestampInfinity(false), nil
	}
	if spanNoEnd { // span is +infinity
		if t.IsTimestampNegInf() {
			return NullDatum, timestampOutOfRange(pos)
		}
		return NewTimestampInfinity(true), nil
	}
	// Span is finite: an already-infinite timestamp passes through unchanged
	// (TIMESTAMP_NOT_FINITE(timestamp) branch).
	if t.IsTimestampNotFinite() {
		return t, nil
	}
	months := int(iv.IntervalMonthsValue())
	days := int(iv.IntervalDaysValue())
	micros := iv.IntervalMicrosValue()
	if subtract {
		months = -months
		days = -days
		micros = -micros
	}
	res := t.TimeValue().AddDate(0, months, days)
	if micros != 0 {
		res = res.Add(time.Duration(micros) * time.Microsecond)
	}
	return NewTimeDatum(res), nil
}

// timestampOutOfRange is PG's error for a non-representable timestamp result
// (here: "infinity − infinity", which the timestamp type cannot express since
// it has no NaN). Mirrors ereport(ERRCODE_DATETIME_VALUE_OUT_OF_RANGE,
// "timestamp out of range"). No Pos: every call site is pure runtime
// timestamp/interval arithmetic (postgres/src/backend/utils/adt/timestamp.c
// has no errposition call anywhere in this file). M0134-0070.
func timestampOutOfRange(pos int) error {
	return &ExecError{Code: "22008", Message: "timestamp out of range"}
}

// usecsPerDay is the microsecond count in a 24-hour day, used by the
// interval time-component helpers below (matches USECS_PER_DAY upstream).
const usecsPerDay = 24 * 60 * 60 * 1_000_000

// Sub-day interval unit magnitudes in microseconds, used when lowering
// sub-day interval literals (`interval '2 hours'` etc.) to the KindInterval
// micros carrier. Mirror USECS_PER_HOUR/MINUTE/SEC upstream
// (postgres/src/include/datatype/timestamp.h).
const (
	usecsPerHour   = 3600 * 1_000_000
	usecsPerMinute = 60 * 1_000_000
	usecsPerSecond = 1_000_000
	usecsPerMilli  = 1_000
)

// subTimeTime implements `timestamp − timestamp` → interval, mirroring
// upstream timestamp_mi: the microsecond delta is justified into whole 24h
// days via interval_justify_hours. goopg represents DATE internally as a
// timestamp, so date − date also flows through here and yields an interval
// (e.g. "9 days") rather than upstream date_mi's integer day count — a
// documented divergence deferred to the type system (deferral_ledger.md).
//
// ±infinity operands follow timestamp_mi's infinity block exactly: any
// "infinity − same-signed infinity" is an error (the interval type has no
// NaN), while a single infinite operand yields the correspondingly-signed
// infinite interval. -inf−x = -inf, +inf−x = +inf, x−(-inf) = +inf,
// x−(+inf) = -inf. (unimplemented_feat #5(d-iv))
func subTimeTime(left, right Datum, pos int) (Datum, error) {
	if left.IsTimestampNotFinite() || right.IsTimestampNotFinite() {
		switch {
		case left.IsTimestampNegInf():
			if right.IsTimestampNegInf() {
				return Datum{}, intervalOutOfRange(pos)
			}
			return NewIntervalInfinity(false), nil
		case left.IsTimestampPosInf():
			if right.IsTimestampPosInf() {
				return Datum{}, intervalOutOfRange(pos)
			}
			return NewIntervalInfinity(true), nil
		case right.IsTimestampNegInf(): // left finite − (−inf) = +inf
			return NewIntervalInfinity(true), nil
		default: // right.IsTimestampPosInf(): left finite − (+inf) = −inf
			return NewIntervalInfinity(false), nil
		}
	}
	diff := left.TimeValue().Sub(right.TimeValue()) // time.Duration (ns)
	micros := int64(diff / time.Microsecond)
	days := micros / usecsPerDay
	micros -= days * usecsPerDay
	return NewIntervalDatumFull(0, int32(days), micros), nil
}

// intervalOutOfRange is PG's error for a non-representable interval result
// (overflow, or an "infinity − infinity" that has no NaN equivalent).
// Mirrors ereport(ERRCODE_DATETIME_VALUE_OUT_OF_RANGE, "interval out of range").
// No Pos: every call site is pure runtime interval arithmetic
// (postgres/src/backend/utils/adt/timestamp.c has no errposition call
// anywhere in this file). M0134-0070.
func intervalOutOfRange(pos int) error {
	return &ExecError{Code: "22008", Message: "interval out of range"}
}

// finiteIntervalArith adds (or subtracts, when subtract) two FINITE intervals
// field-by-field, erroring on int32/int64 field overflow OR on a result that
// lands on a ±infinity sentinel — matching finite_interval_pl / finite_interval_mi
// (both guard with INTERVAL_NOT_FINITE(result)). (unimplemented_feat #5(d-iv))
func finiteIntervalArith(left, right Datum, subtract bool, pos int) (Datum, error) {
	sign := int64(1)
	if subtract {
		sign = -1
	}
	months := int64(left.IntervalMonthsValue()) + sign*int64(right.IntervalMonthsValue())
	days := int64(left.IntervalDaysValue()) + sign*int64(right.IntervalDaysValue())
	// time is int64; detect true 64-bit add/sub overflow (same guards as arithmetic()).
	lt, rt := left.IntervalMicrosValue(), right.IntervalMicrosValue()
	var micros int64
	if subtract {
		micros = lt - rt
		if (lt^rt)&(lt^micros) < 0 {
			return Datum{}, intervalOutOfRange(pos)
		}
	} else {
		micros = lt + rt
		if (lt^micros)&(rt^micros) < 0 {
			return Datum{}, intervalOutOfRange(pos)
		}
	}
	if months > math.MaxInt32 || months < math.MinInt32 ||
		days > math.MaxInt32 || days < math.MinInt32 {
		return Datum{}, intervalOutOfRange(pos)
	}
	res := NewIntervalDatumFull(int32(months), int32(days), micros)
	if res.IsIntervalNotFinite() {
		// Finite arithmetic must never synthesise a ±infinity sentinel.
		return Datum{}, intervalOutOfRange(pos)
	}
	return res, nil
}

// intervalDaysPerMonthF / intervalSecsPerDayF / intervalUsecsPerSecF mirror
// upstream's DAYS_PER_MONTH / SECS_PER_DAY / USECS_PER_SEC (timestamp.h),
// used only by the float-factor interval_mul / interval_div carry math below.
const (
	intervalDaysPerMonthF = 30.0
	intervalSecsPerDayF   = 86400.0
	intervalUsecsPerSecF  = 1000000.0
)

// tsround mirrors upstream's TSROUND macro (datatype/timestamp.h): round to
// MAX_TIMESTAMP_PRECISION (6 fractional digits) using round-to-nearest,
// ties-to-even (C's rint under the default rounding mode).
func tsround(j float64) float64 {
	return math.RoundToEven(j*1e6) / 1e6
}

// float8FitsInInt32 / float8FitsInInt64 mirror upstream's FLOAT8_FITS_IN_INT32
// / FLOAT8_FITS_IN_INT64 macros (c.h): true iff the float, when truncated
// toward zero, lands within the target integer's representable range.
func float8FitsInInt32(f float64) bool {
	return f >= float64(math.MinInt32) && f < -float64(math.MinInt32)
}

func float8FitsInInt64(f float64) bool {
	return f >= float64(math.MinInt64) && f < -float64(math.MinInt64)
}

// intervalLinearSign returns the sign of the interval's linear span (months
// widened to days at a fixed 30-day rate, plus days, plus the whole-day part
// of the microsecond field; the sub-day remainder breaks a zero-days tie) —
// mirroring interval_sign / interval_cmp_value (timestamp.c). Used only by
// intervalMul's ±infinity-factor short-circuit; ordinary comparisons and
// in_range go through the equivalent inline widening at expr.go's KindInterval
// compare arm and operators_window.go respectively (established pattern).
func intervalLinearSign(iv Datum) int {
	days := int64(iv.IntervalMonthsValue())*30 + int64(iv.IntervalDaysValue()) + iv.IntervalMicrosValue()/usecsPerDay
	frac := iv.IntervalMicrosValue() % usecsPerDay
	switch {
	case days != 0:
		if days < 0 {
			return -1
		}
		return 1
	case frac < 0:
		return -1
	case frac > 0:
		return 1
	}
	return 0
}

// addS32Overflow mirrors upstream's pg_add_s32_overflow: adds two int32s and
// reports whether the true sum overflowed int32 range.
func addS32Overflow(a, b int32) (int32, bool) {
	r := int64(a) + int64(b)
	if r > math.MaxInt32 || r < math.MinInt32 {
		return 0, true
	}
	return int32(r), false
}

// intervalMul implements `interval * factor` (interval_mul,
// postgres/src/backend/utils/adt/timestamp.c). Months and days are scaled by
// the float8 factor and truncated toward zero for their whole-unit part; any
// fractional whole-month remainder is cascaded into days (at a fixed 30-day
// rate) and any fractional whole-day remainder is cascaded into the
// microsecond time field (at a fixed 86400s day) — never cascading upward,
// matching upstream's comment that the user must call justify_hours /
// justify_days themselves. ±infinity/NaN factors and non-finite (±infinity
// sentinel) spans are handled exactly as upstream. M0134-0035.
func intervalMul(iv Datum, factor float64, pos int) (Datum, error) {
	if math.IsNaN(factor) {
		return Datum{}, intervalOutOfRange(pos)
	}
	if iv.IsIntervalNotFinite() {
		if factor == 0.0 {
			return Datum{}, intervalOutOfRange(pos)
		}
		if factor < 0.0 {
			return negateInterval(iv, pos)
		}
		return iv, nil
	}
	if math.IsInf(factor, 0) {
		sign := intervalLinearSign(iv)
		if sign == 0 {
			return Datum{}, intervalOutOfRange(pos)
		}
		if factor*float64(sign) < 0 {
			return NewIntervalInfinity(false), nil
		}
		return NewIntervalInfinity(true), nil
	}

	origMonth := iv.IntervalMonthsValue()
	origDay := iv.IntervalDaysValue()

	resultMonthF := float64(origMonth) * factor
	if math.IsNaN(resultMonthF) || !float8FitsInInt32(resultMonthF) {
		return Datum{}, intervalOutOfRange(pos)
	}
	resultMonth := int32(resultMonthF)

	resultDayF := float64(origDay) * factor
	if math.IsNaN(resultDayF) || !float8FitsInInt32(resultDayF) {
		return Datum{}, intervalOutOfRange(pos)
	}
	resultDay := int32(resultDayF)

	// Fractional months cascade into days; the whole-day part of that
	// cascade further folds into monthRemainderDays, and the leftover
	// sub-day fraction (plus the days field's own fractional remainder)
	// cascades into seconds.
	monthRemainderDays := tsround((float64(origMonth)*factor - float64(resultMonth)) * intervalDaysPerMonthF)
	secRemainder := tsround((float64(origDay)*factor - float64(resultDay) +
		monthRemainderDays - float64(int64(monthRemainderDays))) * intervalSecsPerDayF)

	// May have accumulated a full day (or more) of seconds via rounding or
	// cascade from months/days.
	if math.Abs(secRemainder) >= intervalSecsPerDayF {
		carry := int32(secRemainder / intervalSecsPerDayF)
		var overflow bool
		resultDay, overflow = addS32Overflow(resultDay, carry)
		if overflow {
			return Datum{}, intervalOutOfRange(pos)
		}
		secRemainder -= float64(carry) * intervalSecsPerDayF
	}

	var overflow bool
	resultDay, overflow = addS32Overflow(resultDay, int32(monthRemainderDays))
	if overflow {
		return Datum{}, intervalOutOfRange(pos)
	}

	resultTimeF := math.RoundToEven(float64(iv.IntervalMicrosValue())*factor + secRemainder*intervalUsecsPerSecF)
	if math.IsNaN(resultTimeF) || !float8FitsInInt64(resultTimeF) {
		return Datum{}, intervalOutOfRange(pos)
	}

	res := NewIntervalDatumFull(resultMonth, resultDay, int64(resultTimeF))
	if res.IsIntervalNotFinite() {
		return Datum{}, intervalOutOfRange(pos)
	}
	return res, nil
}

// intervalDiv implements `interval / factor` (interval_div,
// postgres/src/backend/utils/adt/timestamp.c). Mirrors intervalMul's
// month/day/time carry logic with division substituted for multiplication
// (and a division_by_zero guard PG's interval_mul has no equivalent of,
// since 0 is never a valid multiplier's reciprocal). M0134-0035.
func intervalDiv(iv Datum, factor float64, pos int) (Datum, error) {
	if factor == 0.0 {
		// No Pos: pure runtime evaluation (interval_div,
		// postgres/src/backend/utils/adt/timestamp.c, has no errposition
		// call). M0134-0070.
		return Datum{}, &ExecError{Code: "22012", Message: "division by zero"}
	}
	if math.IsNaN(factor) {
		return Datum{}, intervalOutOfRange(pos)
	}
	if iv.IsIntervalNotFinite() {
		if math.IsInf(factor, 0) {
			return Datum{}, intervalOutOfRange(pos)
		}
		if factor < 0.0 {
			return negateInterval(iv, pos)
		}
		return iv, nil
	}

	origMonth := iv.IntervalMonthsValue()
	origDay := iv.IntervalDaysValue()

	resultMonthF := float64(origMonth) / factor
	if math.IsNaN(resultMonthF) || !float8FitsInInt32(resultMonthF) {
		return Datum{}, intervalOutOfRange(pos)
	}
	resultMonth := int32(resultMonthF)

	resultDayF := float64(origDay) / factor
	if math.IsNaN(resultDayF) || !float8FitsInInt32(resultDayF) {
		return Datum{}, intervalOutOfRange(pos)
	}
	resultDay := int32(resultDayF)

	monthRemainderDays := tsround((float64(origMonth)/factor - float64(resultMonth)) * intervalDaysPerMonthF)
	secRemainder := tsround((float64(origDay)/factor - float64(resultDay) +
		monthRemainderDays - float64(int64(monthRemainderDays))) * intervalSecsPerDayF)

	if math.Abs(secRemainder) >= intervalSecsPerDayF {
		carry := int32(secRemainder / intervalSecsPerDayF)
		var overflow bool
		resultDay, overflow = addS32Overflow(resultDay, carry)
		if overflow {
			return Datum{}, intervalOutOfRange(pos)
		}
		secRemainder -= float64(carry) * intervalSecsPerDayF
	}

	var overflow bool
	resultDay, overflow = addS32Overflow(resultDay, int32(monthRemainderDays))
	if overflow {
		return Datum{}, intervalOutOfRange(pos)
	}

	resultTimeF := math.RoundToEven(float64(iv.IntervalMicrosValue())/factor + secRemainder*intervalUsecsPerSecF)
	if math.IsNaN(resultTimeF) || !float8FitsInInt64(resultTimeF) {
		return Datum{}, intervalOutOfRange(pos)
	}

	res := NewIntervalDatumFull(resultMonth, resultDay, int64(resultTimeF))
	if res.IsIntervalNotFinite() {
		return Datum{}, intervalOutOfRange(pos)
	}
	return res, nil
}

// intervalInfinityRank maps an interval to its ordering rank against the
// ±infinity sentinels: −1 for −infinity, +1 for +infinity, 0 for any finite
// interval. Used only to order the sentinels exactly in compareDatums.
func intervalInfinityRank(d Datum) int {
	switch {
	case d.IsIntervalNoBegin():
		return -1
	case d.IsIntervalNoEnd():
		return 1
	default:
		return 0
	}
}

// addIntervalInterval implements `interval ± interval`, combining the
// month/day/microsecond fields independently (interval_pl / interval_mi).
// ±infinity operands short-circuit exactly as upstream: like-signed infinities
// pass through, but any "infinity − infinity" (interval has no NaN) errors.
func addIntervalInterval(left, right Datum, subtract bool, pos int) (Datum, error) {
	if !subtract {
		// interval_pl
		switch {
		case left.IsIntervalNoBegin():
			if right.IsIntervalNoEnd() {
				return Datum{}, intervalOutOfRange(pos)
			}
			return NewIntervalInfinity(false), nil
		case left.IsIntervalNoEnd():
			if right.IsIntervalNoBegin() {
				return Datum{}, intervalOutOfRange(pos)
			}
			return NewIntervalInfinity(true), nil
		case right.IsIntervalNotFinite():
			return right, nil
		}
		return finiteIntervalArith(left, right, false, pos)
	}
	// interval_mi
	switch {
	case left.IsIntervalNoBegin():
		if right.IsIntervalNoBegin() {
			return Datum{}, intervalOutOfRange(pos)
		}
		return NewIntervalInfinity(false), nil
	case left.IsIntervalNoEnd():
		if right.IsIntervalNoEnd() {
			return Datum{}, intervalOutOfRange(pos)
		}
		return NewIntervalInfinity(true), nil
	case right.IsIntervalNoBegin():
		return NewIntervalInfinity(true), nil
	case right.IsIntervalNoEnd():
		return NewIntervalInfinity(false), nil
	}
	return finiteIntervalArith(left, right, true, pos)
}

// negateInterval implements unary `- interval` (interval_um / interval_um_internal,
// postgres/src/backend/utils/adt/timestamp.c:3444): the ±infinity sentinels swap
// (NOBEGIN↔NOEND) and every finite field is negated with an overflow guard, also
// erroring if the negation lands exactly on a ±infinity sentinel.
// (unimplemented_feat #5(d-iv))
func negateInterval(d Datum, pos int) (Datum, error) {
	switch {
	case d.IsIntervalNoBegin():
		// -(-infinity) = +infinity (INTERVAL_NOBEGIN -> INTERVAL_NOEND)
		return NewIntervalInfinity(true), nil
	case d.IsIntervalNoEnd():
		// -(+infinity) = -infinity (INTERVAL_NOEND -> INTERVAL_NOBEGIN)
		return NewIntervalInfinity(false), nil
	}
	months := d.IntervalMonthsValue()
	days := d.IntervalDaysValue()
	micros := d.IntervalMicrosValue()
	// pg_sub_s64/s32_overflow(0, x): 0-x overflows only when x is the signed min.
	if micros == math.MinInt64 || months == math.MinInt32 || days == math.MinInt32 {
		return Datum{}, intervalOutOfRange(pos)
	}
	res := NewIntervalDatumFull(-months, -days, -micros)
	if res.IsIntervalNotFinite() {
		// Negating a finite interval must never synthesise a ±infinity sentinel.
		return Datum{}, intervalOutOfRange(pos)
	}
	return res, nil
}

// arithmetic implements int64 (bigint) +, -, *, /, % with PostgreSQL's exact
// overflow detection. No Pos on any raise site: every one mirrors a pure
// runtime C function (int8pl/int8mi/int8mul/int8div/int8mod,
// postgres/src/backend/utils/adt/int8.c:445-530) that calls ereport(ERROR,
// ...) with no errposition — confirmed no int8.c function in that range
// calls errposition. M0134-0070.
func arithmetic(op parser.OpCode, a, b int64, pos int) (Datum, error) {
	var r int64
	switch op {
	case parser.OpAdd:
		r = a + b
		// Detect int64 add overflow: same-sign inputs with opposite-sign result.
		if (a^r)&(b^r) < 0 {
			return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
		}
	case parser.OpSub:
		r = a - b
		// Detect int64 sub overflow: different-sign inputs with result differing from a's sign.
		if (a^b)&(a^r) < 0 {
			return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
		}
	case parser.OpMul:
		// Detect int64 multiplication overflow. M0097-int8-overflow.
		r = a * b
		if a != 0 && b != 0 {
			if a == math.MinInt64 || b == math.MinInt64 {
				// MinInt64 * 1 = MinInt64 (OK); MinInt64 * anything_else overflows.
				if a != 1 && b != 1 {
					return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
				}
			} else if r/a != b {
				return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
			}
		}
	case parser.OpDiv:
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Message: "division by zero"}
		}
		// MinInt64 / -1 overflows: the mathematical result 2^63 doesn't fit in int64.
		if a == math.MinInt64 && b == -1 {
			return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
		}
		r = a / b
	case parser.OpMod:
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Message: "division by zero"}
		}
		r = a % b
	}
	return Datum{Kind: KindInt, Int: r}, nil
}

// promoteCrossKind attempts implicit type promotion for common
// cross-kind pairs that may arise from planner-side column-index
// misalignments.  PostgreSQL performs these coercions implicitly;
// this is the v0 executor-level fallback so the query completes
// instead of erroring.  Returns the (possibly-promoted) operands.
func promoteCrossKind(a, b Datum) (Datum, Datum) {
	if a.Kind == b.Kind {
		return a, b
	}
	// M0073-0001: treat KindString / KindStringArena uniformly
	// as "string" for the cross-kind parse-and-compare path.
	aIsString := a.Kind == KindString
	bIsString := b.Kind == KindString
	// One side is string — try to parse it as the other's type.
	if aIsString && !bIsString {
		a = tryParseStringAs(b.Kind, a.StringValue())
	} else if bIsString && !aIsString {
		b = tryParseStringAs(a.Kind, b.StringValue())
	}
	// KindInterval parses through tryParseStringAs like every other target
	// (it gained its arm when interval columns started decoding as
	// KindInterval); a string neither side can parse is returned unchanged so
	// the caller still errors instead of silently comparing garbage.
	return a, b
}

// tryParseStringAs attempts to parse s as the given target kind.
// On success it returns a Datum with that kind; on failure it
// returns a KindString Datum (the original), letting the caller
// produce a proper type-mismatch error.
// hasISODatePrefix reports whether s opens with a canonical, zero-padded
// "YYYY-MM-DD" date field — the same fixed-offset probe parseTimeString and
// parseTimeTZString use to decide a string carries a date. Callers pass a
// pgdatetime.NormalizeInput'd string so the unpadded spellings PG accepts are
// recognised too.
func hasISODatePrefix(s string) bool {
	return len(s) >= 10 && s[4] == '-' && s[7] == '-'
}

// arraySubscriptElemDatum re-types one array element's already-unquoted text as
// the element type's own Datum, reporting false when the type has no kind of its
// own here (text-likes, uuid, the fixed-width integers the caller's own fast path
// already covers) or when the text does not parse.
//
// Scope note (M0119-0006): only the element types whose Datum kind changes the
// ANSWER are routed. Upstream types every subscript through the element type's
// input function, but goopg's KindTime rendering is not yet byte-identical to
// the array codec's element spelling, so routing date/time/timestamp here would
// trade a comparison fix for a rendering regression — recorded in the deferral
// ledger rather than guessed at. Falling through leaves the pre-existing text
// behaviour, which is already correct for ISO date/timestamp comparisons because
// their spelling sorts the same way their values do.
func arraySubscriptElemDatum(elemType, elem string) (Datum, bool) {
	switch strings.ToLower(elemType) {
	case "interval":
		// Same tokenizer as interval_in and the storage-encode arm, so the
		// element re-renders through formatInterval exactly as the array codec
		// spelled it.
		if months, days, micros, ok := parser.ParseIntervalBody(elem); ok {
			return NewIntervalDatumFull(months, days, micros), true
		}
	case "numeric", "decimal", "float8", "float4", "double precision", "real":
		// Numeric equality is value-based, so '1.50' = '1.5'; as text they differ.
		// The scale survives the round trip, so `n[1]` still prints 1.50.
		//
		// The float types land here too, because goopg has no KindFloat: a
		// float8 COLUMN already decodes to KindNumeric and renders through the
		// same path, so routing float elements here makes the subscript agree
		// with its own scalar column. Without it `f[1] > f[2]` over
		// ARRAY[9.5,10.2]::float8[] answered t (text: "9.5" > "10.2") where PG
		// answers f. A spelling parseNumeric cannot take (scientific notation)
		// falls through to the pre-existing text behaviour rather than guessing.
		if v, scale, ok := parseNumericFast(elem); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, true
		}
		if m, sc, err := parseNumeric(elem); err == nil {
			return newNumeric(m, int(sc)), true
		}
	}
	return NullDatum, false
}

func tryParseStringAs(target DatumKind, s string) Datum {
	switch target {
	case KindInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Datum{Kind: KindInt, Int: n}
		}
	case KindNumeric:
		if m, sc, err := parseNumeric(s); err == nil {
			return newNumeric(m, int(sc))
		}
	case KindTime:
		// M0125-0007: a literal that carries a DATE part is a timestamp and has
		// to be decoded as one FIRST. parseTimeString strips the date prefix and
		// returns the bare time-of-day anchored at 1970-01-01, so reaching it
		// first made `ts_col = '2002-05-01 03:04:05'` compare 2002-05-01 against
		// 1970-01-01 and silently report no match — the same silent-wrong-answer
		// shape as the unpadded-field defect, one type over. PG never has this
		// ambiguity: transformExpr coerces the unknown literal to the column's
		// type before evaluation.
		// M0119-0006: a separator-less digit run is a date to DecodeDateTime
		// ('20200101') but a time of day to DecodeTimeOnly ('040506'), and this
		// path has no target type to decide with. Take the date reading only for
		// the widths that cannot also be a time — see
		// pgdatetime.RunTogetherDateIsTimeAmbiguous.
		normalized := datetime.NormalizeDateTimeInput(s, false)
		if datetime.RunTogetherDateIsTimeAmbiguous(s) {
			normalized = datetime.NormalizeInput(s)
		}
		if hasISODatePrefix(normalized) {
			if t, err := parseCopyTimestamp(s); err == nil {
				return NewTimeDatum(t)
			}
		}
		// Try timetz first ("HH:MM:SS±HH[:MM]") to preserve the offset.
		// M0097-0004: strings like '05:06:07-07' must compare as timetz, not plain time.
		//
		// The session zone is deliberately NOT passed here: this arm is deciding
		// which TYPE an untyped string denotes, and it decides by whether a zone
		// was written. Feeding it the session default would make every bare
		// "10:00" arrive with a non-zero offset on a non-UTC session and be
		// mistyped as timetz. M0119-0006.
		if ts, offsetSecs, err := parseTimeTZString(s, ""); err == nil && offsetSecs != 0 {
			return NewTimeTZDatum(ts, offsetSecs)
		}
		// Try time-of-day first ("HH:MM:SS") then full timestamp.
		if t, err := parseTimeString(s); err == nil {
			return NewTimeDatum(t)
		}
		if t, err := parseCopyTimestamp(s); err == nil {
			return NewTimeDatum(t)
		}
	case KindInterval:
		// `i > '10 days'` on an interval column: the literal is `unknown`
		// upstream and transformExpr coerces it to interval before the operator
		// is resolved, so both sides reach interval_gt. goopg has no such
		// coercion pass, and until interval columns were stored in PG's native
		// layout both sides happened to be strings, so the comparison "worked"
		// lexicographically and wrongly. With the column now KindInterval the
		// string side must be parsed here or the pair falls through to the
		// Format()-vs-Format() fallback below — text comparison again, just one
		// level down. Same tokenizer as interval_in / the storage-encode arm.
		if months, days, micros, ok := parser.ParseIntervalBody(s); ok {
			return NewIntervalDatumFull(months, days, micros)
		}
	}
	return NewStringDatum(s)
}

// compareDatum returns -1/0/1 the same way upstream's btree
// comparators do, scoped to the v0 type set.
// splitRowElements splits a PostgreSQL row-literal string "(e1,e2,...)" into
// its elements. Handles nested parentheses and double-quoted strings.
// Returns nil if s is not a valid row literal.
func splitRowElements(s string) []string {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return nil
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}
	var elems []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inQuote {
			if c == '"' {
				if i+1 < len(inner) && inner[i+1] == '"' {
					i++ // escaped quote
				} else {
					inQuote = false
				}
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				elems = append(elems, inner[start:i])
				start = i + 1
			}
		}
	}
	elems = append(elems, inner[start:])
	return elems
}

// compareRowElem compares two row element strings element-wise.
// Numeric strings are compared numerically; others lexicographically.
func compareRowElem(a, b string) int {
	if a == b {
		return 0
	}
	// NULL is represented as empty string in row format; NULL < any non-NULL.
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	// Try numeric comparison.
	af, aerr := strconv.ParseFloat(a, 64)
	bf, berr := strconv.ParseFloat(b, 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// compareRowStrings compares two PostgreSQL composite-type (row) strings.
// Elements are compared in order; numeric elements use numeric comparison.
// Returns 0 if not recognizable as row format, falling back to lexicographic.
// splitArrayElements splits a PostgreSQL array literal "{e1,e2,...}" into elements.
// Handles nested arrays like "{{1,2},{3,4}}" and quoted elements.
func splitArrayElements(s string) []string {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}
	var elems []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inQuote {
			if c == '"' {
				if i+1 < len(inner) && inner[i+1] == '"' {
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				elems = append(elems, inner[start:i])
				start = i + 1
			}
		}
	}
	elems = append(elems, inner[start:])
	return elems
}

// compareArrayStrings compares two PostgreSQL array literals element-wise.
// Nested arrays are compared recursively; scalar elements use numeric comparison.
func compareArrayStrings(a, b string) int {
	ae := splitArrayElements(a)
	be := splitArrayElements(b)
	if ae == nil || be == nil {
		return strings.Compare(a, b)
	}
	n := len(ae)
	if len(be) < n {
		n = len(be)
	}
	for i := 0; i < n; i++ {
		ea, eb := ae[i], be[i]
		var c int
		if len(ea) > 0 && ea[0] == '{' && len(eb) > 0 && eb[0] == '{' {
			c = compareArrayStrings(ea, eb)
		} else {
			c = compareRowElem(ea, eb)
		}
		if c != 0 {
			return c
		}
	}
	return len(ae) - len(be)
}

func compareRowStrings(a, b string) int {
	ae := splitRowElements(a)
	be := splitRowElements(b)
	if ae == nil || be == nil {
		return strings.Compare(a, b)
	}
	n := len(ae)
	if len(be) < n {
		n = len(be)
	}
	for i := 0; i < n; i++ {
		c := compareRowElem(ae[i], be[i])
		if c != 0 {
			return c
		}
	}
	return len(ae) - len(be)
}

func compareDatum(a, b Datum, pos int) (int, error) {
	// Implicit cross-kind promotion so planner-side column-index
	// misalignments don't crash the entire query.  PostgreSQL
	// handles these implicitly; goopg v0 mirrors that behaviour
	// for the common pairs that appear in TPC-H.
	a, b = promoteCrossKind(a, b)

	// NUMERIC ↔ INT: promote int to numeric so the comparison is
	// scale-aware. NUMERIC ↔ NUMERIC: align scales then compare
	// mantissas. Identical kinds drop through to the per-kind
	// switch below.
	if a.Kind == KindNumeric || b.Kind == KindNumeric {
		if a.Kind == KindInt {
			a = numericFromInt(a.Int)
		}
		if b.Kind == KindInt {
			b = numericFromInt(b.Int)
		}
		if a.Kind != KindNumeric || b.Kind != KindNumeric {
			return strings.Compare(a.Format(), b.Format()), nil
		}
		return numericCmp(a, b)
	}
	if a.Kind != b.Kind {
		// M0073-0001: arena and non-arena string/bytes Datums
		// are logically the same Kind for comparison purposes.
		// Treat KindString ↔ KindStringArena and KindBytes ↔
		// KindBytesArena as same-kind so the per-kind switch
		// below dispatches correctly.
		aIsString := a.Kind == KindString
		bIsString := b.Kind == KindString
		if aIsString && bIsString {
			as, bs := a.StringValue(), b.StringValue()
			// pg_lsn comparison: use uint64 semantics. M0097-pg_lsn.
			if looksLikePgLSN(as) && looksLikePgLSN(bs) {
				lu, errL := parsePgLSN(as)
				ru, errR := parsePgLSN(bs)
				if errL == nil && errR == nil {
					if lu < ru {
						return -1, nil
					}
					if lu > ru {
						return 1, nil
					}
					return 0, nil
				}
			}
			// UUID cross-format comparison: if either looks like a UUID in any format,
			// normalize both to canonical form so hyphenated matches non-hyphenated. M0097-0003.
			if isValidUUIDStr(as) || isValidUUIDStr(bs) {
				if isValidUUIDStr(as) {
					as = normalizeUUIDStr(as)
				}
				if isValidUUIDStr(bs) {
					bs = normalizeUUIDStr(bs)
				}
			}
			return strings.Compare(as, bs), nil
		}
		aIsBytes := a.Kind == KindBytes
		bIsBytes := b.Kind == KindBytes
		if aIsBytes && bIsBytes {
			return strings.Compare(string(a.BytesValue()), string(b.BytesValue())), nil
		}
		// bytea vs string: the string side is an unknown-type literal that PG
		// would have coerced to bytea before the operator was resolved, so
		// `b = '\xaabb'` compares TWO BYTES, not two bytes against the six
		// characters of the escape text. Without this, M0125-0021's storage fix
		// would have made every such predicate silently match nothing. A string
		// that is not valid bytea input keeps the old raw comparison rather
		// than failing the query, matching this block's fall-back convention.
		if aIsBytes != bIsBytes {
			lit, other := b, a
			if bIsBytes {
				lit, other = a, b
			}
			if coerced, cerr := byteaIn(lit.StringValue(), 0); cerr == nil {
				if aIsBytes {
					return bytes.Compare(other.BytesValue(), coerced), nil
				}
				return bytes.Compare(coerced, other.BytesValue()), nil
			}
		}
		// Fall back to string comparison so planner-side column
		// misalignments don't crash the entire query.  The result
		// may not be PostgreSQL-correct, but the query completes.
		return strings.Compare(a.Format(), b.Format()), nil
	}
	switch a.Kind {
	case KindInt:
		switch {
		case a.Int < b.Int:
			return -1, nil
		case a.Int > b.Int:
			return 1, nil
		}
		return 0, nil
	case KindBool:
		switch {
		case !a.BoolValue() && b.BoolValue():
			return -1, nil
		case a.BoolValue() && !b.BoolValue():
			return 1, nil
		}
		return 0, nil
	case KindString:
		as, bs := a.StringValue(), b.StringValue()
		// pg_lsn comparison: use uint64 semantics, not lexicographic. M0097-pg_lsn.
		if looksLikePgLSN(as) && looksLikePgLSN(bs) {
			lu, errL := parsePgLSN(as)
			ru, errR := parsePgLSN(bs)
			if errL == nil && errR == nil {
				if lu < ru {
					return -1, nil
				}
				if lu > ru {
					return 1, nil
				}
				return 0, nil
			}
		}
		// UUID cross-format comparison: normalize both if either is a valid UUID. M0097-0003.
		if isValidUUIDStr(as) || isValidUUIDStr(bs) {
			if isValidUUIDStr(as) {
				as = normalizeUUIDStr(as)
			}
			if isValidUUIDStr(bs) {
				bs = normalizeUUIDStr(bs)
			}
		}
		// Composite row literal comparison: "(e1,e2,...)" uses element-wise
		// numeric comparison so max(row(a,b)) works correctly. M0097-0115.
		if len(as) > 0 && as[0] == '(' && len(bs) > 0 && bs[0] == '(' {
			return compareRowStrings(as, bs), nil
		}
		// Array literal comparison: "{e1,e2,...}" uses element-wise numeric
		// comparison so min/max over integer arrays work correctly. M0097-0117.
		if len(as) > 0 && as[0] == '{' && len(bs) > 0 && bs[0] == '{' {
			return compareArrayStrings(as, bs), nil
		}
		return strings.Compare(as, bs), nil
	case KindBytes:
		return bytes.Compare(a.BytesValue(), b.BytesValue()), nil
	case KindTime:
		// For timetz datums (Scale != 0) PostgreSQL compares by UTC time
		// (local_nanos - offset_nanos), then by offset as tiebreaker.
		// Plain time/timestamp datums (Scale == 0) compare by Int directly.
		// M0097-0004.
		if a.Scale != 0 || b.Scale != 0 {
			aUTC := a.Int - int64(a.Scale)*60*1_000_000_000
			bUTC := b.Int - int64(b.Scale)*60*1_000_000_000
			switch {
			case aUTC < bUTC:
				return -1, nil
			case aUTC > bUTC:
				return 1, nil
			}
			// Same UTC: smaller offset (more east) sorts last in PG.
			switch {
			case a.Scale > b.Scale:
				return -1, nil
			case a.Scale < b.Scale:
				return 1, nil
			}
			return 0, nil
		}
		switch {
		case a.TimeValue().Before(b.TimeValue()):
			return -1, nil
		case a.TimeValue().After(b.TimeValue()):
			return 1, nil
		}
		return 0, nil
	case KindEnum:
		// Enum comparison uses sort order, not label. M0097-enum-sort.
		ao := math.Float64frombits(uint64(a.Int))
		bo := math.Float64frombits(uint64(b.Int))
		switch {
		case ao < bo:
			return -1, nil
		case ao > bo:
			return 1, nil
		}
		return 0, nil
	case KindInterval:
		// ±infinity sentinels order exactly: −infinity precedes and +infinity
		// follows every finite interval (and each other), matching
		// interval_cmp_internal's non-finite short-circuit. Must run before the
		// lossy day-widening below, whose int64 sum is not exact at the extremes.
		if a.IsIntervalNotFinite() || b.IsIntervalNotFinite() {
			ra := intervalInfinityRank(a)
			rb := intervalInfinityRank(b)
			switch {
			case ra < rb:
				return -1, nil
			case ra > rb:
				return 1, nil
			default:
				return 0, nil
			}
		}
		// Mirrors PostgreSQL's interval_cmp_value (timestamp.c): months are
		// widened to days at a fixed 30-day rate and combined with the day
		// field plus the whole-day part of the microsecond component into a
		// single day count; the sub-day microsecond remainder breaks ties.
		// Decomposing this way (rather than multiplying days back up to
		// microseconds) keeps the comparison inside int64 for the ranges
		// goopg produces. M0122-0004 / timestamp − timestamp subtraction.
		aDays := int64(a.IntervalMonthsValue())*30 + int64(a.IntervalDaysValue()) + a.IntervalMicrosValue()/usecsPerDay
		bDays := int64(b.IntervalMonthsValue())*30 + int64(b.IntervalDaysValue()) + b.IntervalMicrosValue()/usecsPerDay
		aFrac := a.IntervalMicrosValue() % usecsPerDay
		bFrac := b.IntervalMicrosValue() % usecsPerDay
		switch {
		case aDays < bDays:
			return -1, nil
		case aDays > bDays:
			return 1, nil
		case aFrac < bFrac:
			return -1, nil
		case aFrac > bFrac:
			return 1, nil
		}
		return 0, nil
	}
	return 0, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("comparison not supported for kind %d", a.Kind)}
}

func cmpResult(op parser.OpCode, cmp int) bool {
	switch op {
	case parser.OpEq:
		return cmp == 0
	case parser.OpNe:
		return cmp != 0
	case parser.OpLt:
		return cmp < 0
	case parser.OpLe:
		return cmp <= 0
	case parser.OpGt:
		return cmp > 0
	case parser.OpGe:
		return cmp >= 0
	}
	return false
}

// evalAnd / evalOr implement Kleene three-valued logic.
func evalAnd(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && !a.BoolValue() {
		return NewBoolDatum(false)
	}
	if !b.IsNull() && b.Kind == KindBool && !b.BoolValue() {
		return NewBoolDatum(false)
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return NewBoolDatum(a.BoolValue() && b.BoolValue())
}

func evalOr(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && a.BoolValue() {
		return NewBoolDatum(true)
	}
	if !b.IsNull() && b.Kind == KindBool && b.BoolValue() {
		return NewBoolDatum(true)
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return NewBoolDatum(a.BoolValue() || b.BoolValue())
}

// evalTypedStringLit parses the body of a `<type> 'value'`
// literal at evaluation time. v0 supports date / timestamp /
// timestamptz; the parsed time is normalised to UTC.
//
// M0066-0002: caches the parsed time on the planner node so
// repeated evaluations in a hot loop (e.g. Q5's date filter
// applied per orders row) skip the `time.Parse` cost. pprof
// showed `time.parse` at 10.5 % cumulative CPU pre-cache.
//
// M0119-0006: `ctx` reaches only the timetz arm, whose zone-less default is the
// SESSION TimeZone. That arm never fills CachedTime — it must not, since the
// cache is keyed on the planner node alone and would freeze one session's zone
// into a plan another session reuses.
//
// M0134-0026: the timestamptz arm now has the same hazard for a zone-less
// literal (it also reads the session TimeZone) and follows the same rule —
// see usedSession in that arm.
func evalTypedStringLit(x *optimizer.TypedStringLit, ctx *Context) (Datum, error) {
	if x.CacheValid {
		return NewTimeDatum(x.CachedTime), nil
	}
	switch x.Type {
	case "bool", "boolean":
		if b, ok := pgBoolIn(x.Value); ok {
			return NewBoolDatum(b), nil
		}
		return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
			Message: fmt.Sprintf("invalid input syntax for type boolean: %q", x.Value)}

	case "int2", "smallint":
		n, err := parseIntegerInput(x.Value, "smallint", 16)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "int4", "integer", "int":
		n, err := parseIntegerInput(x.Value, "integer", 32)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "int8", "bigint":
		n, err := parseIntegerInput(x.Value, "bigint", 64)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "float", "float4", "real", "float8":
		// Goopg v0 stores floats as KindNumeric strings. Validate via
		// ParseFloat so the error message is PostgreSQL-compatible.
		v := strings.TrimSpace(x.Value)
		_, err := strconv.ParseFloat(v, 64)
		if err != nil {
			typname := "double precision"
			if x.Type == "float4" || x.Type == "real" {
				typname = "real"
			}
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type %s: %q", typname, x.Value)}
		}
		m, s, perr := parseNumeric(v)
		if perr != nil {
			return NewStringDatum(v), nil
		}
		return newNumeric(m, int(s)), nil

	case "numeric", "decimal":
		// Return as string — goopg v0 stores numerics as text.
		return NewStringDatum(strings.TrimSpace(x.Value)), nil

	case "text", "bpchar", "char", "varchar":
		return NewStringDatum(x.Value), nil

	case "name":
		// name type truncates to NAMEDATALEN-1 = 63 bytes. M0097-0003.
		s := x.Value
		if len(s) > 63 {
			s = s[:63]
		}
		return NewStringDatum(s), nil

	case "oid":
		// oid is uint32: 0..4294967295. M0097-0003.
		n, err := parseIntegerInput(x.Value, "oid", 64)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		if n < 0 || n > 4294967295 {
			return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(),
				Message: fmt.Sprintf("value %q is out of range for type oid", x.Value)}
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "xid":
		// xid is a 32-bit unsigned transaction ID. Accepts decimal, octal (0NNN), hex (0xNNN).
		// -1 wraps to 4294967295, matching PostgreSQL behaviour. M0097-0018.
		v := strings.TrimSpace(x.Value)
		// Special case: PostgreSQL allows "-1" as 2^32-1 = 4294967295.
		if v == "-1" {
			return Datum{Kind: KindInt, Int: int64(uint32(0xffffffff))}, nil
		}
		n, err := parseXid(v)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type xid: %q", x.Value)}
		}
		return Datum{Kind: KindInt, Int: int64(n)}, nil

	case "xid8":
		// xid8 is a 64-bit unsigned transaction ID. M0097-0018.
		v := strings.TrimSpace(x.Value)
		// Special case: PostgreSQL allows "-1" as 2^64-1 = 18446744073709551615.
		if v == "-1" {
			return Datum{Kind: KindInt, Int: -1}, nil // int64(-1) == uint64(0xffffffffffffffff) bitwise
		}
		n, err := parseXid8(v)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type xid8: %q", x.Value)}
		}
		return Datum{Kind: KindInt, Int: int64(n)}, nil

	case "date":
		// PG's 'infinity' / '-infinity' spellings have no finite time.Time and
		// map to the DATEVAL_NOEND / DATEVAL_NOBEGIN sentinel; intercept before
		// the layout parse (not time-cached). (unimplemented_feat #5(d-iv))
		if inf, ok := parseDateInfinityLiteral(x.Value); ok {
			return inf, nil
		}
		// M0125-0007 / M0119-0006: PG's DecodeDate reads each numeric field on
		// its own ('2002-5-1' is '2002-05-01') and accepts a trailing era token
		// ('2020-01-01 BC'); parsePGDateText applies both around the fixed Go
		// layout, which can express neither.
		t, err := parsePGDateText(x.Value)
		if err != nil {
			return Datum{}, dateTimeInputError(err, "date", x.Value, x.Pos())
		}
		x.CachedTime = t.UTC()
		x.CacheValid = true
		return NewTimeDatum(x.CachedTime), nil
	case "time":
		ts, err := parseTimeString(x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid input syntax for type time: %q", x.Value)}
		}
		return NewTimeDatum(ts), nil
	case "timetz":
		ts, offsetSecs, err := parseTimeTZString(x.Value, timeZoneFromCtx(ctx))
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid input syntax for type time with time zone: %q", x.Value)}
		}
		return NewTimeTZDatum(ts, offsetSecs), nil
	case "timestamp", "timestamptz":
		// Try a few common upstream layouts in order. The
		// `2006-01-02 15:04:05` form is what TPC-H and pgbench
		// use. PostgreSQL's timestamp(tz) input also accepts a
		// seconds-less `HH:MM` time and an optional numeric
		// timezone offset (e.g. `2010-04-01 10:00` and
		// `2010-04-01 10:00:00-04`); the tz-suffixed and
		// seconds-less variants below cover those. The tz-bearing
		// layouts are tried first so an explicit offset is honoured
		// before the zone-less fallbacks treat the wall clock as UTC.
		// Without these the isolation classroom-scheduling /
		// receipt-report specs (which book rooms on the half-hour with
		// `TIMESTAMP WITH TIME ZONE '2010-04-01 10:00'`) fail their
		// setup INSERT with `invalid timestamp` (22007).
		//
		// PG's special 'infinity' / '-infinity' spellings have no finite
		// time.Time and so are intercepted before the layout loop; the
		// ±infinity sentinel is not time-cached (detection is a trivial
		// string compare). (unimplemented_feat #5(d-iv))
		if inf, ok := parseTimestampInfinityLiteral(x.Value); ok {
			return inf, nil
		}
		// M0125-0007: same field-at-a-time acceptance as the date case above —
		// PG takes '2002-5-1 3:4:5' for a timestamp, the layouts below do not.
		// M0119-0006: the whole entry point (era split + normalisation + the
		// shared pgTimestampLayouts table) is now parsePGTimestampText, which
		// the COPY/encode path calls too, so the literal path and the COPY path
		// can no longer accept different spellings of the same timestamp.
		// M0119-0006: the literal's own type decides what happens to a zone the
		// input carries — TIMESTAMP '2020-01-01 10:00:00+05:30' is 10:00:00, the
		// offset parsed and thrown away, while the TIMESTAMPTZ spelling of the
		// same text is 04:30:00 UTC. See tsZoneMode.
		//
		// M0134-0026: a zone-less TIMESTAMPTZ literal is read as local wall-clock
		// time in the session TimeZone GUC (DecodeDateTime, datetime.c:1573-
		// 1583), which parsePGTimestampTextZoneSession needs ctx's setting for.
		// usedSession reports whether that branch actually fired; when it did,
		// the answer depends on THIS session's TimeZone, so — like the timetz
		// arm above — it must not be written to x.CachedTime/x.CacheValid, or a
		// different session with a different TimeZone reusing this plan node
		// would get the wrong instant.
		t, usedSession, err := parsePGTimestampTextZoneSession(x.Value, tsZoneModeForType(x.Type), timeZoneFromCtx(ctx))
		if err != nil {
			if ee := dateTimeInputError(err, x.Type, x.Value, x.Pos()); ee.Code == "22008" {
				return Datum{}, ee
			}
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid timestamp %q", x.Value)}
		}
		if !usedSession {
			x.CachedTime = t
			x.CacheValid = true
		}
		// M0119-0006 (40th slice): the literal's own type also decides how the
		// value is later RENDERED by type-agnostic paths, not just how its zone
		// was read above — so tag the timestamptz spelling. Same split as
		// tsZoneModeForType makes on the input side, one type name apart.
		if isTimestampTZTypeName(x.Type) {
			return NewTimestampTZDatum(t), nil
		}
		return NewTimeDatum(t), nil
	case "txid_snapshot", "pg_snapshot":
		// M0134-0080: mirrors parse_snapshot (xid8funcs.c) — xmin/xmax/xip
		// must all parse, xmin<=xmax, each xip in [xmin,xmax) and
		// non-decreasing (duplicates collapsed). The error always names
		// "pg_snapshot" regardless of which spelling was cast, matching
		// upstream's hardcoded errmsg.
		norm, ok := parsePgSnapshot(x.Value)
		if !ok {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type pg_snapshot: %q", x.Value)}
		}
		return NewStringDatum(norm), nil
	case "box":
		// M0134-0094: `box '...'` shares the same validate+canonicalize
		// chokepoint as a box(n) column's assignment coercion
		// (coerceTextLikeDatum, codec.go) — both call parseBoxLiteral.
		hx, hy, lx, ly, ok := parseBoxLiteral(x.Value)
		if !ok {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type box: %q", x.Value)}
		}
		return NewStringDatum(boxCanonicalText(hx, hy, lx, ly)), nil
	case "circle":
		// M0134-0098: `circle '...'` shares the same validate+canonicalize
		// chokepoint as a circle(n) column's assignment coercion
		// (coerceTextLikeDatum, codec.go) — both call parseCircleLiteral.
		cx, cy, r, ok := parseCircleLiteral(x.Value)
		if !ok {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type circle: %q", x.Value)}
		}
		return NewStringDatum(circleCanonicalText(cx, cy, r)), nil
	default:
		// Unknown type — treat as text literal. Covers enum/domain casts in v0.
		// M0097-0017: enum/domain type casts return the string value as-is.
		return NewStringDatum(x.Value), nil
	}
}

// roundNumericToInt rounds a KindNumeric datum using "round half away from zero"
// (PostgreSQL's numeric→integer rounding rule). M0097-0003.
//
// M0134-0069: unlike roundFloatToInt (whose source value is genuinely a
// float8/float4 and therefore already float-imprecise), a KindNumeric
// datum's mantissa+scale is exact, so bounds-checking must operate on the
// exact big.Int mantissa rather than round-tripping through float64 —
// a float64 bounds check silently accepts boundary values like
// -9223372036854775809 because strconv.ParseFloat rounds that exact string
// to precisely -9223372036854775808 (MinInt64) in IEEE double precision,
// letting the true overflow slip past an f<MinInt64 comparison. PG oracle:
// postgres/src/backend/utils/adt/numeric.c numericvar_to_int64 operates on
// the exact decimal digit array, never on a lossy float conversion.
func roundNumericToInt(d Datum, pos int) (int64, error) {
	scale := d.Scale
	if scale < 0 {
		scale = 0
	}
	var mantissa *big.Int
	if d.Flags&flagBigNumeric != 0 {
		mantissa = new(big.Int).Set(d.NumericBigValue())
	} else {
		mantissa = big.NewInt(d.Int)
	}
	if scale == 0 {
		if !mantissa.IsInt64() {
			// PG oracle: postgres/src/backend/utils/adt/numeric.c
			// numeric_int8_opt_error's ereport(bigint out of range) never
			// calls errposition() — runtime CAST-evaluation errors carry no
			// LINE/caret context regardless of literal-vs-column origin.
			// M0134-0070.
			return 0, &ExecError{Code: "22003", Message: "bigint out of range"}
		}
		return mantissa.Int64(), nil
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	quo, rem := new(big.Int).QuoRem(mantissa, divisor, new(big.Int))
	// Round half away from zero (PostgreSQL's numeric→integer rule):
	// bump the quotient away from zero when the remainder is at least half
	// the divisor.
	absRem := new(big.Int).Abs(rem)
	doubledRem := new(big.Int).Lsh(absRem, 1)
	if doubledRem.Cmp(divisor) >= 0 {
		if mantissa.Sign() >= 0 {
			quo.Add(quo, big.NewInt(1))
		} else {
			quo.Sub(quo, big.NewInt(1))
		}
	}
	if !quo.IsInt64() {
		// PG oracle: same numeric_int8_opt_error ereport as above, no
		// errposition(). M0134-0070.
		return 0, &ExecError{Code: "22003", Message: "bigint out of range"}
	}
	return quo.Int64(), nil
}

// roundFloatToInt rounds a KindNumeric or KindString datum using banker's rounding
// (round half to even) — PostgreSQL's float8/float4→integer rule. M0097-0003.
// KindString is handled for datums produced by the float8 arithmetic path. M0097-0042.
func roundFloatToInt(d Datum, pos int) (int64, error) {
	var text string
	if d.Kind == KindString {
		text = d.StringValue()
	} else {
		text = numericText(d)
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("invalid float value for integer cast: %s", text)}
	}
	// Check for NaN and out-of-range before converting to int64.
	if math.IsNaN(f) || math.IsInf(f, 0) || f > float64(math.MaxInt64) || f < float64(math.MinInt64) {
		return 0, &ExecError{Code: "22003", Pos: pos, Message: "bigint out of range"}
	}
	// Banker's rounding: round half to nearest even.
	rounded := math.RoundToEven(f)
	// Re-check after rounding (the rounded value could edge over MaxInt64).
	if rounded > float64(math.MaxInt64) || rounded < float64(math.MinInt64) {
		return 0, &ExecError{Code: "22003", Pos: pos, Message: "bigint out of range"}
	}
	return int64(rounded), nil
}

// roundFloat4ToInt rounds a KindNumeric or KindString datum that represents a float4
// value to int64, parsing via float32 precision (matching PostgreSQL's float4→int8 cast).
// The float4 source has already been rounded to float32 precision before storage as a
// KindNumeric string, so we parse via float32 to get the correct rounded value. M0097-0147.
func roundFloat4ToInt(d Datum, pos int) (int64, error) {
	var text string
	if d.Kind == KindString {
		text = d.StringValue()
	} else {
		text = numericText(d)
	}
	// Parse as float32 to honour float4 precision (PostgreSQL's float4 is IEEE 754 single).
	f32, err := strconv.ParseFloat(text, 32)
	if err != nil {
		return 0, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("invalid float value for integer cast: %s", text)}
	}
	f := float64(float32(f32))
	if math.IsNaN(f) || math.IsInf(f, 0) || f > float64(math.MaxInt64) || f < float64(math.MinInt64) {
		return 0, &ExecError{Code: "22003", Pos: pos, Message: "bigint out of range"}
	}
	rounded := math.RoundToEven(f)
	if rounded > float64(math.MaxInt64) || rounded < float64(math.MinInt64) {
		return 0, &ExecError{Code: "22003", Pos: pos, Message: "bigint out of range"}
	}
	return int64(rounded), nil
}

// datumToFloat64 converts any numeric Datum kind to float64.
// Returns (value, true) on success; (0, false) if conversion is not possible.
func datumToFloat64(d Datum) (float64, bool) {
	switch d.Kind {
	case KindInt:
		return float64(d.Int), true
	case KindNumeric:
		f, err := strconv.ParseFloat(d.Format(), 64)
		return f, err == nil
	case KindString:
		f, err := strconv.ParseFloat(strings.TrimSpace(d.StringValue()), 64)
		return f, err == nil
	}
	return 0, false
}

// isFloatSourceType reports whether a type name denotes a floating-point type
// (float4 / float8 / real / double precision). Used to select banker's rounding
// for float→integer casts. M0097-0003.
func isFloatSourceType(t string) bool {
	switch strings.ToLower(t) {
	case "float", "float4", "float8", "real", "double precision":
		return true
	}
	return false
}

// evalCastTyped is like evalCast but accepts the source type so it can select
// the correct rounding mode (banker's for float, away-from-zero for numeric).
// M0097-0003.
// roundNumericToScale rounds a Datum to the given decimal scale.
// Handles KindNumeric (int64 fast-path and big.Int), KindString, and KindInt.
func roundNumericToScale(d Datum, scale int16) Datum {
	switch d.Kind {
	case KindNumeric:
		curScale := d.NumericScaleValue()
		if curScale <= scale {
			// Already at or below target scale; no rounding needed but may need padding.
			// Re-format with exact scale for correct display.
			var s string
			if d.Flags&flagBigNumeric != 0 {
				s = formatNumericBig(big.NewInt(0).Set(d.NumericBigValue()), curScale)
			} else {
				s = formatNumeric(d.NumericMantissaValue(), curScale)
			}
			// Parse back at scale to add trailing zeros.
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return d
			}
			return NewStringDatum(strconv.FormatFloat(f, 'f', int(scale), 64))
		}
		// Need to reduce scale: convert to float64 and round.
		var s string
		if d.Flags&flagBigNumeric != 0 {
			s = formatNumericBig(big.NewInt(0).Set(d.NumericBigValue()), curScale)
		} else {
			s = formatNumeric(d.NumericMantissaValue(), curScale)
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return d
		}
		factor := math.Pow10(int(scale))
		return NewStringDatum(strconv.FormatFloat(math.Round(f*factor)/factor, 'f', int(scale), 64))
	case KindString:
		s := d.StringValue()
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return d
		}
		factor := math.Pow10(int(scale))
		return NewStringDatum(strconv.FormatFloat(math.Round(f*factor)/factor, 'f', int(scale), 64))
	case KindInt:
		return NewStringDatum(strconv.FormatFloat(float64(d.Int), 'f', int(scale), 64))
	}
	return d
}

// byteaIntSourceWidth returns the fixed byte width of the integer type named
// by typeName, or 0 if typeName is not an integer type. It accepts both the
// canonical short names (int2/int4/int8) and their SQL display spellings
// (smallint/integer/bigint): CastExpr.SourceType is stamped from the operand's
// declared catalog type name, which can be either spelling depending on where
// the operand came from (a CastExpr target uses the short form, a column ref
// uses whatever the catalog stores). M0134-0070.
func byteaIntSourceWidth(typeName string) int {
	switch strings.ToLower(typeName) {
	case "int2", "smallint":
		return 2
	case "int4", "integer", "int":
		return 4
	case "int8", "bigint":
		return 8
	}
	return 0
}

// byteaToIntN decodes a big-endian bytea payload to a signed integer of the
// given byte width, mirroring PG's bytea_int2/int4/int8
// (postgres/src/backend/utils/adt/varlena.c:4139-4211): MSB-first; a payload
// longer than the width raises 22003 with the width's type name in the message
// and NO errposition (the cast functions never call errposition, and
// strings.out's overflow rows expect no LINE/caret — same no-Pos convention as
// the numeric_int2 note above); short payloads are zero-extended. The unsigned
// accumulation is re-interpreted through the signed type so the min-value bit
// pattern wraps (e.g. \x8000 → -32768, \x8000000000000000 →
// -9223372036854775808). M0134-0070.
func byteaToIntN(b []byte, width int, typeName string) (int64, error) {
	if len(b) > width {
		return 0, &ExecError{Code: "22003", Message: typeName + " out of range"}
	}
	var u uint64
	for _, by := range b {
		u = u<<8 | uint64(by)
	}
	switch width {
	case 2:
		return int64(int16(u)), nil
	case 4:
		return int64(int32(u)), nil
	default:
		return int64(u), nil
	}
}

func evalCastTyped(d Datum, targetType, sourceType string, pos int, ctx *Context) (Datum, error) {
	if sourceType == "" {
		return evalCast(d, targetType, pos, ctx)
	}
	// M0134-0070: intN → bytea casts (pg_cast.dat:323-335, castfuncs
	// int2_bytea/int4_bytea/int8_bytea = varlena.c:4214-4233, each just the
	// corresponding intNsend) emit the big-endian two's-complement binary at
	// exactly the source type's fixed width — NOT a text rendering.
	// CastExpr.SourceType (stamped from the operand's declared type at
	// planner.go) supplies the width; evalCast has no source-type parameter.
	// A NULL (KindNull) or non-int datum falls through to evalCast's bytea
	// arm unchanged. Bare integer literals stamp int8 (goopg's exprType quirk;
	// PG would use int4 — the known literal-typing divergence documented for
	// to_hex at planner.go, out of scope here; the fixture spells widths
	// explicitly via ::intN).
	if strings.EqualFold(targetType, "bytea") && d.Kind == KindInt {
		switch byteaIntSourceWidth(sourceType) {
		case 2:
			b := make([]byte, 2)
			binary.BigEndian.PutUint16(b, uint16(int16(d.Int)))
			return NewBytesDatum(b), nil
		case 4:
			b := make([]byte, 4)
			binary.BigEndian.PutUint32(b, uint32(int32(d.Int)))
			return NewBytesDatum(b), nil
		case 8:
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(d.Int))
			return NewBytesDatum(b), nil
		}
	}
	// M0119-0006 (deferral row 1350): a reg* datum is a plain KindInt holding an
	// object OID, so casting one to a string type must render its OBJECT NAME via
	// the same RegOut the SELECT (internal/server/dispatch.go appendTypedCellText)
	// and COPY (datumToCopyText) wire paths use — not the raw numeric OID. This
	// guard is the cast path's third sibling of those renderers
	// (pattern_sibling_paths_must_agree); the reg*out family
	// (regclassout/regprocout/regprocedureout/regtypeout/regroleout/
	// regcollationout, postgres/src/backend/utils/adt/regproc.c) all render the
	// resolved name. regOut degrades to "-" for OID 0 and to the numeric OID for a
	// dangling/nil-catalog object, unchanged. `char` (CHAROID) is deliberately
	// excluded from the targets (charin/charout first-byte semantics).
	if isRegIdentifierTypeName(sourceType) && isStringTargetType(targetType) && d.Kind == KindInt {
		var cat catalog.Catalog
		var connDBOid []uint32
		if ctx != nil {
			cat = ctx.Catalog
			// Scope the relation lookup to the connection's own database
			// namespace (mirroring the `oid::regclass` CastExpr arm's connDBOid,
			// M0122-0007 4e follow-up 33) so an OID owned by ANOTHER database
			// never renders that database's relation name from this connection
			// (TestRegclassCastScopedToConnectionDBOid). Nil ctx keeps the empty
			// variadic → DefaultDBOid default, byte-identical to pre-scoping.
			connDBOid = []uint32{catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)}
		}
		return NewStringDatum(RegOut(sourceType, uint32(d.Int), cat, regCastQualify(ctx), connDBOid...)), nil
	}
	// For float8/float4 → integer casts, override the default (away-from-zero)
	// rounding inside evalCast to use banker's rounding instead.
	// Also handle KindString datums produced by the float8 arithmetic path
	// (e.g. "0.05" from random()*0.1). M0097-0042.
	// For float4 source, parse via float32 precision to match PostgreSQL semantics. M0097-0147.
	isFloat4Source := strings.ToLower(sourceType) == "float4" || strings.ToLower(sourceType) == "real"
	if isFloatSourceType(sourceType) && (d.Kind == KindNumeric || d.Kind == KindString) {
		roundFn := roundFloatToInt
		if isFloat4Source {
			roundFn = roundFloat4ToInt
		}
		intTarget := strings.ToLower(targetType)
		switch intTarget {
		case "int2", "smallint":
			n, err := roundFn(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -32768 || n > 32767 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "smallint out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case "int4", "integer", "int":
			n, err := roundFn(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -2147483648 || n > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case "int8", "bigint":
			n, err := roundFn(d, pos)
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		}
	}
	return evalCast(d, targetType, pos, ctx)
}

// isStringTargetType reports whether targetType is one of the string types a
// reg* value renders its object name as (deferral row 1350). `char` (CHAROID)
// is deliberately excluded — it keeps charin/charout first-byte semantics and
// is not named in the row.
func isStringTargetType(targetType string) bool {
	switch strings.ToLower(targetType) {
	case "text", "varchar", "name", "bpchar":
		return true
	}
	return false
}

// regCastQualify reports whether a reg*→string cast must schema-qualify object
// names, mirroring the SELECT wire path's publicSchemaVisible exactly
// (internal/server/dispatch.go): true when the `public` schema is not on the
// session's effective search_path (including pg_dump's search_path='').
// Nil ctx ⇒ false, matching the SELECT path's nil-getSetting behavior. Per-object
// qualification (deferral row 1347) stays out of scope.
func regCastQualify(ctx *Context) bool {
	if ctx == nil {
		return false
	}
	return !RegObjectSchemaVisible(ctx, "public")
}

// dateStyleFromCtx resolves the session's DateStyle GUC (style, order) via
// ctx.GetSetting, defaulting to ISO/MDY (PostgreSQL's boot default) when ctx
// is nil, has no GetSetting wired, or the GUC is unset. Mirrors the same
// resolution dispatch.go's appendTypedCellText performs for SELECT output, so
// CAST-to-text agrees with the SELECT-output path (pattern_sibling_paths_must_agree).
func dateStyleFromCtx(ctx *Context) (style, order string) {
	style, order = "ISO", "MDY"
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("datestyle"); ok {
			style, order = misc.ParseDateStyleValue(v)
		}
	}
	return style, order
}

// timeZoneFromCtx resolves the session's TimeZone GUC via ctx.GetSetting,
// returning "" (which config.FormatTimestampTZ reads as UTC) when ctx is nil,
// has no GetSetting wired, or the GUC is unset. Mirrors the same lookup
// dispatch.go's appendTypedCellText and copy_text.go's datumToCopyText perform
// for the timestamptz arm, so CAST-to-text agrees with SELECT and COPY output
// on the TimeZone axis too (pattern_sibling_paths_must_agree). M0119-0006.
func timeZoneFromCtx(ctx *Context) string {
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("timezone"); ok {
			return v
		}
	}
	return ""
}

// formatTimeDatumDateStyle renders a non-time-only KindTime datum as text,
// honoring the session DateStyle and TimeZone GUCs. Mirrors Datum.Format()'s
// ±infinity and TimeSub branching, but dispatches DATE / TIMESTAMP /
// TIMESTAMPTZ through config.FormatDate/FormatTimestamp/FormatTimestampTZ
// instead of Format()'s hardcoded Postgres-MDY-only / fixed-ISO layouts, so
// callers (CAST-to-text, FK violation DETAIL messages, ...) agree with
// SELECT/COPY output.
//
// M0119-0006 (40th slice): the TimeSubTimestampTZ arm closes the residual the
// 39th slice left. That slice fixed the two output paths that know the DECLARED
// column type (dispatch.go's appendTypedCellText, copy_text.go's
// datumToCopyText) but could not fix this one, which sees a bare Datum — so
// `('2020-01-01 10:00:00+05:30'::timestamptz)::text` printed no zone AND never
// left UTC, disagreeing with goopg's own SELECT output of the same value.
// Under a non-UTC TimeZone that is a silent relabel of the instant, the exact
// failure mode the 39th slice removed one path over. The producers now tag the
// datum (NewTimestampTZDatum), so the discriminator this needs exists.
func formatTimeDatumDateStyle(d Datum, style, order, zone string) string {
	if d.Int == math.MaxInt64 {
		return "infinity"
	}
	if d.Int == math.MinInt64 {
		return "-infinity"
	}
	switch d.TimeSub {
	case TimeSubDate:
		return misc.FormatDate(d.TimeValue(), style, order)
	case TimeSubTimestampTZ:
		return misc.FormatTimestampTZ(d.TimeValue(), style, order, zone)
	}
	return misc.FormatTimestamp(d.TimeValue(), style, order)
}

// formatDatumDateStyle is Datum.Format() with DateStyle-aware KindTime
// rendering: a DATE/TIMESTAMP/TIMESTAMPTZ datum honors the session's
// datestyle GUC (via ctx), every other Kind falls back to Format()
// unchanged. Pass nil ctx where no session is reachable (defaults to
// ISO/MDY, matching Format()'s pre-existing hardcoded behavior).
func formatDatumDateStyle(d Datum, ctx *Context) string {
	if d.Kind != KindTime {
		return d.Format()
	}
	style, order := dateStyleFromCtx(ctx)
	return formatTimeDatumDateStyle(d, style, order, timeZoneFromCtx(ctx))
}

// CoerceParamToDeclaredType applies the declared parameter type from a SQL
// PREPARE/EXECUTE to a supplied argument, mirroring
// postgres/src/backend/commands/prepare.c:EvaluateParams, which runs
// coerce_to_target_type(..., COERCION_ASSIGNMENT, COERCE_IMPLICIT_CAST, -1)
// over every supplied expression against pstmt->argtypes[i]. Without this,
// goopg only validated argument compatibility (execParamTypeIncompatible)
// and then bound the raw literal datum, so e.g. a regclass[] parameter kept
// its KindString and every OID comparison in the plan silently evaluated
// false. targetType is normalised here (quotes stripped, lower-cased,
// typmod parenthesis dropped, trailing "[]" kept) so callers may pass the
// declared spelling straight from parser.PrepareStmt.ParamTypes or
// normPrepParamType's output — this function is a thin wrapper and delegates
// all actual coercion to evalCast; it must not duplicate cast logic.
//
// One exception: a scalar (non-array) reg* target — e.g. `PREPARE
// t(regclass) AS ...; EXECUTE t('tbl')` — is resolved directly via
// regIdentifierInput (the same shared primitive evalCast's reg*[] array
// arm already calls per-element) rather than through evalCast. evalCast's
// own "regclass" case is deliberately a KindInt-only pass-through for
// KindString (see its comment): scalar `'name'::regclass` name→OID
// resolution normally happens inline in evalExprSlot's *optimizer.CastExpr
// arm (which is catalog/dbOid-aware) or evalFuncCall's reg* arm — neither
// of which a bound EXECUTE parameter ever passes through, since the
// parameter value never becomes a CastExpr/FuncCall AST node. Resolving it
// here (not by widening evalCast's own regclass case) keeps every other
// evalCast caller's existing regclass behavior byte-identical — widening
// evalCast's case regressed TestRegCastToStringRendersName's pinned
// bare-catalog literals (a test fixture where system tables are
// deliberately not name-resolvable, discovered and reverted during
// M0134-0005a). M0134-0005a.
func CoerceParamToDeclaredType(d Datum, targetType string, ctx *Context) (Datum, error) {
	if targetType == "" || d.IsNull() {
		return d, nil
	}
	t := strings.TrimSpace(targetType)
	t = strings.Trim(t, `"`)
	t = strings.ToLower(t)
	isArray := strings.HasSuffix(t, "[]")
	if isArray {
		t = strings.TrimSuffix(t, "[]")
	}
	// Drop a typmod parenthesis, e.g. "varchar(10)" -> "varchar",
	// "numeric(10,2)" -> "numeric". evalCast dispatches on the bare type
	// name; typmod-specific range/precision enforcement is out of scope
	// for parameter coercion (matches EvaluateParams, which coerces
	// against the type OID, not the typmod, for this path).
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	if !isArray && d.Kind == KindString && ctx != nil && isRegIdentifierTypeName(t) {
		return regIdentifierInput(d, t, ctx, 0)
	}
	if isArray {
		t += "[]"
	}
	return evalCast(d, t, 0, ctx)
}

// evalCast coerces datum d to the declared SQL type name.
// Handles: string→bool, bool→text, int→text, int→int2 (range check),
// string→int2/4/8 (via parseIntegerInput). Pass-through for unknown types.
// ctx supplies session-GUC reachability (currently: DateStyle for
// timestamp/date → text casts); pass nil where no session context is
// available (behavior falls back to ISO/MDY, matching the pre-existing
// hardcoded default). M0097-0003.
func evalCast(d Datum, targetType string, pos int, ctx *Context) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch strings.ToLower(targetType) {
	case "bool", "boolean":
		switch d.Kind {
		case KindBool:
			return d, nil
		case KindString:
			if b, ok := pgBoolIn(d.StringValue()); ok {
				return NewBoolDatum(b), nil
			}
			return Datum{}, &ExecError{Code: "22P02", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type boolean: %q", d.StringValue())}
		case KindInt:
			return NewBoolDatum(d.Int != 0), nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to bool"}
		}
	case "int2", "smallint":
		switch d.Kind {
		case KindInt:
			if d.Int < -32768 || d.Int > 32767 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "smallint out of range"}
			}
			return d, nil
		case KindBytes:
			// bytea_int2 (postgres/src/backend/utils/adt/varlena.c:4139):
			// big-endian MSB-first; len > 2 → 22003 with no errposition;
			// short payloads zero-extended; uint→signed re-interpretation
			// wraps the min-value pattern (\x8000 → -32768). M0134-0070.
			n, err := byteaToIntN(d.BytesValue(), 2, "smallint")
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindString:
			s := d.StringValue()
			// Array literals like '{1,2,3}'::int2[] pass through — the parser strips '[]'
			// making the target type look like 'int2', but the value is an array. M0097-0063.
			if len(s) > 0 && s[0] == '{' {
				return d, nil
			}
			n, err := parseIntegerInput(s, "smallint", 16)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			// Float/numeric → int2: round to nearest even (banker's rounding). M0097-0003.
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -32768 || n > 32767 {
				// PG oracle: postgres/src/backend/utils/adt/numeric.c
				// numeric_int2's ereport(smallint out of range) never calls
				// errposition(). M0134-0070.
				return Datum{}, &ExecError{Code: "22003", Message: "smallint out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to smallint"}
		}
	case "int4", "integer", "int":
		switch d.Kind {
		case KindInt:
			if d.Int < -2147483648 || d.Int > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
			}
			return d, nil
		case KindBytes:
			// bytea_int4 (postgres/src/backend/utils/adt/varlena.c:4166):
			// big-endian MSB-first; len > 4 → 22003 with no errposition;
			// short payloads zero-extended; uint→signed re-interpretation
			// wraps the min-value pattern (\x80000000 → -2147483648).
			// M0134-0070.
			n, err := byteaToIntN(d.BytesValue(), 4, "integer")
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindString:
			s := d.StringValue()
			// Array literals like '{1,2,3}'::int4[] pass through — the parser strips '[]'
			// making the target type look like 'int4', but the value is an array. M0097-0063.
			if len(s) > 0 && s[0] == '{' {
				return d, nil
			}
			n, err := parseIntegerInput(s, "integer", 32)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -2147483648 || n > 2147483647 {
				// PG oracle: postgres/src/backend/utils/adt/numeric.c
				// numeric_int4_opt_error's ereport(integer out of range)
				// never calls errposition(). M0134-0070.
				return Datum{}, &ExecError{Code: "22003", Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to integer"}
		}
	case "int8", "bigint":
		switch d.Kind {
		case KindInt:
			return d, nil
		case KindBytes:
			// bytea_int8 (postgres/src/backend/utils/adt/varlena.c:4193):
			// big-endian MSB-first; len > 8 → 22003 with no errposition;
			// short payloads zero-extended; uint64→int64 re-interpretation
			// wraps the min-value pattern
			// (\x8000000000000000 → -9223372036854775808). M0134-0070.
			n, err := byteaToIntN(d.BytesValue(), 8, "bigint")
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindString:
			s := d.StringValue()
			// Array literals like '{1,2,3}'::int8[] pass through — parser strips '[]'. M0097-0063.
			if len(s) > 0 && s[0] == '{' {
				return d, nil
			}
			n, err := parseIntegerInput(s, "bigint", 64)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to bigint"}
		}
	case "name":
		// name type truncates to NAMEDATALEN-1 = 63 bytes.
		// For text[] values (e.g. from parse_ident()), truncate each array element. M0097-0003.
		switch d.Kind {
		case KindString:
			s := d.StringValue()
			// If the value looks like a PostgreSQL array ({elem1,elem2,...}), process as array.
			if len(s) > 0 && s[0] == '{' && s[len(s)-1] == '}' {
				elems := parseTextArray(s)
				for i, e := range elems {
					if len(e) > 63 {
						elems[i] = e[:63]
					}
				}
				return NewStringDatum(formatTextArray(elems)), nil
			}
			// Single value: truncate.
			if len(s) > 63 {
				s = s[:63]
			}
			return NewStringDatum(s), nil
		default:
			return d, nil
		}
	case "name[]":
		// name[] cast: truncate each array element to NAMEDATALEN-1 = 63 bytes.
		// The parser preserves "[]" in TargetType for ::name[] casts; this case
		// handles the array form so element truncation is applied. M0097-name-fix.
		s := d.StringValue()
		if len(s) > 0 && s[0] == '{' && s[len(s)-1] == '}' {
			elems := parseTextArray(s)
			for i, e := range elems {
				if len(e) > 63 {
					elems[i] = e[:63]
				}
			}
			return NewStringDatum(formatTextArray(elems)), nil
		}
		return d, nil
	case "regclass[]", "regproc[]", "regprocedure[]", "regtype[]", "regrole[]",
		"regcollation[]", "regnamespace[]", "regdictionary[]":
		// reg*[] cast: resolve each array-literal element to its OID via the
		// SAME regIdentifierInput the scalar reg* casts (case "regclass" below
		// delegates name→OID resolution to evalExprSlot's CastExpr arm, which
		// calls the equivalent catalog lookups) and the heap encode path
		// (codec_array.go's encodeArrayElem) already use — parseRegDashOrOid
		// ("-"/numeric) first, then the per-type catalog lookup, propagating a
		// miss as the type's own undefined-object SQLSTATE rather than
		// swallowing it. Before this arm the switch had no case for any
		// reg*-array type, so control fell through to the unknown-type
		// pass-through at the end of this function and the literal stayed raw,
		// unresolved text — every downstream `conrelid = ANY('{tbl}'::regclass[])`
		// silently evaluated false instead of erroring or matching (M0134-0005
		// S07; S06 diagnosis). Mirrors the "name[]" arm's shape immediately
		// above (parseTextArray → per-element transform → formatTextArray).
		// regnamespace/regdictionary have no name-resolution seam yet
		// (reg_identifier.go's file-level comment) — they still pass through
		// unresolved here, matching the scalar family's existing documented
		// limitation, not a regression introduced by this arm.
		s := d.StringValue()
		if len(s) == 0 || s[0] != '{' || s[len(s)-1] != '}' {
			return d, nil
		}
		elemType := strings.TrimSuffix(strings.ToLower(targetType), "[]")
		elems := parseTextArray(s)
		resolved := make([]string, len(elems))
		for i, e := range elems {
			rd, err := regIdentifierInput(NewStringDatum(e), elemType, ctx, pos)
			if err != nil {
				return Datum{}, err
			}
			if rd.Kind == KindInt {
				resolved[i] = strconv.FormatInt(rd.Int, 10)
			} else {
				resolved[i] = rd.StringValue()
			}
		}
		return NewStringDatum(formatTextArray(resolved)), nil
	case "bytea":
		// byteain (postgres/src/backend/utils/adt/varlena.c). An unknown-type
		// literal and an explicitly-typed text value both reach this arm as
		// KindString and PG treats them identically — `'\xaa'::text::bytea`
		// and `'\xaa'::bytea` are both the single byte 0xAA. M0125-0021.
		switch d.Kind {
		case KindBytes:
			return d, nil
		case KindString:
			b, err := byteaIn(d.StringValue(), pos)
			if err != nil {
				return Datum{}, err
			}
			return NewBytesDatum(b), nil
		default:
			return Datum{}, &ExecError{Code: "42846", Pos: pos,
				Message: fmt.Sprintf("cannot cast type %s to bytea", pgKindTypeName(d.Kind))}
		}
	case "text", "varchar", "bpchar", "char":
		switch d.Kind {
		case KindBytes:
			// byteaout: a bytea cast to text is the escape string the
			// session's `bytea_output` GUC selects (hex's `\x…` by default),
			// NOT the raw payload bytes. M0125-0021, M0134-0001 S12.
			return NewStringDatum(byteaOutMode(d.BytesValue(), byteaOutputModeFromCtx(ctx))), nil
		case KindBool:
			if d.BoolValue() {
				return NewStringDatum("true"), nil
			}
			return NewStringDatum("false"), nil
		case KindInt:
			return NewStringDatum(strconv.FormatInt(d.Int, 10)), nil
		case KindTime:
			if isTimeOnlyValue(d.TimeValue()) {
				return NewStringDatum(string(appendTimeOnlyValueText(nil, d.TimeValue()))), nil
			}
			style, order := dateStyleFromCtx(ctx)
			return NewStringDatum(formatTimeDatumDateStyle(d, style, order, timeZoneFromCtx(ctx))), nil
		case KindEnum:
			// Cast enum to text: return the label string (loses sort order). M0097-enum.
			return NewStringDatum(string(d.Buf)), nil
		case KindString:
			s := d.StringValue()
			// For "char" (internal 1-byte type), interpret backslash-octal escapes
			// and return the charout display form. PostgreSQL's charin() accepts
			// \NNN and charout() formats non-printable bytes as \NNN. M0097-0003.
			if targetType == "char" {
				if b, ok := charTypeParseOctalEscape(s); ok {
					return NewStringDatum(charTypeDisplayForm(b)), nil
				}
				// PostgreSQL's charin() takes the first byte of any non-\NNN
				// input and silently discards the rest (char.c). M0122-0005.
				var b byte
				if len(s) > 0 {
					b = s[0]
				}
				return NewStringDatum(charTypeDisplayForm(b)), nil
			}
			return d, nil
		default:
			return d, nil
		}
	case "float4", "real":
		// Integer → float4: round-trip through float32 to apply float32 precision.
		// PostgreSQL float4 has ~7 significant decimal digits; full int64 precision
		// must be lost before display. M0097-0147.
		if d.Kind == KindInt {
			f32 := float32(d.Int)
			f64 := float64(f32)
			normalized := strconv.FormatFloat(f64, 'f', -1, 64)
			if v, s, ok := parseNumericFast(normalized); ok {
				return Datum{Kind: KindNumeric, Int: v, Scale: s}, nil
			}
			m, s, parseErr := parseNumeric(normalized)
			if parseErr != nil {
				return numericFromInt(d.Int), nil // unexpected, keep original
			}
			return newNumeric(m, int(s)), nil
		}
		// Normalize KindNumeric through float64 to strip trailing zeros (0.0→0). M0097-0003.
		if d.Kind == KindNumeric {
			text := numericText(d)
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type float8: %q", text)}
			}
			normalized := strconv.FormatFloat(f, 'f', -1, 64)
			if v, s, ok := parseNumericFast(normalized); ok {
				return Datum{Kind: KindNumeric, Int: v, Scale: s}, nil
			}
			m, s, parseErr := parseNumeric(normalized)
			if parseErr != nil {
				return d, nil // unexpected, keep original
			}
			return newNumeric(m, int(s)), nil
		}
		return d, nil
	case "float", "float8", "double precision":
		// Normalize KindNumeric through float64 to strip trailing zeros (0.0→0). M0097-0003.
		// PostgreSQL float8out uses printf-style format that removes trailing zeros.
		if d.Kind == KindNumeric {
			text := numericText(d)
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type float8: %q", text)}
			}
			normalized := strconv.FormatFloat(f, 'f', -1, 64)
			if v, s, ok := parseNumericFast(normalized); ok {
				return Datum{Kind: KindNumeric, Int: v, Scale: s}, nil
			}
			m, s, parseErr := parseNumeric(normalized)
			if parseErr != nil {
				return d, nil // unexpected, keep original
			}
			return newNumeric(m, int(s)), nil
		}
		// Integer → float8: promote to KindNumeric so float arithmetic applies.
		// Without this, float8(count(*)) / scalar_int uses integer division (→ 0).
		if d.Kind == KindInt {
			return numericFromInt(d.Int), nil
		}
		return d, nil
	case "oid":
		switch d.Kind {
		case KindInt:
			if d.Int < 0 || d.Int > 4294967295 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "OID out of range"}
			}
			return d, nil
		case KindString:
			n, err := parseIntegerInput(d.StringValue(), "oid", 64)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			if n < 0 || n > 4294967295 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "OID out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return d, nil
		}
	case "xid", "xid8":
		// M0134-0087 (xid.sql sizing): evalCast had NO case for xid/xid8 at
		// all, so `'…'::xid`/`'…'::xid8` (and an implicit cast into an
		// xid/xid8 COLUMN, e.g. `INSERT INTO t(x xid8) VALUES ('0x2a')`) fell
		// through to the bottom default arm and returned the input datum
		// UNCHANGED — no octal/hex parsing, no range validation, no 22P02 on
		// garbage input ('asdf'::xid silently "succeeded" as the string
		// "asdf"). This mirrors the CastExpr-vs-TypedStringLit split the
		// pattern_sibling_paths_must_agree memory warns about: the `xid
		// '42'` TypedStringLit form (evalTypedStringLit, above) already had
		// working parseXid/parseXid8 logic; `'42'::xid` (CastExpr, this
		// function) did not share it.
		bits := 32
		typeName := "xid"
		wrapped := int64(uint32(0xffffffff)) // -1 special case, 32-bit
		if targetType == "xid8" {
			bits = 64
			typeName = "xid8"
			wrapped = -1 // int64(-1) bit-for-bit equals uint64 2^64-1
		}
		switch d.Kind {
		case KindInt:
			if bits == 32 {
				// xid8::xid truncates to the low 32 bits (xid8toxid /
				// XidFromFullTransactionId, postgres/src/backend/utils/adt/xid.c:187-191).
				return Datum{Kind: KindInt, Int: int64(uint32(uint64(d.Int)))}, nil
			}
			return d, nil
		case KindString:
			v := strings.TrimSpace(d.StringValue())
			if v == "-1" {
				return Datum{Kind: KindInt, Int: wrapped}, nil
			}
			var n uint64
			var err error
			if bits == 32 {
				var n32 uint32
				n32, err = parseXid(v)
				n = uint64(n32)
			} else {
				n, err = parseXid8(v)
			}
			if err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, d.StringValue())}
			}
			return Datum{Kind: KindInt, Int: int64(n)}, nil
		default:
			return d, nil
		}
	case "pg_lsn":
		switch d.Kind {
		case KindString:
			u, err := parsePgLSN(d.StringValue())
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return NewStringDatum(formatPgLSN(u)), nil
		case KindInt:
			return NewStringDatum(formatPgLSN(uint64(d.Int))), nil
		default:
			return NewStringDatum(d.Format()), nil
		}
	case "regclass":
		// `oid::regclass` renders as the relation name (matches PG's
		// regclassout). The catalog lookup happens at evalFuncCall's
		// `regclass` arm for string→OID; here we cover the
		// CastExpr path used by `tableoid::regclass` for the
		// `tableoid` system column. KindInt input is the OID; we
		// resolve via the executor's catalog (see CastExpr eval-site
		// note below) and emit the qualified relname as a string.
		// String input (e.g. `'pg_class'::regclass`) is delegated to
		// the function-call path which already handles it.
		// M0100-0005y.
		if d.Kind == KindInt {
			// The catalog isn't reachable from evalCast's signature;
			// stash the OID as KindRegClass so the wire formatter (or
			// upstream evalCastTyped wrapper) can render it. Until
			// formatter support lands, return the raw integer — the
			// CastExpr operand path in evalExprSlot will route through
			// evalRegclassCast for the tableoid OID lookup.
			return d, nil
		}
		return d, nil
	case "date":
		// Cast to date: truncate KindTime to midnight UTC, parse strings as dates. M0097-0004.
		if d.Kind == KindString {
			s := d.StringValue()
			// 'infinity' / '-infinity' → DATEVAL_NOEND / NOBEGIN (#5(d-iv)).
			if inf, ok := parseDateInfinityLiteral(s); ok {
				return inf, nil
			}
			// date_in decodes a zone field and then ignores it, so the day comes
			// from the wall clock as written: '2020-01-02 02:00:00+05:30'::date
			// is 2020-01-02, not the previous day (tsZoneMode). It likewise never
			// composes date with time, so an hour-24 / leap-second day carry is
			// dropped too — '2020-01-01 24:00:00'::date is the 1st.
			t, err := parseDateInputText(s)
			if err != nil {
				// M0119-0006: a range failure (no year zero, or a value the
				// KindTime carrier cannot hold) keeps its own 22008 wording —
				// reporting it as a syntax error points the user at the
				// spelling, which is not what is wrong with it.
				return Datum{}, dateTimeInputError(err, "date", s, pos)
			}
			t2 := t.UTC()
			return NewDateDatum(time.Date(t2.Year(), t2.Month(), t2.Day(), 0, 0, 0, 0, time.UTC)), nil
		}
		if d.Kind == KindTime {
			// A ±infinity timestamp/date sentinel casts to the same-signed date
			// infinity (PG timestamp2date / date passthrough). (#5(d-iv))
			if d.IsTimestampNotFinite() {
				return NewDateInfinity(d.IsTimestampPosInf()), nil
			}
			t := d.TimeValue().UTC()
			return NewDateDatum(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)), nil
		}
		return d, nil
	case "time":
		// Cast to time: extract time-of-day from KindTime, parse strings. M0097-0004.
		if d.Kind == KindString {
			ts, err := parseTimeString(d.StringValue())
			if err != nil {
				return Datum{}, err
			}
			return NewTimeDatum(ts), nil
		}
		if d.Kind == KindTime {
			t := d.TimeValue().UTC()
			// Re-anchor to epoch to strip any date component.
			return NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)), nil
		}
		return d, nil
	case "timetz":
		// Cast to timetz: parse strings with timezone offset. M0097-0004.
		if d.Kind == KindString {
			ts, offsetSecs, err := parseTimeTZString(d.StringValue(), timeZoneFromCtx(ctx))
			if err != nil {
				return Datum{}, err
			}
			return NewTimeTZDatum(ts, offsetSecs), nil
		}
		if d.Kind == KindTime {
			t := d.TimeValue().UTC()
			// Re-anchor to epoch to strip any date component; preserve stored offset.
			return NewTimeTZDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC), d.TimeTZOffsetSecs()), nil
		}
		return d, nil
	case "timestamp", "timestamptz":
		// Cast to timestamp: parse strings, retag KindTime. M0097-0004.
		tz := isTimestampTZTypeName(targetType)
		if d.Kind == KindString {
			// 'infinity' / '-infinity' have no finite time.Time (#5(d-iv)).
			if inf, ok := parseTimestampInfinityLiteral(d.StringValue()); ok {
				return inf, nil
			}
			// `::timestamp` discards a zone the text carries, `::timestamptz`
			// applies it (tsZoneMode). M0134-0026: a zone-less `::timestamptz`
			// reads the wall clock as local time in the session TimeZone GUC
			// (DecodeDateTime, datetime.c:1573-1583) — this arm also covers a
			// bound extended-protocol text parameter coerced to timestamptz
			// (CoerceParamToDeclaredType → evalCast), so timeZoneFromCtx(ctx)
			// must be threaded through here too, not just the typed-literal
			// arm (evalTypedStringLit). No caching hazard here: evalCast does
			// not cache its result on a plan node.
			ts, err := parseCopyTimestampZoneSession(d.StringValue(), tsZoneModeForType(targetType), timeZoneFromCtx(ctx))
			if err != nil {
				return Datum{}, dateTimeInputError(err, "timestamp", d.StringValue(), pos)
			}
			if tz {
				return NewTimestampTZDatum(ts), nil
			}
			return NewTimeDatum(ts), nil
		}
		// M0119-0006 (40th slice): a cast BETWEEN the two timestamp types is not
		// a no-op in either half. Upstream timestamp2timestamptz reads the
		// zone-less wall clock as a LOCAL time in the session zone (so the
		// stored instant moves), and timestamptz2timestamp renders the instant
		// into the session zone and keeps that wall clock; the subtype must move
		// with it, or `ts_col::timestamptz` renders zone-less and
		// `tstz_col::timestamp` keeps a zone the target type does not have.
		// goopg previously returned the datum untouched, which is the identity
		// only while TimeZone is UTC — under any other zone both directions were
		// silently off by the offset with no diagnostic.
		//
		// The ±infinity sentinels render from Int alone (their wall clock is not
		// a real instant), so leave them untouched — as are DATE and the two
		// time-of-day subtypes, whose carriers mean something else (see
		// NewTimeTZDatum's offset in Datum.Scale).
		if d.Kind == KindTime &&
			(d.TimeSub == TimeSubTimestamp || d.TimeSub == TimeSubTimestampTZ) &&
			d.Int != math.MaxInt64 && d.Int != math.MinInt64 {
			zone := timeZoneFromCtx(ctx)
			switch {
			case tz && d.TimeSub == TimeSubTimestamp:
				return NewTimestampTZDatum(misc.TimestampToTimestampTZ(d.TimeValue(), zone)), nil
			case !tz && d.TimeSub == TimeSubTimestampTZ:
				return NewTimeDatum(misc.TimestampTZToTimestamp(d.TimeValue(), zone)), nil
			}
		}
		return d, nil
	case "interval":
		// Cast to interval: parse the v0-supported "<n> <unit>" string shape
		// (unit ∈ day(s)/month(s)/year(s) or the sub-day
		// hour(s)/minute(s)/second(s)/millisecond(s)), mirroring the
		// `INTERVAL '<n> <unit>'` typed-literal grammar
		// (evalIntervalLit/splitEmbeddedInterval) so `'<n> <unit>'::interval`
		// and `CAST('<n> <unit>' AS interval)` accept exactly the same
		// strings instead of silently passing the string through unparsed.
		// Multi-component interval strings (`1 day 05:00:00`) and fractional
		// magnitudes remain a documented v0 scope limit. M0122-0004;
		// sub-day units unimplemented_feat #5.
		if d.Kind == KindInterval {
			return d, nil
		}
		if d.Kind == KindString {
			months, days, micros, ok := parseIntervalCastString(d.StringValue())
			if !ok {
				return Datum{}, &ExecError{Code: "22007", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type interval: %q", d.StringValue())}
			}
			return NewIntervalDatumFull(months, days, micros), nil
		}
		return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to interval"}
	case "tid":
		// Cast to tid: parse/validate "(block,offset)" and re-emit the
		// canonical form. PostgreSQL's tidin treats block as an unsigned
		// 32-bit BlockNumber (so '(-1,0)' normalises to '(4294967295,0)')
		// and offset as an unsigned 16-bit OffsetNumber. M0097-0036.
		if d.Kind == KindString {
			block, offset, ok := parseTidInput(d.StringValue())
			if !ok {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type tid: %q", d.StringValue())}
			}
			return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
		}
		return d, nil
	case "txid_snapshot", "pg_snapshot":
		// M0134-0080: `::txid_snapshot`/`::pg_snapshot` and CAST(...) route
		// here (evalCastTyped falls back to evalCast); the `txid_snapshot
		// '...'` typed-literal spelling has its own arm in
		// evalTypedStringLit, kept in sync with this one. Mirrors
		// parse_snapshot (xid8funcs.c) — see parsePgSnapshot.
		if d.Kind == KindString {
			norm, ok := parsePgSnapshot(d.StringValue())
			if !ok {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type pg_snapshot: %q", d.StringValue())}
			}
			return NewStringDatum(norm), nil
		}
		return d, nil
	case "numeric", "decimal":
		// Cast to numeric: validate string inputs, pass through numeric/int as-is.
		// M0097-0056: prevents 'foo'::numeric from succeeding silently.
		switch d.Kind {
		case KindNumeric:
			return d, nil
		case KindInt:
			return numericFromInt(d.Int), nil
		case KindString:
			s := strings.TrimSpace(d.StringValue())
			// NaN and Infinity (including abbreviated forms) are valid numeric special values.
			// Normalize to canonical capitalization so applyAgg's switch can match them.
			if strings.EqualFold(s, "nan") {
				return NewStringDatum("NaN"), nil
			}
			if strings.EqualFold(s, "inf") || strings.EqualFold(s, "infinity") ||
				strings.EqualFold(s, "+inf") || strings.EqualFold(s, "+infinity") {
				return NewStringDatum("Infinity"), nil
			}
			if strings.EqualFold(s, "-inf") || strings.EqualFold(s, "-infinity") {
				return NewStringDatum("-Infinity"), nil
			}
			_, _, err := parseNumeric(s)
			if err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type numeric: %q", d.StringValue())}
			}
			// Re-use the string datum rather than allocating a big.Int when fast
			// path would suffice; the string form is already the canonical form.
			return d, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos,
				Message: fmt.Sprintf("cannot cast type %v to numeric", d.Kind)}
		}
	case "jsonb":
		// `::jsonb` canonicalises the text the same way a jsonb column does
		// (coerceTextLikeDatum's jsonb arm) — the two are the twin input
		// boundaries for jsonb and must agree (Hard-won Rule #2). `json` is
		// untouched: it preserves the input spelling. M0119-0006 (64th slice).
		if s, ok := datumAsString(d); ok {
			canon, err := canonicalizeJSONB(s)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return NewStringDatum(canon), nil
		}
		return d, nil
	case "json":
		// `::json` preserves the input spelling verbatim (no re-serialisation,
		// unlike `::jsonb` above) but still validates JSON syntax at the input
		// boundary — coerceTextLikeDatum's `json` column-storage path must
		// agree (Hard-won Rule #2). Unlike most 22P02 casts, PG's json parser
		// reports this via DETAIL/CONTEXT (json_ereport_error), never a LINE/^
		// position marker — so, unlike the numeric/int2/etc. arms above,
		// ee.Pos is deliberately left at its zero value here (the wire layer
		// only emits LINE when Pos > 0). M0134-0120.
		if s, ok := datumAsString(d); ok {
			if err := validateJSONText(s); err != nil {
				return Datum{}, err
			}
		}
		return d, nil
	}
	return d, nil // pass-through for unknown types
}

// cStrtoul10Full emulates C strtoul(s, &end, 10) followed by PostgreSQL's
// "fully consumed" check used in tidin: it skips leading C whitespace, accepts
// an optional +/- sign, then base-10 digits, and requires the digits to run to
// the end of s (so any trailing junk before the delimiter is rejected, matching
// "*badp != DELIM"). Negative inputs wrap modulo 2^64 like C unsigned
// arithmetic. ok is false when no digits were present or trailing junk remains;
// overflow is true when the magnitude exceeds 64 bits (C would set ERANGE).
func cStrtoul10Full(s string) (val uint64, ok bool, overflow bool) {
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			i++
			continue
		}
		break
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var v uint64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		nv := v*10 + uint64(s[i]-'0')
		if nv < v {
			overflow = true
		}
		v = nv
		i++
	}
	if i == start || i != len(s) {
		return 0, false, false
	}
	if neg {
		v = -v // two's-complement negation modulo 2^64
	}
	return v, true, overflow
}

// parseTidInput parses a tid external representation "(block,offset)" exactly
// as PostgreSQL's tidin (src/backend/utils/adt/tid.c): block is a BlockNumber
// (uint32) accepted via strtoul with the wider-than-32-bit round-trip guard
// (so '-1' → 4294967295 but '4294967296' is rejected), and offset is an
// OffsetNumber (uint16) bounded by USHRT_MAX. Returns ok=false on any malformed
// or out-of-range input. M0097-0036.
func parseTidInput(str string) (block uint32, offset uint16, ok bool) {
	lp := strings.IndexByte(str, '(')
	if lp < 0 {
		return 0, 0, false
	}
	rest := str[lp+1:]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return 0, 0, false
	}
	offPart := rest[comma+1:]
	rp := strings.IndexByte(offPart, ')')
	if rp < 0 {
		return 0, 0, false
	}

	bcvt, bok, bovf := cStrtoul10Full(rest[:comma])
	if !bok || bovf {
		return 0, 0, false
	}
	block = uint32(bcvt)
	// PG's SIZEOF_LONG > 4 guard: accept only values that round-trip through
	// either the unsigned or sign-extended 32-bit truncation.
	if bcvt != uint64(block) && bcvt != uint64(int64(int32(block))) {
		return 0, 0, false
	}

	ocvt, ook, oovf := cStrtoul10Full(offPart[:rp])
	if !ook || oovf || ocvt > 65535 {
		return 0, 0, false
	}
	return block, uint16(ocvt), true
}

// parseXid parses an xid value (unsigned 32-bit). Accepts decimal, octal (0NNN), hex (0xNNN).
// M0097-0018.
func parseXid(s string) (uint32, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseUint(s[2:], 16, 32)
		return uint32(n), err
	}
	if len(s) > 1 && s[0] == '0' {
		n, err := strconv.ParseUint(s[1:], 8, 32)
		return uint32(n), err
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err
}

// parseXid8 parses an xid8 value (unsigned 64-bit). Accepts decimal, octal
// (0NNN), hex (0xNNN) — matching xid8in's uint64in_subr, which calls
// strtou64(s, &endptr, 0) (base 0 = C's octal/hex/decimal auto-detect,
// postgres/src/backend/utils/adt/numutils.c:985-992). M0097-0018;
// octal support added M0134-0087 (parseXid already had it; this was the
// sibling gap — pattern_sibling_paths_must_agree).
func parseXid8(s string) (uint64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	if len(s) > 1 && s[0] == '0' {
		return strconv.ParseUint(s[1:], 8, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

// parsePgSnapshot parses and validates a pg_snapshot/txid_snapshot text
// literal, mirroring parse_snapshot (xid8funcs.c). Format: xmin:xmax[:xip,...].
// On success returns the canonical form: xip values must be non-decreasing,
// with equal-valued duplicates collapsed (matching buf_add_txid's "skip
// duplicates" and the reject of any xip that goes backward). M0134-0080
// widens the original M0097-0018 form (which never rejected an out-of-order
// or zero xmin/xmax and never deduplicated) to match upstream exactly.
func parsePgSnapshot(s string) (string, bool) {
	xmin, xmax, xips, ok := parsePgSnapshotParts(s)
	if !ok {
		return "", false
	}
	return formatPgSnapshot(xmin, xmax, xips), true
}

// parsePgSnapshotParts is parsePgSnapshot's validating parse, exposing the
// three fields separately for txid_snapshot_xmin/xmax/txid_visible_in_snapshot
// (M0134-0080) instead of re-parsing the canonical string those callers would
// otherwise have to re-split.
func parsePgSnapshotParts(s string) (xmin, xmax uint64, xips []uint64, ok bool) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return 0, 0, nil, false
	}
	xmin, err1 := strconv.ParseUint(parts[0], 10, 64)
	xmax, err2 := strconv.ParseUint(parts[1], 10, 64)
	// FullTransactionIdIsValid checks TransactionIdIsValid on the LOW 32 BITS
	// only (XidFromFullTransactionId): a FullTransactionId packs epoch (high
	// 32 bits) : xid (low 32 bits), and it's the xid half that must be
	// nonzero — the full 64-bit value itself can be nonzero while still being
	// "invalid" this way, e.g. 9223372036854775808 (0x8000000000000000) has a
	// zero low half. goopg's own TransactionID has no epoch packing, but
	// txid_snapshot/pg_snapshot's on-the-wire text form must still reject
	// exactly what upstream rejects.
	if err1 != nil || err2 != nil || uint32(xmin) == 0 || uint32(xmax) == 0 || xmin > xmax {
		return 0, 0, nil, false
	}
	if len(parts) == 3 && parts[2] != "" {
		var last uint64
		haveLast := false
		for _, xip := range strings.Split(parts[2], ",") {
			v, err := strconv.ParseUint(xip, 10, 64)
			if err != nil {
				return 0, 0, nil, false
			}
			if v < xmin || v >= xmax {
				return 0, 0, nil, false
			}
			if haveLast && v < last {
				return 0, 0, nil, false
			}
			if !haveLast || v != last {
				xips = append(xips, v)
			}
			last = v
			haveLast = true
		}
	}
	return xmin, xmax, xips, true
}

// formatPgSnapshot renders xmin:xmax[:xip,...] — the pg_snapshot_out /
// txid_current_snapshot canonical text form. M0134-0080.
func formatPgSnapshot(xmin, xmax uint64, xips []uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d:%d:", xmin, xmax)
	for i, v := range xips {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", v)
	}
	return b.String()
}

// dedupSortedUint64 collapses adjacent equal values in an ascending-sorted
// slice, in place. Used by txid_current_snapshot to match sort_snapshot's
// "also remove duplicates" step (xid8funcs.c) when the in-progress array
// happens to contain the same xid twice. M0134-0080.
func dedupSortedUint64(s []uint64) []uint64 {
	if len(s) < 2 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// parsePgSnapshotValid returns true if s is a valid pg_snapshot literal.
// Format: xmin:xmax[:xip,...]. Thin wrapper over parsePgSnapshot for callers
// (pg_input_is_valid) that only need the boolean. M0097-0018.
func parsePgSnapshotValid(s string) bool {
	_, ok := parsePgSnapshot(s)
	return ok
}

// resolveRegclassOID evaluates a regclass/oid argument to its underlying OID.
// A regclass value already carries its OID once evaluated (or, less
// commonly, arrives as the textual OID from an explicit ::regclass cast) —
// same pattern already used by pg_get_indexdef/pg_get_statisticsobjdef.
func resolveRegclassOID(argExpr optimizer.Expr, slot SlotView, ctx *Context) (uint32, bool) {
	arg, err := evalExprSlot(argExpr, slot, ctx)
	if err != nil || arg.IsNull() {
		return 0, false
	}
	if arg.Kind == KindInt {
		return uint32(arg.Int), true
	}
	v, perr := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
	if perr != nil {
		return 0, false
	}
	return uint32(v), true
}

// parseForkName maps a pg_relation_size fork-name argument to storage's
// ForkNumber, matching PG's forkname_to_number (relfilenodemap.c/relpath.h).
func parseForkName(s string) (storage.ForkNumber, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "main":
		return storage.MainFork, true
	case "fsm":
		return storage.FSMFork, true
	case "vm", "visibilitymap":
		return storage.VisibilityMapFork, true
	case "init":
		return storage.InitFork, true
	default:
		return 0, false
	}
}

// relationForkSize returns the byte size of one fork of rel, or 0 if that
// fork's file has never been created. Checking Pool.Exists first is load-
// bearing: calling NBlocks on a fork that doesn't exist would silently
// create it (smgr O_CREATE semantics) — pg_relation_size must never do
// that as a side effect of merely being called.
func relationForkSize(pool *storage.Pool, rel storage.RelFileNode, fork storage.ForkNumber) int64 {
	rel.Fork = fork
	if !pool.Exists(rel) {
		return 0
	}
	n, err := pool.NBlocks(rel)
	if err != nil {
		return 0
	}
	return int64(n) * storage.BlockSize
}

// relationAllForksSize sums the main/fsm/vm forks of rel — goopg never
// materializes FSM/VM as separate on-disk forks, so those two always
// contribute 0 via relationForkSize's Exists check (an accurate "never
// created" answer, not a stub value).
func relationAllForksSize(pool *storage.Pool, rel storage.RelFileNode) int64 {
	return relationForkSize(pool, rel, storage.MainFork) +
		relationForkSize(pool, rel, storage.FSMFork) +
		relationForkSize(pool, rel, storage.VisibilityMapFork)
}

// relationFileNodeForOID resolves a regclass OID to its RelFileNode, for
// either an ordinary table or an index — pg_relation_size accepts both.
func relationFileNodeForOID(cat *catalog.InMemory, oid uint32) (storage.RelFileNode, bool) {
	if tbl, ok := cat.LookupTableByOID(oid); ok {
		return cat.RelFileNode(tbl), true
	}
	if idx, ok := cat.LookupIndexByOID(oid); ok {
		return cat.IndexRelFileNode(idx), true
	}
	// A synthetic TOAST relation OID (parent OID + 100M) has no table/index
	// registry entry — its pg_class row is virtual-only — so resolve it through
	// the catalog helper instead. pg_relation_size(reltoastrelid) then sizes the
	// live toast heap; mirrors PG, where reltoastrelid is a real relkind='t'
	// relation (dbsize.c:371-381). M0134-0070.
	if toastRel, ok := cat.ToastRelFileNodeByOID(oid); ok {
		return toastRel, true
	}
	return storage.RelFileNode{}, false
}

// evalPgRelationSize implements pg_relation_size(relation [, fork]) → bigint,
// mirroring PG's calculate_relation_size (dbsize.c): the byte size of one
// named fork (default "main") of the relation's own storage. M0122-0002.
func evalPgRelationSize(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 1 {
		return NullDatum, nil
	}
	oid, ok := resolveRegclassOID(x.Args[0], slot, ctx)
	if !ok {
		return NullDatum, nil
	}
	cat, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum, nil
	}
	rel, ok := relationFileNodeForOID(cat, oid)
	if !ok {
		return NullDatum, nil
	}
	fork := storage.MainFork
	if len(x.Args) >= 2 {
		forkArg, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		if forkArg.IsNull() {
			return NullDatum, nil
		}
		f, ok := parseForkName(forkArg.StringValue())
		if !ok {
			return Datum{}, &ExecError{Code: "22023", Message: fmt.Sprintf("invalid fork name %q", forkArg.StringValue())}
		}
		fork = f
	}
	return Datum{Kind: KindInt, Int: relationForkSize(ctx.Pool, rel, fork)}, nil
}

// evalPgTableSize implements pg_table_size(relation) → bigint, mirroring
// PG's calculate_table_size: the table's own main/fsm/vm forks plus its
// TOAST relation's forks (but not its indexes — pg_indexes_size covers
// those separately). M0122-0002.
func evalPgTableSize(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 1 {
		return NullDatum, nil
	}
	oid, ok := resolveRegclassOID(x.Args[0], slot, ctx)
	if !ok {
		return NullDatum, nil
	}
	cat, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum, nil
	}
	rel, ok := relationFileNodeForOID(cat, oid)
	if !ok {
		return NullDatum, nil
	}
	total := relationAllForksSize(ctx.Pool, rel)
	if toastRel, ok := cat.ToastRelFileNode(rel); ok {
		total += relationAllForksSize(ctx.Pool, toastRel)
	}
	return Datum{Kind: KindInt, Int: total}, nil
}

// evalPgIndexesSize implements pg_indexes_size(relation) → bigint: the
// summed size of every index belonging to the named table. M0122-0002.
func evalPgIndexesSize(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 1 {
		return NullDatum, nil
	}
	oid, ok := resolveRegclassOID(x.Args[0], slot, ctx)
	if !ok {
		return NullDatum, nil
	}
	cat, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum, nil
	}
	var total int64
	for _, idx := range cat.AllIndexes() {
		if idx.Table == nil || idx.Table.OID != oid {
			continue
		}
		total += relationAllForksSize(ctx.Pool, cat.IndexRelFileNode(idx))
	}
	return Datum{Kind: KindInt, Int: total}, nil
}

// evalPgTotalRelationSize implements pg_total_relation_size(relation) →
// bigint: pg_table_size + pg_indexes_size, matching PG's
// calculate_total_relation_size. M0122-0002.
func evalPgTotalRelationSize(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	tableSize, err := evalPgTableSize(x, slot, ctx)
	if err != nil || tableSize.IsNull() {
		return tableSize, err
	}
	idxSize, err := evalPgIndexesSize(x, slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	total := tableSize.Int
	if !idxSize.IsNull() {
		total += idxSize.Int
	}
	return Datum{Kind: KindInt, Int: total}, nil
}

// sizePretty formats a byte count as a human-readable size string, matching
// PostgreSQL's pg_size_pretty() output. Uses 1024-based units. M0097-0018.
//
// sizePretty formats an integer byte count as a human-readable size string.
// Replicates PostgreSQL's pg_size_pretty(bigint) iterative algorithm exactly,
// using uint64 for the absolute-value check to handle INT64_MIN correctly.
func sizePretty(size int64) string {
	type szUnit struct {
		name     string
		limit    uint64
		round    bool
		unitbits int
	}
	szUnits := []szUnit{
		{"bytes", 10 * 1024, false, 0},
		{"kB", 20*1024 - 1, true, 10},
		{"MB", 20*1024 - 1, true, 20},
		{"GB", 20*1024 - 1, true, 30},
		{"TB", 20*1024 - 1, true, 40},
		{"PB", 20*1024 - 1, true, 50},
	}
	cur := size
	for i, u := range szUnits {
		var absSize uint64
		if cur < 0 {
			absSize = 0 - uint64(cur) // handles INT64_MIN: 0-uint64(INT64_MIN)=2^63
		} else {
			absSize = uint64(cur)
		}
		nextIsLast := i+1 >= len(szUnits)
		if nextIsLast || absSize < u.limit {
			if u.round {
				if cur > 0 {
					cur = (cur + 1) / 2
				} else {
					cur = (cur - 1) / 2
				}
			}
			return fmt.Sprintf("%d %s", cur, u.name)
		}
		next := szUnits[i+1]
		bits := uint(next.unitbits - u.unitbits)
		if next.round {
			bits--
		}
		if u.round {
			bits++
		}
		cur /= int64(1) << bits
	}
	return fmt.Sprintf("%d PB", cur)
}

// sizePrettyBig formats a numeric byte count (given as a decimal string) as a
// human-readable size string. Uses exact big.Int/big.Rat arithmetic to replicate
// PostgreSQL's pg_size_pretty(numeric) algorithm (avoids float64 precision loss
// at the PB boundary).
func sizePrettyBig(s string) string {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return s + " bytes"
	}
	type szUnit struct {
		name     string
		limit    int64
		round    bool
		unitbits int
	}
	szUnits := []szUnit{
		{"bytes", 10 * 1024, false, 0},
		{"kB", 20*1024 - 1, true, 10},
		{"MB", 20*1024 - 1, true, 20},
		{"GB", 20*1024 - 1, true, 30},
		{"TB", 20*1024 - 1, true, 40},
		{"PB", 20*1024 - 1, true, 50},
	}
	cur := new(big.Rat).Set(r)
	for i, u := range szUnits {
		absCur := new(big.Rat).Abs(cur)
		limitR := new(big.Rat).SetInt64(u.limit)
		nextIsLast := i+1 >= len(szUnits)
		if nextIsLast || absCur.Cmp(limitR) < 0 {
			if u.round {
				// Truncate to integer, then half_rounded: (n±1)/2 toward zero.
				curInt := bigRatTrunc(cur)
				if curInt.Sign() >= 0 {
					curInt.Add(curInt, big.NewInt(1))
				} else {
					curInt.Sub(curInt, big.NewInt(1))
				}
				curInt.Quo(curInt, big.NewInt(2))
				return fmt.Sprintf("%s %s", curInt.String(), u.name)
			}
			// bytes: display exact value (preserve fractional part like PG).
			if cur.IsInt() {
				return fmt.Sprintf("%s %s", cur.Num().String(), u.name)
			}
			f, _ := strconv.ParseFloat(cur.FloatString(20), 64)
			return fmt.Sprintf("%g %s", f, u.name)
		}
		next := szUnits[i+1]
		bits := uint(next.unitbits - u.unitbits)
		if next.round {
			bits--
		}
		if u.round {
			bits++
		}
		divisor := new(big.Rat).SetInt64(int64(1) << bits)
		cur.Quo(cur, divisor)
		// Truncate toward zero after each step (exact Numeric arithmetic).
		curInt := bigRatTrunc(cur)
		cur = new(big.Rat).SetInt(curInt)
	}
	return fmt.Sprintf("%s PB", bigRatTrunc(cur).String())
}

// bigRatTrunc truncates a big.Rat toward zero and returns the integer part.
func bigRatTrunc(r *big.Rat) *big.Int {
	q, _ := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	return q
}

// sizePrettyFloat formats a float64 byte count as a human-readable size string.
// Used for KindFloat (float8) inputs to pg_size_pretty.
func sizePrettyFloat(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	var result string
	const (
		kB = float64(1024)
		MB = float64(1024 * 1024)
		GB = float64(1024 * 1024 * 1024)
		TB = float64(1024 * 1024 * 1024 * 1024)
		PB = float64(1024 * 1024 * 1024 * 1024 * 1024)
	)
	halfRoundF := func(n, unit float64) int64 { return int64(n / unit) }
	switch {
	case f < 10*kB:
		if f == float64(int64(f)) {
			result = fmt.Sprintf("%d bytes", int64(f))
		} else {
			result = fmt.Sprintf("%g bytes", f)
		}
	case f < 10*MB:
		result = fmt.Sprintf("%d kB", halfRoundF(f, kB))
	case f < 10*GB:
		result = fmt.Sprintf("%d MB", halfRoundF(f, MB))
	case f < 10*TB:
		result = fmt.Sprintf("%d GB", halfRoundF(f, GB))
	case f < 10*PB:
		result = fmt.Sprintf("%d TB", halfRoundF(f, TB))
	default:
		result = fmt.Sprintf("%d PB", halfRoundF(f, PB))
	}
	if neg {
		return "-" + result
	}
	return result
}

// parseSizeBytes parses a human-readable size string into bytes.
// Supports units: bytes/B, kB, MB, GB, TB, PB (case-insensitive).
// Also accepts scientific notation (e.g. "1e6 MB"). M0097-0018.
// Error messages and behaviour match PostgreSQL 17.
func parseSizeBytes(s string) (int64, error) {
	// orig: preserve original input exactly for error messages (matches PG behaviour).
	orig := s
	ws := strings.TrimSpace(s)
	if ws == "" {
		return 0, fmt.Errorf("invalid size: %q", "")
	}

	// Parse numeric part: optional sign, digits, optional '.'+digits, optional exponent.
	i := 0
	if i < len(ws) && (ws[i] == '-' || ws[i] == '+') {
		i++
	}
	for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
		i++
	}
	if i < len(ws) && ws[i] == '.' {
		i++
		for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
			i++
		}
	}
	expStart := i
	if i < len(ws) && (ws[i] == 'e' || ws[i] == 'E') {
		j := i + 1
		if j < len(ws) && (ws[j] == '-' || ws[j] == '+') {
			j++
		}
		if j < len(ws) && ws[j] >= '0' && ws[j] <= '9' {
			i = j
			for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
				i++
			}
		} else {
			i = expStart // no valid exponent; treat 'e' as start of unit
		}
	}
	numStr := ws[:i]
	unitStr := strings.TrimSpace(ws[i:])

	// Must have at least one digit.
	hasDigit := false
	for _, c := range numStr {
		if c >= '0' && c <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return 0, fmt.Errorf("invalid size: %q", orig)
	}

	// Handle trailing decimal point: "1." → "1.0"
	if strings.HasSuffix(numStr, ".") {
		numStr += "0"
	}

	val, parseErr := strconv.ParseFloat(numStr, 64)
	// ErrRange produces Inf — means exponent overflow, matching PG's "value overflows numeric format".
	if math.IsInf(val, 0) {
		return 0, fmt.Errorf("value overflows numeric format")
	}
	if parseErr != nil || math.IsNaN(val) {
		return 0, fmt.Errorf("invalid size: %q", orig)
	}

	const sizeHint = `Valid units are "bytes", "B", "kB", "MB", "GB", "TB", and "PB".`
	var multiplier float64
	switch strings.ToLower(unitStr) {
	case "", "b", "bytes":
		multiplier = 1
	case "kb":
		multiplier = 1024
	case "mb":
		multiplier = 1024 * 1024
	case "gb":
		multiplier = 1024 * 1024 * 1024
	case "tb":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "pb":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 0, &ExecError{
			Code:    "22023",
			Message: fmt.Sprintf("invalid size: %q", orig),
			Detail:  fmt.Sprintf("Invalid size unit: %q.", unitStr),
			Hint:    sizeHint,
		}
	}

	result := val * multiplier
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("bigint out of range")
	}
	// MaxInt64 as float64 rounds to 9.223372036854776e18; values strictly
	// greater than that can't fit in int64.
	const maxInt64Float = float64(1 << 63) // 9.223372036854776e18
	if result >= maxInt64Float || result < -maxInt64Float {
		return 0, fmt.Errorf("bigint out of range")
	}
	// Truncate toward zero, matching PostgreSQL behaviour (e.g. -.1 kB → -102).
	return int64(result), nil
}

// newNumericFromFloat converts a float64 to a KindNumeric Datum for
// EXTRACT/date_part fractional-second results. Uses up to 6 decimal places.
func newNumericFromFloat(f float64) Datum {
	s := strconv.FormatFloat(f, 'f', 6, 64)
	// Strip trailing zeros after decimal point.
	if idx := strings.Index(s, "."); idx >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if v, scale, ok := parseNumericFast(s); ok {
		return Datum{Kind: KindNumeric, Int: v, Scale: scale}
	}
	m, scale, err := parseNumeric(s)
	if err != nil {
		return NewStringDatum(s)
	}
	return newNumeric(m, int(scale))
}

// int64DivFastToNumeric reproduces PostgreSQL's int64_div_fast_to_numeric
// (postgres/src/backend/utils/adt/numeric.c:4423): it renders val / 10^log10
// as a NUMERIC whose *display scale* is exactly log10 (log10<0 → scale 0), so
// trailing zeros are preserved. EXTRACT (the numeric-returning spelling) uses
// this for its fractional-second fields — PG prints `EXTRACT(SECOND FROM
// INTERVAL '5 seconds')` as `5.000000` (scale 6), not `5`. This is distinct
// from date_part (the float8-returning spelling, retnumeric=false), which
// strips trailing zeros via newNumericFromFloat.
func int64DivFastToNumeric(val int64, log10 int) Datum {
	scale := log10
	if scale < 0 {
		scale = 0
	}
	return Datum{Kind: KindNumeric, Int: val, Scale: int16(scale)}
}

// timeOfDayMicros returns t's wall-clock time-of-day (hour/minute/second and
// fractional microseconds) expressed as microseconds since midnight, ignoring
// the date component. Used by EXTRACT(EPOCH …)/date_part('epoch', …) for the
// time / timetz source types, whose epoch is a seconds-of-day quantity rather
// than a full Unix epoch.
func timeOfDayMicros(t time.Time) int64 {
	return (int64(t.Hour())*3600+int64(t.Minute())*60+int64(t.Second()))*1_000_000 +
		int64(t.Nanosecond())/1000
}

// evalExtract implements `EXTRACT(field FROM source)` for the
// timestamp-component fields TPC-H Q7/Q8/Q9 use (year), plus
// the obvious neighbours (month, day, hour, minute, dow, doy,
// epoch). Returns int8 for most fields; float8 for fractional-second
// fields (second, millisecond, epoch). M0097-0004.
func evalExtract(x *optimizer.ExtractExpr, row Row, ctx *Context) (Datum, error) {
	src, err := evalExpr(x.Source, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() {
		return NullDatum, nil
	}
	if src.Kind == KindInterval {
		// EXTRACT(field FROM interval) has its own field taxonomy and
		// broken-down representation (interval_part_common). M0097 follow-up.
		// EXTRACT returns numeric (retnumeric=true) → scale-preserved.
		return evalExtractInterval(src, x.Field, x.Pos(), true)
	}
	if src.Kind != KindTime {
		// Try to parse a string as timestamp (planner may assign
		// string storage for date columns loaded via INSERT).
		if src.Kind == KindString {
			if t, err := parseCopyTimestamp(src.StringValue()); err == nil {
				src = NewTimeDatum(t)
			}
		}
	}
	if src.Kind != KindTime {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("EXTRACT(%s FROM …) requires timestamp/date input", x.Field)}
	}
	// Fractional-second fields return float8 (numeric) in PostgreSQL.
	u := src.TimeValue().UTC()
	field := strings.ToLower(strings.TrimSpace(x.Field))
	// For time-of-day source types, validate and handle allowed fields only. M0097-0004.
	srcType := strings.ToLower(x.SourceTypeName)
	isTimeOnly := srcType == "time" || srcType == "timetz"
	// M0134-0076: a timestamptz source extracts in the session TimeZone — PG's
	// timestamptz_part passes NULL zone to timestamp2tm, which means
	// session_timezone (postgres/src/backend/utils/adt/timestamp.c:5824). Every
	// other source kind keeps its stored wall clock; epoch is zone-independent
	// (Unix()), so it needs no special-casing.
	if srcType == "timestamptz" {
		u = u.In(sessionTimeZoneLocation(timeZoneFromCtx(ctx)))
	}
	// TIMESTAMP_NOT_FINITE arms (timestamp.c:5794): oscillating units → NULL,
	// monotonically-increasing units → ±Infinity.
	if src.IsTimestampNotFinite() {
		if d, handled := extractNonFiniteField(field, src.IsTimestampPosInf()); handled {
			return d, nil
		}
	}
	switch field {
	case "second", "seconds":
		// EXTRACT returns numeric: (tm_sec*1e6 + fsec) / 1e6 at scale 6
		// (timestamp_part / interval_part_common, retnumeric=true), so PG
		// prints `40.500000` not `40.5`.
		usec := int64(u.Second())*1_000_000 + int64(u.Nanosecond())/1000
		return int64DivFastToNumeric(usec, 6), nil
	case "milliseconds", "millisecond", "msec":
		// msec is the datetktbl alias for DTK_MILLISEC (datetime.c). Sibling of
		// evalDatePart's identical arm — they must agree. M0134-0076.
		usec := int64(u.Second())*1_000_000 + int64(u.Nanosecond())/1000
		return int64DivFastToNumeric(usec, 3), nil
	case "microseconds", "microsecond", "usec":
		// usec is the datetktbl alias for DTK_MICROSEC. Unlike milliseconds/
		// seconds, DTK_MICROSEC stores a plain intresult (timestamp.c:5557) and
		// returns int64_to_numeric — an INTEGER numeric (scale 0), so PG prints
		// `1000000` not `1000000.000000` (timestamptz.out extract block).
		usec := int64(u.Second())*1_000_000 + int64(u.Nanosecond())/1000
		return int64DivFastToNumeric(usec, 0), nil
	case "epoch":
		// EXTRACT returns numeric (retnumeric=true), so the display scale is
		// preserved. PG's epoch is source-type dependent (timestamp_part /
		// timetz_part / extract_date, all in .../adt/*.c):
		//   timestamp/timestamptz → full Unix epoch, µs/1e6 at scale 6
		//     (982355920.500000);
		//   time                  → seconds-of-day at scale 6 (74320.500000);
		//   timetz                → local seconds-of-day − offset at scale 6
		//     (offset east-positive; stored Int is the local wall-clock as UTC
		//      nanos, so u's time-of-day is the local time);
		//   date                  → integer seconds since the Unix epoch at
		//     scale 0 (982281600, no fractional part).
		switch {
		case srcType == "timetz":
			epochMicros := timeOfDayMicros(u) - int64(src.TimeTZOffsetSecs())*1_000_000
			return int64DivFastToNumeric(epochMicros, 6), nil
		case srcType == "time":
			return int64DivFastToNumeric(timeOfDayMicros(u), 6), nil
		case srcType == "date" || src.IsDate():
			return int64DivFastToNumeric(u.Unix(), 0), nil
		default: // timestamp / timestamptz
			epochMicros := u.Unix()*1_000_000 + int64(u.Nanosecond())/1000
			return int64DivFastToNumeric(epochMicros, 6), nil
		}
	case "timezone", "timezone_hour", "timezone_minute":
		if srcType == "timestamptz" {
			// Session offset, seconds east of UTC (DTK_TZ arms,
			// timestamp.c:5831-5841; Go's Zone() uses the same sign convention).
			_, off := u.Zone()
			switch field {
			case "timezone":
				return Datum{Kind: KindInt, Int: int64(off)}, nil
			case "timezone_hour":
				return Datum{Kind: KindInt, Int: int64(off / 3600)}, nil
			case "timezone_minute":
				return Datum{Kind: KindInt, Int: int64((off % 3600) / 60)}, nil
			}
		}
		if srcType == "time" {
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unit %q not supported for type time without time zone", field)}
		}
		if !isTimeOnly {
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unit %q not supported for type timestamp without time zone", field)}
		}
		// timetz: return offset components
		offsetSecs := src.TimeTZOffsetSecs()
		switch field {
		case "timezone":
			return Datum{Kind: KindInt, Int: int64(offsetSecs)}, nil
		case "timezone_hour":
			h := offsetSecs / 3600
			return Datum{Kind: KindInt, Int: int64(h)}, nil
		case "timezone_minute":
			m := (offsetSecs % 3600) / 60
			return Datum{Kind: KindInt, Int: int64(m)}, nil
		}
	}
	// For time-of-day types, reject date-specific fields with PG-compatible errors. M0097-0004.
	if isTimeOnly {
		typeName := "time without time zone"
		if srcType == "timetz" {
			typeName = "time with time zone"
		}
		switch field {
		case "hour", "minute", "microseconds", "microsecond":
			// allowed for time types (handled by extractTimestampField below)
		default:
			// Check if it's a known-but-unsupported date field or completely unknown.
			knownDateFields := map[string]bool{
				"year": true, "month": true, "day": true, "decade": true,
				"century": true, "millennium": true, "week": true, "isoweek": true,
				"isoyear": true, "isodow": true, "dow": true, "doy": true, "quarter": true,
			}
			if knownDateFields[field] {
				return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
					Message: fmt.Sprintf("unit %q not supported for type %s", field, typeName)}
			}
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unit %q not recognized for type %s", field, typeName)}
		}
	}
	if field == "julian" {
		// DTK_JULIAN (timestamp.c:5651): a fractional day, returned numeric like
		// the other fractional fields (the "msec"/"usec"/"julian" aliases land
		// here rather than in extractTimestampField, which returns int64).
		return newNumericFromFloat(julianNumber(u)), nil
	}
	n, err := extractTimestampField(x.Field, u, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
}

// evalExtractInterval implements EXTRACT(field FROM interval), a line-port of
// interval_part_common (postgres/src/backend/utils/adt/timestamp.c:6098). PG
// breaks the interval into a pg_itm via interval2itm with NO justification —
// year=month/12, mon=month%12, mday=day — and hour/min/sec/usec are carved from
// the raw micros (time) field, so hour may exceed 24 and day is taken verbatim.
// Integer-valued fields return int8; second/millisecond return numeric (fsec is
// fractional); epoch returns numeric via the DAYS_PER_YEAR/MONTH weighting.
// The ±infinity sentinels follow NonFiniteIntervalPart: monotonically-increasing
// units yield ±Infinity (numeric), oscillating units yield NULL, and any other
// unit raises the same error the finite path would.
func evalExtractInterval(src Datum, field string, pos int, retnumeric bool) (Datum, error) {
	f := strings.ToLower(strings.TrimSpace(field))

	// ±infinity sentinel handling (INTERVAL_NOT_FINITE → NonFiniteIntervalPart).
	if src.IsIntervalNotFinite() {
		switch f {
		case "microsecond", "microseconds", "millisecond", "milliseconds",
			"second", "seconds", "minute", "minutes", "week", "weeks",
			"month", "months", "quarter":
			// Oscillating units → 0.0, which PG maps to a NULL result.
			return NullDatum, nil
		case "hour", "hours", "day", "days", "year", "years",
			"decade", "decades", "century", "centuries",
			"millennium", "millenniums", "millennia", "epoch":
			// Monotonically-increasing units → ±Infinity (numeric).
			if src.IsIntervalNoBegin() {
				return NewStringDatum("-Infinity"), nil
			}
			return NewStringDatum("Infinity"), nil
		default:
			return intervalUnitError(f, pos)
		}
	}

	months := src.IntervalMonthsValue()
	days := src.IntervalDaysValue()
	micros := src.IntervalMicrosValue()

	// interval2itm: broken-down fields (no justification).
	tmYear := int64(months) / 12
	tmMon := int64(months) % 12
	tmMday := int64(days)
	t := micros
	tmHour := t / usecsPerHour
	t -= tmHour * usecsPerHour
	tmMin := t / usecsPerMinute
	t -= tmMin * usecsPerMinute
	tmSec := t / usecsPerSecond
	t -= tmSec * usecsPerSecond
	tmUsec := t

	switch f {
	case "microsecond", "microseconds":
		return Datum{Kind: KindInt, Int: tmSec*1_000_000 + tmUsec}, nil
	case "millisecond", "milliseconds":
		if retnumeric {
			// EXTRACT: (tm_sec*1e6 + usec) / 1e3 at scale 3.
			return int64DivFastToNumeric(tmSec*1_000_000+tmUsec, 3), nil
		}
		return newNumericFromFloat(float64(tmSec)*1000.0 + float64(tmUsec)/1000.0), nil
	case "second", "seconds":
		if retnumeric {
			// EXTRACT: (tm_sec*1e6 + usec) / 1e6 at scale 6.
			return int64DivFastToNumeric(tmSec*1_000_000+tmUsec, 6), nil
		}
		return newNumericFromFloat(float64(tmSec) + float64(tmUsec)/1_000_000.0), nil
	case "minute", "minutes":
		return Datum{Kind: KindInt, Int: tmMin}, nil
	case "hour", "hours":
		return Datum{Kind: KindInt, Int: tmHour}, nil
	case "day", "days":
		return Datum{Kind: KindInt, Int: tmMday}, nil
	case "week", "weeks":
		return Datum{Kind: KindInt, Int: tmMday / 7}, nil
	case "month", "months":
		return Datum{Kind: KindInt, Int: tmMon}, nil
	case "quarter":
		// Work from interval->month directly so a negative interval yields the
		// negated field of its sign-reversed value (interval_part_common).
		var q int64
		if months >= 0 {
			q = (tmMon / 3) + 1
		} else {
			q = -(((-int64(months) % 12) / 3) + 1)
		}
		return Datum{Kind: KindInt, Int: q}, nil
	case "year", "years":
		return Datum{Kind: KindInt, Int: tmYear}, nil
	case "decade", "decades":
		return Datum{Kind: KindInt, Int: tmYear / 10}, nil
	case "century", "centuries":
		return Datum{Kind: KindInt, Int: tmYear / 100}, nil
	case "millennium", "millenniums", "millennia":
		return Datum{Kind: KindInt, Int: tmYear / 1000}, nil
	case "epoch":
		// DAYS_PER_YEAR=365.25, DAYS_PER_MONTH=30, SECS_PER_DAY=86400.
		if retnumeric {
			// EXTRACT: integer arithmetic per interval_part_common — multiply
			// by 4 and divide by 4 so the fractional DAYS_PER_YEAR (365.25)
			// stays exact: 4*365.25=1461, 4*30=120, SECS_PER_DAY/4=21600.
			// secs_from_day_month always fits int64, but its product with 1e6
			// overflows around 10^9 days (~1.07e8 days for a whole-day interval,
			// or fewer via the months arm). PG guards that with
			// pg_mul/pg_add_s64_overflow and, on overflow, redoes the sum in
			// numeric (interval_part_common); we mirror both paths so huge
			// intervals return the correct value instead of a silent int64 wrap.
			// result = (secs_from_day_month*1e6 + micros) / 1e6 at scale 6.
			m := int64(months)
			secsFromDayMonth := (1461*(m/12) + 120*(m%12) + 4*int64(days)) * 21600
			if v, ok := mulInt64Overflow(secsFromDayMonth, 1_000_000); ok {
				if val, ok := addInt64Overflow(v, micros); ok {
					return int64DivFastToNumeric(val, 6), nil
				}
			}
			// Overflow fallback: numeric_add(int64_div_fast_to_numeric(time,6),
			// int64_to_numeric(secs_from_day_month)) — the whole-seconds term is
			// scale 0, the time term scale 6, so the sum lands at scale 6 exactly
			// like the fast path but backed by big.Int.
			return numericAdd(int64DivFastToNumeric(micros, 6), numericFromInt(secsFromDayMonth))
		}
		result := float64(micros) / 1_000_000.0
		result += 365.25 * 86400.0 * float64(int64(months)/12)
		result += 30.0 * 86400.0 * float64(int64(months)%12)
		result += 86400.0 * float64(days)
		return newNumericFromFloat(result), nil
	default:
		return intervalUnitError(f, pos)
	}
}

// intervalUnitError reproduces interval_part_common's two error taxonomies:
// a unit that PG's DecodeUnits recognizes but does not support for interval
// raises 0A000 (feature not supported); a wholly unknown unit raises 22023
// (invalid parameter value / not recognized).
func intervalUnitError(field string, pos int) (Datum, error) {
	knownUnsupported := map[string]bool{
		"dow": true, "isodow": true, "doy": true, "isoyear": true,
		"julian": true, "timezone": true, "timezone_hour": true,
		"timezone_minute": true,
	}
	if knownUnsupported[field] {
		return Datum{}, &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("unit %q not supported for type interval", field)}
	}
	return Datum{}, &ExecError{Code: "22023", Pos: pos,
		Message: fmt.Sprintf("unit %q not recognized for type interval", field)}
}

// extractTimestampField returns the integer value of a named
// calendar field from a UTC timestamp. Shared by evalExtract and
// evalDatePart.
func extractTimestampField(field string, t time.Time, pos int) (int64, error) {
	// M0134-0076: the caller (evalExtract/evalDatePart) passes t already
	// zone-adjusted for a timestamptz source; this helper no longer strips the
	// zone with .UTC(). A plain timestamp/date/time/timetz keeps its wall clock.
	switch field {
	case "year":
		return int64(t.Year()), nil
	case "month":
		return int64(t.Month()), nil
	case "day":
		return int64(t.Day()), nil
	case "hour":
		return int64(t.Hour()), nil
	case "minute":
		return int64(t.Minute()), nil
	case "second":
		return int64(t.Second()), nil
	case "dow":
		return int64(t.Weekday()), nil // Sunday=0, matches upstream
	case "doy":
		return int64(t.YearDay()), nil
	case "epoch":
		return t.Unix(), nil
	case "quarter":
		return int64((int(t.Month())-1)/3 + 1), nil
	// M0097-0004: additional calendar fields.
	case "week", "isoweek":
		_, week := t.ISOWeek()
		return int64(week), nil
	case "isoyear":
		year, _ := t.ISOWeek()
		return int64(year), nil
	case "isodow":
		wd := t.Weekday()
		if wd == 0 {
			wd = 7 // ISO: Sunday = 7
		}
		return int64(wd), nil
	case "decade":
		y := int64(t.Year())
		if y >= 0 {
			return y / 10, nil
		}
		return (y - 9) / 10, nil
	case "century":
		y := int64(t.Year())
		if y > 0 {
			return (y + 99) / 100, nil
		}
		return -((-y + 99) / 100), nil
	case "millennium":
		y := int64(t.Year())
		if y > 0 {
			return (y + 999) / 1000, nil
		}
		return -((-y + 999) / 1000), nil
	case "microseconds", "microsecond", "usec":
		return int64(t.Second())*1_000_000 + int64(t.Nanosecond()/1000), nil
	case "milliseconds", "millisecond", "msec":
		return int64(t.Second())*1000 + int64(t.Nanosecond()/1_000_000), nil
	// timezone/timezone_hour/timezone_minute are owned by the callers, which
	// know the source type (timestamptz → session offset, else 22023). M0134-0076.
	default:
		return 0, &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("date field %q is not supported in v0", field)}
	}
}

// sessionTimeZoneLocation resolves the session's TimeZone GUC to a
// *time.Location with exactly the renderer's semantics (sessionLocation,
// internal/utils/misc/timestamptz_out.go:37): "" → UTC, and any spelling Go's
// tzdata does not know (POSIX-style fixed offsets like "+05:30") falls back to
// UTC — so EXTRACT/date_part and FormatTimestampTZ always agree. M0134-0076.
func sessionTimeZoneLocation(zone string) *time.Location {
	if zone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// julianNumber ports DTK_JULIAN (postgres/src/backend/utils/adt/timestamp.c:5651):
// date2j(year,mon,day) + (hour*3600 + min*60 + sec + fsec)/86400 — the Julian day
// number plus the fraction of the day elapsed since midday. t carries the fields
// in the session zone (timestamptz) or the stored wall clock. The callers return
// it as numeric (EXTRACT) / float8 (date_part), like the other fractional fields.
func julianNumber(t time.Time) float64 {
	year, month, day := t.Date()
	jd := date2j(year, int(month), day)
	f := (float64(t.Hour()*3600+t.Minute()*60+t.Second())*1e9 + float64(t.Nanosecond())) / 86400e9
	return float64(jd) + f
}

// date2j ports PostgreSQL's Gregorian day-number function
// (postgres/src/backend/utils/adt/datetime.c:296). PG feeds it tm_year, the
// astronomical year (1 BC = 0), which is exactly Go's time.Year() convention.
func date2j(year, month, day int) int {
	if month > 2 {
		month += 1
		year += 4800
	} else {
		month += 13
		year += 4799
	}
	century := year / 100
	julian := year*365 - 32167
	julian += year/4 - century + century/4
	julian += 7834*month/256 + day
	return julian
}

// extractNonFiniteField ports the TIMESTAMP_NOT_FINITE arms of
// NonFiniteTimestampTzPart (postgres/src/backend/utils/adt/timestamp.c:5441),
// reached by both callers for ±infinity timestamps and dates (NewTimestampInfinity
// and NewDateInfinity both carry Int=±MaxInt64): oscillating units return NULL,
// monotonically-increasing units return ±Infinity. handled=false means the field
// is not one of those arms, so the caller falls through to normal extraction
// (which raises the same unit error it would for a finite input).
func extractNonFiniteField(field string, positive bool) (Datum, bool) {
	switch field {
	case "microseconds", "microsecond", "milliseconds", "millisecond",
		"msec", "usec", "second", "seconds", "minute", "hour", "day", "month",
		"quarter", "week", "isoweek", "dow", "isodow", "doy",
		"timezone", "timezone_hour", "timezone_minute":
		return NullDatum, true
	case "year", "decade", "century", "millennium", "isoyear", "julian", "epoch":
		if positive {
			return NewStringDatum("Infinity"), true
		}
		return NewStringDatum("-Infinity"), true
	}
	return Datum{}, false
}

// evalDatePart implements PostgreSQL's `date_part(text, timestamp)`
// builtin. The first argument is a string literal naming the field
// (e.g. 'year', 'month', 'quarter'). Semantics match
// extractTimestampField, which is shared with EXTRACT.
func evalDatePart(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part(text, timestamp) requires exactly 2 arguments"}
	}
	fieldArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	src, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if fieldArg.IsNull() || src.IsNull() {
		return NullDatum, nil
	}
	if fieldArg.Kind != KindString {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part first argument must be text"}
	}
	if src.Kind == KindInterval {
		// date_part('field', interval) is the function spelling of
		// EXTRACT(field FROM interval); share the same line-port. M0097 follow-up.
		// date_part returns float8 (retnumeric=false) → trailing zeros stripped.
		return evalExtractInterval(src, fieldArg.StringValue(), x.Pos(), false)
	}
	if src.Kind != KindTime {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part second argument must be timestamp/date"}
	}
	// Fractional-second fields return float8 (numeric), like evalExtract. M0097-0004.
	u := src.TimeValue().UTC()
	// M0134-0076: sibling of evalExtract — a timestamptz source extracts in the
	// session TimeZone (timestamp.c:5824); other kinds keep their stored wall
	// clock. Discriminator is the datum's TimeSub tag, since a FuncCall carries
	// no declared source type.
	if src.IsTimestampTZ() {
		u = u.In(sessionTimeZoneLocation(timeZoneFromCtx(ctx)))
	}
	field := strings.ToLower(strings.TrimSpace(fieldArg.StringValue()))
	// TIMESTAMP_NOT_FINITE arms (timestamp.c:5794).
	if src.IsTimestampNotFinite() {
		if d, handled := extractNonFiniteField(field, src.IsTimestampPosInf()); handled {
			return d, nil
		}
	}
	switch field {
	case "second", "seconds":
		f := float64(u.Second()) + float64(u.Nanosecond())/1e9
		return newNumericFromFloat(f), nil
	case "milliseconds", "millisecond", "msec":
		// msec/usec are the datetktbl aliases (datetime.c), sibling of
		// evalExtract's identical arms — they must agree. M0134-0076.
		f := float64(u.Second())*1000 + float64(u.Nanosecond())/1_000_000.0
		return newNumericFromFloat(f), nil
	case "microseconds", "microsecond", "usec":
		f := float64(u.Second())*1_000_000 + float64(u.Nanosecond())/1000.0
		return newNumericFromFloat(f), nil
	case "epoch":
		// date_part returns float8 (retnumeric=false) → trailing zeros stripped.
		// timetz carries its offset in Scale (minutes east of UTC): its epoch is
		// local seconds-of-day − offset. Every other KindTime source (time,
		// timestamp, timestamptz, date) has Scale 0 and uses the full Unix epoch;
		// for a `time` value (always stored on 1970-01-01) the full Unix epoch
		// equals its seconds-of-day, so the uniform formula is correct there too.
		if src.Scale != 0 {
			f := float64(timeOfDayMicros(u))/1e6 - float64(src.TimeTZOffsetSecs())
			return newNumericFromFloat(f), nil
		}
		epochMicros := u.Unix()*1_000_000 + int64(u.Nanosecond())/1000
		return newNumericFromFloat(float64(epochMicros)/1e6), nil
	case "julian":
		// DTK_JULIAN (timestamp.c:5651), returned float8 like the other
		// fractional fields.
		return newNumericFromFloat(julianNumber(u)), nil
	case "timezone", "timezone_hour", "timezone_minute":
		if src.IsTimestampTZ() {
			// Session offset, seconds east of UTC (timestamp.c:5831-5841).
			_, off := u.Zone()
			switch field {
			case "timezone":
				return Datum{Kind: KindInt, Int: int64(off)}, nil
			case "timezone_hour":
				return Datum{Kind: KindInt, Int: int64(off / 3600)}, nil
			case "timezone_minute":
				return Datum{Kind: KindInt, Int: int64((off % 3600) / 60)}, nil
			}
		}
		// A zone-less timestamp/date has no offset to report. PG rejects with
		// 0A000 (timestamp.c:5683-5691); goopg keeps evalExtract's 22023.
		return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
			Message: fmt.Sprintf("unit %q not supported for type timestamp without time zone", field)}
	}
	n, err := extractTimestampField(field, u, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
}

// evalToChar implements to_char(value, fmt) → text.
// Converts a timestamp or number to a string using a PostgreSQL format string.
// Supports a subset of PostgreSQL format codes. M0097-0004.
func evalToChar(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 2 {
		return NullDatum, nil
	}
	srcArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || srcArg.IsNull() {
		return NullDatum, nil
	}
	fmtArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil || fmtArg.IsNull() {
		return NullDatum, nil
	}
	fmtStr := strings.TrimSpace(fmtArg.StringValue())
	// to_char(timestamp/time, fmt) — time/date formatting.
	if srcArg.Kind == KindTime {
		t := srcArg.TimeValue().UTC()
		goFmt := pgToCharToGoFormat(fmtStr)
		return NewStringDatum(t.Format(goFmt)), nil
	}
	// to_char(numeric, fmt) — number formatting. M0097-0105.
	return NewStringDatum(toCharNumericFormat(srcArg, fmtStr)), nil
}

// toCharNumericFormat formats a numeric Datum using a PostgreSQL numeric format string.
// Supports: FM prefix, 0 (zero-fill), 9 (space-fill), . (decimal point),
// , (grouping separator), S/MI/PL/PR signs. M0097-0105.
func toCharNumericFormat(val Datum, fmtStr string) string {
	orig := fmtStr
	upper := strings.ToUpper(strings.TrimSpace(fmtStr))

	// EEEE/eeee — scientific notation: delegate entirely.
	if strings.Contains(upper, "EEEE") {
		// Extract the mantissa format (everything before EEEE, minus FM/sign tokens).
		eeeeIdx := strings.Index(strings.ToUpper(orig), "EEEE")
		mantissaRaw := orig[:eeeeIdx]
		mantissaFmt := strings.NewReplacer("FM", "", "fm", "", "S", "", "s", "").Replace(mantissaRaw)
		lowercase := strings.Contains(orig, "eeee")
		fmMode := strings.Contains(upper, "FM")
		return toCharScientific(val, mantissaFmt, lowercase, fmMode)
	}

	// RN — Roman numerals: delegate entirely.
	if strings.Contains(upper, "RN") {
		fmMode := strings.Contains(upper, "FM")
		return toCharRoman(val, fmMode)
	}

	// FM: fill mode — strip leading/trailing spaces.
	fm := strings.Contains(upper, "FM")
	if fm {
		upper = strings.ReplaceAll(upper, "FM", "")
	}

	// Pre-scan original format for quoted literal text segments ("...").
	// We extract them keyed by their byte-position range so we can splice them
	// back into the result after digit formatting.
	type quotedSeg struct {
		// position in the upper-cased, stripped format string after quotes removed
		insertAfterPos int // insert after this many chars of the stripped upper string
		text           string
	}
	var quotedSegs []quotedSeg
	{
		// Build a parallel "stripped" upper string (no quote regions) and record where
		// each quoted segment goes.
		stripped := strings.Builder{}
		i := 0
		for i < len(upper) {
			if upper[i] == '"' {
				// Find closing quote; handle backslash-escaped quotes \"  inside.
				j := i + 1
				for j < len(upper) && upper[j] != '"' {
					if upper[j] == '\\' && j+1 < len(upper) && upper[j+1] == '"' {
						j += 2 // skip \"
					} else {
						j++
					}
				}
				// Extract raw text from orig (preserving case) and unescape \" → ".
				rawText := orig[i+1 : i+1+(j-(i+1))]
				rawText = strings.ReplaceAll(rawText, `\"`, `"`)
				quotedSegs = append(quotedSegs, quotedSeg{
					insertAfterPos: stripped.Len(),
					text:           rawText,
				})
				i = j + 1 // skip past closing '"'
			} else {
				stripped.WriteByte(upper[i])
				i++
			}
		}
		upper = stripped.String()
	}

	// TH/th ordinal suffix.
	hasTHUpper, hasTHLower := false, false
	if strings.Contains(upper, "TH") {
		origNoFM := strings.ReplaceAll(strings.ReplaceAll(orig, "FM", ""), "fm", "")
		hasTHUpper = strings.Contains(origNoFM, "TH")
		hasTHLower = strings.Contains(origNoFM, "th")
		upper = strings.ReplaceAll(upper, "TH", "")
	}

	// Detect SG (sign) in the middle of the format: 2-char code that outputs the sign (+/-)
	// at that position without any extra leading sign space. Detect AFTER quoted-text removal
	// so the position is relative to the stripped upper string. SG at position 0 is handled
	// by the hasSStart path below (S at start of format). M0097-0147.
	hasSGMid := false
	sgMidInjectAt := 0
	if idx := strings.Index(upper, "SG"); idx > 0 {
		hasSGMid = true
		sgMidInjectAt = idx
		upper = upper[:idx] + upper[idx+2:]
	}

	// Detect sign modifiers. MI/PL positions matter (prefix vs suffix).
	hasMIStart := strings.HasPrefix(upper, "MI")
	hasMIEnd := !hasMIStart && strings.HasSuffix(upper, "MI")
	hasMI := hasMIStart || hasMIEnd
	hasPLEnd := strings.HasSuffix(upper, "PL")
	hasPLStart := !hasPLEnd && strings.HasPrefix(upper, "PL")
	hasPL := hasPLStart || hasPLEnd
	hasPR := strings.Contains(upper, "PR")
	hasSStart, hasSSuffix, hasS := false, false, false
	if !hasMI && !hasPL && !hasPR {
		// Remove G/D/L/C/spaces so they don't confuse S detection.
		chk := strings.NewReplacer("G", "", "D", "", "L", "", "C", "", " ", "").Replace(upper)
		if strings.HasPrefix(chk, "S") {
			hasSStart = true
		} else if strings.HasSuffix(chk, "S") {
			hasSSuffix = true
		}
		hasS = hasSStart || hasSSuffix
	}

	// Strip sign specifiers for digit-format processing.
	digitFmt := upper
	digitFmt = strings.ReplaceAll(digitFmt, "MI", "")
	digitFmt = strings.ReplaceAll(digitFmt, "PL", "")
	digitFmt = strings.ReplaceAll(digitFmt, "PR", "")
	digitFmt = strings.ReplaceAll(digitFmt, "S", "")
	// Map locale separators to canonical chars.
	digitFmt = strings.ReplaceAll(digitFmt, "G", ",")
	digitFmt = strings.ReplaceAll(digitFmt, "D", ".")
	digitFmt = strings.ReplaceAll(digitFmt, "L", " ")
	digitFmt = strings.ReplaceAll(digitFmt, "C", "")

	// V — decimal shift: multiply value by 10^N where N = digit positions after V.
	vShift := 0
	if vIdx := strings.Index(digitFmt, "V"); vIdx >= 0 {
		afterV := digitFmt[vIdx+1:]
		for _, c := range afterV {
			if c == '0' || c == '9' {
				vShift++
			}
		}
		digitFmt = digitFmt[:vIdx]
	}

	// Split into integer and decimal parts.
	dotIdx := strings.Index(digitFmt, ".")
	var intFmt, decFmt string
	if dotIdx >= 0 {
		intFmt = digitFmt[:dotIdx]
		decFmt = digitFmt[dotIdx+1:]
	} else {
		intFmt = digitFmt
	}
	intFmtDigits := strings.ReplaceAll(intFmt, ",", "")
	intFmtDigits = strings.ReplaceAll(intFmtDigits, " ", "")
	decFmtDigits := strings.ReplaceAll(decFmt, ",", "")
	decFmtDigits = strings.ReplaceAll(decFmtDigits, " ", "")

	// Zero-fill: a '0' format char at position i makes all positions j >= i use
	// '0' fill instead of ' ' fill (propagates rightward from the leftmost '0').
	zeroFillFrom := len(intFmtDigits) // default: no zero-fill
	for i, c := range intFmtDigits {
		if c == '0' {
			zeroFillFrom = i
			break
		}
	}
	// totalDigitPositions is used to map right-to-left walk index → left-to-right position.
	totalDigitPositions := 0
	for _, c := range intFmtDigits {
		if c == '0' || c == '9' {
			totalDigitPositions++
		}
	}

	decPositions := 0
	for _, c := range decFmtDigits {
		if c == '0' || c == '9' {
			decPositions++
		}
	}

	// Extract numeric value.
	negative := false
	var intVal int64
	var fracStr string
	switch val.Kind {
	case KindInt:
		intVal = val.Int
		if intVal < 0 {
			negative = true
			intVal = -intVal
		}
	case KindNumeric:
		m := val.NumericMantissaValue()
		s := int(val.NumericScaleValue())
		if m < 0 {
			negative = true
			m = -m
		}
		if s > 0 {
			var divisor int64 = 1
			for i := 0; i < s; i++ {
				divisor *= 10
			}
			intVal = m / divisor
			rem := m % divisor
			if decPositions > 0 {
				fracStr = fmt.Sprintf("%0*d", s, rem)
				if len(fracStr) > decPositions {
					fracStr = fracStr[:decPositions]
				}
			}
		} else {
			intVal = m
		}
	case KindString:
		f, parseErr := strconv.ParseFloat(val.StringValue(), 64)
		if parseErr == nil {
			if f < 0 {
				negative = true
				f = -f
			}
			intVal = int64(f)
			if decPositions > 0 {
				frac := f - float64(intVal)
				fs := fmt.Sprintf("%.*f", decPositions, frac)
				if len(fs) > 2 {
					fracStr = fs[2:]
				}
				if len(fracStr) > decPositions {
					fracStr = fracStr[:decPositions]
				}
			}
		}
	}

	// Apply V shift: multiply intVal by 10^vShift and clear decimal part.
	if vShift > 0 {
		mult := int64(1)
		for i := 0; i < vShift; i++ {
			mult *= 10
		}
		intVal *= mult
		fracStr = ""
		decPositions = 0
		decFmt = ""
		dotIdx = -1
	}

	// Format integer part: walk intFmt right-to-left, preserving comma/space positions.
	// Track whether each char slot is a fill position (vs actual digit).
	intStr := strconv.FormatInt(intVal, 10)
	var intBuf []byte
	var intIsFill []bool // parallel to intBuf: true = fill char
	pos := len(intStr) - 1
	digitPosFromRight := 0
	for fi := len(intFmt) - 1; fi >= 0; fi-- {
		fc := intFmt[fi]
		switch fc {
		case ',':
			intBuf = append([]byte{','}, intBuf...)
			intIsFill = append([]bool{true}, intIsFill...) // comma is fill until second pass
		case ' ':
			// Literal space in format — treat as fill spacer.
			intBuf = append([]byte{' '}, intBuf...)
			intIsFill = append([]bool{true}, intIsFill...)
		case '0', '9':
			digitPosFromLeft := totalDigitPositions - 1 - digitPosFromRight
			digitPosFromRight++
			fillCh := byte(' ')
			if digitPosFromLeft >= zeroFillFrom {
				fillCh = '0'
			}
			if pos >= 0 {
				intBuf = append([]byte{intStr[pos]}, intBuf...)
				intIsFill = append([]bool{false}, intIsFill...)
				pos--
			} else {
				intBuf = append([]byte{fillCh}, intBuf...)
				intIsFill = append([]bool{true}, intIsFill...)
			}
		}
	}
	// Overflow: more digits than format positions.
	for pos >= 0 {
		intBuf = append([]byte{intStr[pos]}, intBuf...)
		intIsFill = append([]bool{false}, intIsFill...)
		pos--
	}

	// Second pass: replace commas in the fill area (before first actual digit) with
	// the appropriate fill char (space for '9' area, '0' for '0' area).
	// gFillCount tracks how many G (comma) separators were in the fill area (before first
	// actual digit); used later by the S-start sign handler to adjust column width.
	gFillCount := 0
	seenActualDigit := false
	for i := range intBuf {
		if !intIsFill[i] {
			seenActualDigit = true
		}
		if intBuf[i] == ',' && !seenActualDigit {
			gFillCount++
			// Determine fill char at this position from the nearest digit slot.
			// Use the rightmost fill char type in the prefix region.
			// Simple heuristic: if any preceding fill slot used '0', use '0', else ' '.
			fillCh := byte(' ')
			for j := 0; j < i; j++ {
				if intIsFill[j] && intBuf[j] == '0' {
					fillCh = '0'
					break
				}
			}
			intBuf[i] = fillCh
		}
	}
	result := string(intBuf)

	// V-shift extends effective output width: 99999V99 with value 1234 → 1234×100=123400,
	// width = 5 (before V) + 2 (after V) = 7 digit slots, not just 5. Pad with fill if needed.
	if vShift > 0 && len(result) < totalDigitPositions+vShift {
		result = strings.Repeat(" ", totalDigitPositions+vShift-len(result)) + result
	}

	// Append decimal part.
	if dotIdx >= 0 && decPositions > 0 {
		if fracStr == "" {
			fracStr = strings.Repeat("0", decPositions)
		} else if len(fracStr) < decPositions {
			fracStr += strings.Repeat("0", decPositions-len(fracStr))
		}
		// Walk decFmt left-to-right to insert any commas at their positions (G separators).
		decOut := strings.Builder{}
		fracPos := 0
		for _, dc := range decFmt {
			switch dc {
			case ',':
				decOut.WriteByte(',')
			case ' ':
				decOut.WriteByte(' ')
			case '0', '9':
				if fracPos < len(fracStr) {
					decOut.WriteByte(fracStr[fracPos])
				} else {
					decOut.WriteByte('0')
				}
				fracPos++
			}
		}
		result = result + "." + decOut.String()
	}

	// FM mode: strip leading spaces only (from '9' fill without zero-fill propagation).
	// '0' fill positions and propagated zero-fill are NOT stripped.
	// For decimal: strip trailing zeros from '9' decimal positions; keep '0' positions.
	if fm {
		result = strings.TrimLeft(result, " ")
		if result == "" || result == "." {
			result = "0"
		}
		if dotIdx >= 0 {
			hasAnyDecimalZero := strings.ContainsRune(decFmtDigits, '0')
			if !hasAnyDecimalZero {
				// Strip trailing fractional zeros (from '9' positions); keep dot.
				result = strings.TrimRight(result, "0")
			}
		}
	}

	// Ordinal suffix (positive values only).
	ordSuffix := ""
	if (hasTHUpper || hasTHLower) && !negative {
		sfx := toCharOrdinalSuffix(intVal)
		if hasTHLower {
			sfx = strings.ToLower(sfx)
		}
		ordSuffix = sfx
	}

	// Splice quoted literal segments into result BEFORE applying sign.
	// The insertAfterPos values are relative to the stripped-upper (digit-only) coordinate
	// system. Inserting before sign keeps them valid without a sign-prefix offset.
	// Exception: hasSGMid injects the sign at a digit-column position; if quoted text also
	// appears, positions shift and would require re-calculation — defer that edge case.
	if len(quotedSegs) > 0 && !hasSGMid {
		for i := len(quotedSegs) - 1; i >= 0; i-- {
			seg := quotedSegs[i]
			insertAt := seg.insertAfterPos
			// Adjust insertion point when the literal format space immediately before
			// the insertion is in the fill area (all preceding positions are fills).
			// In PostgreSQL's to_char, a literal separator space adjacent to the fill
			// region is output AFTER the quoted text rather than before, so we insert
			// before it. For large values (digit at the preceding position) no shift.
			if insertAt > 0 && insertAt <= len(intFmt) && intFmt[insertAt-1] == ' ' {
				allFillBefore := true
				for j := 0; j < insertAt-1; j++ {
					if !intIsFill[j] {
						allFillBefore = false
						break
					}
				}
				if allFillBefore {
					insertAt--
				}
			}
			if insertAt > len(result) {
				insertAt = len(result)
			}
			result = result[:insertAt] + seg.text + result[insertAt:]
		}
	}

	// Apply sign modifier.
	if hasSGMid {
		// SG in the middle: inject sign at the recorded position, no leading sign space.
		sign := byte('+')
		if negative {
			sign = '-'
		}
		if sgMidInjectAt > len(result) {
			sgMidInjectAt = len(result)
		}
		result = result[:sgMidInjectAt] + string(sign) + result[sgMidInjectAt:]
	} else if hasMI {
		if hasMIStart {
			if negative {
				// Place minus right before the first significant digit.
				trim := strings.TrimLeft(result, " ")
				spaces := len(result) - len(trim)
				result = strings.Repeat(" ", spaces) + "-" + trim
			} else if !fm {
				result = " " + result
			}
		} else {
			// hasMIEnd: sign at the end.
			if negative {
				result = result + "-"
			} else if !fm {
				result = result + " "
			}
		}
	} else if hasPL {
		if hasPLEnd {
			if !negative {
				// PL-end positive: add one more leading sign-space and trailing + symbol.
				result = " " + result + "+"
			} else {
				// PL-end negative: sign right before digits, trailing space for PL position.
				trim := strings.TrimLeft(result, " ")
				spaces := len(result) - len(trim)
				result = strings.Repeat(" ", spaces) + "-" + trim + " "
			}
		} else {
			// hasPLStart: sign right before first significant digit.
			trim := strings.TrimLeft(result, " ")
			spaces := len(result) - len(trim)
			if negative {
				result = strings.Repeat(" ", spaces) + "-" + trim
			} else {
				result = strings.Repeat(" ", spaces) + "+" + trim
			}
		}
	} else if hasPR {
		if negative {
			// Place < right before the first significant digit, > at end.
			trim := strings.TrimLeft(result, " ")
			spaces := len(result) - len(trim)
			result = strings.Repeat(" ", spaces) + "<" + trim + ">"
		} else if !fm {
			result = " " + result + " "
		}
	} else if hasS {
		if hasSSuffix {
			if negative {
				result = result + "-"
			} else {
				result = result + "+"
			}
		} else {
			// hasSStart: sign right before first significant digit (no extra leading space).
			trim := strings.TrimLeft(result, " ")
			spaces := len(result) - len(trim)
			if gFillCount > 0 {
				// G separators in fill area produce an extra fill space; absorb them
				// so the sign sits at position 0 with the correct total width.
				fill := spaces - gFillCount
				if fill < 0 {
					fill = 0
				}
				if negative {
					result = "-" + strings.Repeat(" ", fill) + trim
				} else {
					result = "+" + strings.Repeat(" ", fill) + trim
				}
			} else {
				if negative {
					result = strings.Repeat(" ", spaces) + "-" + trim
				} else {
					result = strings.Repeat(" ", spaces) + "+" + trim
				}
			}
		}
	} else {
		// Default: sign immediately before first significant digit; positive
		// reserves one extra leading space for the sign position.
		if negative {
			trim := strings.TrimLeft(result, " ")
			spaces := len(result) - len(trim)
			result = strings.Repeat(" ", spaces) + "-" + trim
		} else if !fm {
			result = " " + result
		}
	}

	// hasSGMid + quoted text: sign position shifts with text insertion; fall back to
	// post-sign insertion with a sign-prefix offset to keep sign embedded correctly.
	if len(quotedSegs) > 0 && hasSGMid {
		signPrefix := 0
		if len(result) > 0 && (result[0] == ' ' || result[0] == '+' || result[0] == '-') {
			signPrefix = 1
		}
		for i := len(quotedSegs) - 1; i >= 0; i-- {
			seg := quotedSegs[i]
			insertAt := seg.insertAfterPos + signPrefix
			if insertAt > len(result) {
				insertAt = len(result)
			}
			result = result[:insertAt] + seg.text + result[insertAt:]
		}
	}

	return result + ordSuffix
}

// toCharOrdinalSuffix returns the English ordinal suffix (ST/ND/RD/TH) for n.

// toCharScientific formats val in scientific notation for the EEEE/eeee format modifier.
// mantissaFmt is the digit format string before the EEEE token (e.g. "9.99" from "9.99EEEE").
// lowercase controls whether the exponent uses 'e' or 'E'. fm strips the leading sign space.
func toCharScientific(val Datum, mantissaFmt string, lowercase bool, fm bool) string {
	f, ok := datumToFloat64(val)
	if !ok {
		return "0"
	}
	negative := f < 0
	if negative {
		f = -f
	}

	// Count decimal positions in mantissaFmt (digits after the '.').
	decPlaces := 0
	dotSeen := false
	for _, c := range mantissaFmt {
		if c == '.' || c == 'D' {
			dotSeen = true
			continue
		}
		if dotSeen && (c == '0' || c == '9') {
			decPlaces++
		}
	}

	// Special values (Infinity/-Infinity/NaN): PostgreSQL never errors for these
	// in EEEE format; it emits a fixed "#" pattern with no sign marker, regardless
	// of Inf's sign or NaN. postgres/src/backend/utils/adt/formatting.c, the
	// isnan(value) || isinf(value) branch inside float4_to_char/float8_to_char
	// (numeric_to_char follows the identical NUMERIC_IS_SPECIAL pattern):
	// one sign-slot space (stripped by FM), Num.pre '#'s (always 1 for a valid
	// EEEE format), '.', then Num.post+4 '#'s.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		const pre = 1
		special := strings.Repeat("#", pre) + "." + strings.Repeat("#", decPlaces+4)
		if !fm {
			special = " " + special
		}
		return special
	}

	// Format using Go's scientific notation, then reformat the exponent to ±NN.
	raw := fmt.Sprintf("%.*e", decPlaces, f)
	// raw is like "1.23e+03" or "1.23e+003" (platform-dependent).
	eIdx := strings.LastIndex(raw, "e")
	mantissa := raw[:eIdx]
	expPart := raw[eIdx+1:] // e.g. "+03" or "+003"

	// Normalise exponent to sign + exactly 2 digits.
	expSign := "+"
	if len(expPart) > 0 && (expPart[0] == '+' || expPart[0] == '-') {
		expSign = string(expPart[0])
		expPart = expPart[1:]
	}
	// Strip leading zeros to get the numeric value, then re-pad to 2 digits.
	expNum := strings.TrimLeft(expPart, "0")
	if expNum == "" {
		expNum = "0"
	}
	expFormatted := fmt.Sprintf("%s%02s", expSign, expNum)

	// PostgreSQL always uses lowercase 'e' in scientific notation regardless of EEEE/eeee case.
	result := mantissa + "e" + expFormatted

	if negative {
		result = "-" + result
	} else if !fm {
		result = " " + result
	}
	return result
}

// toCharRoman converts val to a Roman numeral string for the RN format.
// Returns a 15-char right-justified string (or fm-stripped). Out-of-range values return "###############".
func toCharRoman(val Datum, fm bool) string {
	f, ok := datumToFloat64(val)
	if !ok {
		return "###############"
	}
	n := int64(f)
	if n < 1 || n > 3999 {
		return "###############"
	}

	thousands := []string{"", "M", "MM", "MMM"}
	hundreds := []string{"", "C", "CC", "CCC", "CD", "D", "DC", "DCC", "DCCC", "CM"}
	tens := []string{"", "X", "XX", "XXX", "XL", "L", "LX", "LXX", "LXXX", "XC"}
	ones := []string{"", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX"}

	roman := thousands[n/1000] + hundreds[(n%1000)/100] + tens[(n%100)/10] + ones[n%10]
	if fm {
		return roman
	}
	// Right-justify in 15 chars.
	if len(roman) < 15 {
		return strings.Repeat(" ", 15-len(roman)) + roman
	}
	return roman
}

// int64ToAbsUint64 returns the absolute value of v as uint64,
// correctly handling math.MinInt64 which cannot be negated in int64.
func int64ToAbsUint64(v int64) uint64 {
	if v >= 0 {
		return uint64(v)
	}
	if v == math.MinInt64 {
		return uint64(math.MaxInt64) + 1
	}
	return uint64(-v)
}

func toCharOrdinalSuffix(n int64) string {
	if n < 0 {
		n = -n
	}
	if mod100 := n % 100; mod100 >= 11 && mod100 <= 13 {
		return "TH"
	}
	switch n % 10 {
	case 1:
		return "ST"
	case 2:
		return "ND"
	case 3:
		return "RD"
	default:
		return "TH"
	}
}

func pgToCharToGoFormat(pg string) string {
	replacer := strings.NewReplacer(
		"YYYY", "2006",
		"YYY", "006",
		"YY", "06",
		"Y", "6",
		"IYYY", "2006", // ISO year — approximate
		"IYY", "006",
		"IY", "06",
		"I", "6",
		"MM", "01",
		"MON", "Jan",
		"Mon", "Jan",
		"mon", "jan",
		"MONTH", "January",
		"Month", "January",
		"month", "january",
		"DD", "02",
		"D", "1", // day of week 1=Sun PostgreSQL, Go: Mon=1
		"DAY", "Monday",
		"Day", "Monday",
		"day", "monday",
		"DY", "Mon",
		"Dy", "Mon",
		"dy", "mon",
		"HH24", "15",
		"HH12", "03",
		"HH", "03",
		"MI", "04",
		"SS", "05",
		"MS", "000", // milliseconds
		"US", "000000", // microseconds
		"TZ", "UTC", // always UTC in v0
		"tz", "utc",
		"TZH", "-07",
		"TZM", "00",
		"AM", "PM",
		"PM", "PM",
		"am", "pm",
		"pm", "pm",
		"A.M.", "PM",
		"P.M.", "PM",
		"Q", "", // quarter — not supported in Go format
		"WW", "", // week of year — not directly supported
		"IW", "", // ISO week
		"CC", "", // century
		"J", "", // Julian day
		"SSSSS", "", // seconds past midnight
		"SSSS", "",
		"Y,YYY", "", // year with comma
		"OF", "-07:00",
		"TZO", "-07:00",
	)
	return replacer.Replace(pg)
}

// evalDateTrunc implements date_trunc(field, source) → timestamp.
// Truncates a timestamp to the specified field granularity. M0097-0004.
func evalDateTrunc(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 2 {
		return NullDatum, nil
	}
	fieldArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || fieldArg.IsNull() {
		return NullDatum, nil
	}
	src, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil || src.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindTime {
		if src.Kind == KindString {
			if parsed, perr := parseCopyTimestamp(src.StringValue()); perr == nil {
				src = NewTimeDatum(parsed)
			}
		}
		if src.Kind != KindTime {
			return NullDatum, nil
		}
	}
	t := src.TimeValue().UTC()
	switch strings.ToLower(strings.TrimSpace(fieldArg.StringValue())) {
	case "microseconds":
		t = t.Truncate(time.Microsecond)
	case "milliseconds":
		t = t.Truncate(time.Millisecond)
	case "second":
		t = t.Truncate(time.Second)
	case "minute":
		t = t.Truncate(time.Minute)
	case "hour":
		t = t.Truncate(time.Hour)
	case "day":
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	case "week":
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7
		}
		t = time.Date(t.Year(), t.Month(), t.Day()-wd+1, 0, 0, 0, 0, t.Location())
	case "month":
		t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	case "quarter":
		m := t.Month()
		qm := ((m-1)/3)*3 + 1
		t = time.Date(t.Year(), qm, 1, 0, 0, 0, 0, t.Location())
	case "year":
		t = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	case "decade":
		y := (t.Year() / 10) * 10
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	case "century":
		y := ((t.Year() - 1) / 100) * 100
		if y < 0 {
			y = ((t.Year()) / 100) * 100
		} else {
			y++
		}
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	case "millennium":
		y := ((t.Year() - 1) / 1000) * 1000
		if y < 0 {
			y = (t.Year() / 1000) * 1000
		} else {
			y++
		}
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	}
	return NewTimeDatum(t), nil
}

// evalAge implements age(ts) and age(ts2, ts1) → interval. M0097-0004.
func evalAge(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	var ts1, ts2 time.Time
	switch len(x.Args) {
	case 1:
		d, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || d.IsNull() || d.Kind != KindTime {
			return NullDatum, nil
		}
		ts1 = d.TimeValue().UTC()
		ts2 = ctx.Now.UTC()
	case 2:
		d2, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || d2.IsNull() || d2.Kind != KindTime {
			return NullDatum, nil
		}
		d1, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil || d1.IsNull() || d1.Kind != KindTime {
			return NullDatum, nil
		}
		ts2 = d2.TimeValue().UTC()
		ts1 = d1.TimeValue().UTC()
	default:
		return NullDatum, nil
	}
	// Compute year/month/day difference the PostgreSQL way.
	years := ts2.Year() - ts1.Year()
	months := int(ts2.Month()) - int(ts1.Month())
	days := ts2.Day() - ts1.Day()
	if days < 0 {
		months--
		// Days in the month prior to ts2.
		prevMonth := time.Date(ts2.Year(), ts2.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		days += prevMonth.Day()
	}
	if months < 0 {
		years--
		months += 12
	}
	totalMonths := int32(years*12 + months)
	return NewIntervalDatum(totalMonths, int32(days)), nil
}

// buildIndexDefString delegates to catalog.BuildIndexDef. M0097-0023.
func buildIndexDefString(idx *catalog.Index) string {
	return catalog.BuildIndexDef(idx)
}

// buildConstraintDefString builds the pg_get_constraintdef text for a
// UNIQUE, PRIMARY KEY, or EXCLUDE constraint backed by idx. M0097-0023.
func buildConstraintDefString(idx *catalog.Index) string {
	if idx.IsExclusion {
		// EXCLUDE USING method (col WITH op) [INCLUDE (cols)]
		var pairs []string
		for _, col := range idx.Columns {
			op := idx.ExclusionOp
			if op == "" {
				op = "="
			}
			pairs = append(pairs, col+" WITH "+op)
		}
		def := "EXCLUDE USING " + idx.Method + " (" + strings.Join(pairs, ", ") + ")"
		if len(idx.IncludeColumns) > 0 {
			def += " INCLUDE (" + strings.Join(idx.IncludeColumns, ", ") + ")"
		}
		// Partial EXCLUDE WHERE predicate — pg_get_constraintdef renders the
		// exclusion def via pg_get_indexdef_worker, which appends ` WHERE (%s)`
		// (ruleutils.c:1564) after the operator/INCLUDE list and BEFORE the
		// DEFERRABLE clauses that the shared tail adds. PredicateString already
		// carries the fully-parenthesized predicate (defaultExprToSQL), matching
		// PG's `WHERE (pred)`. DU-002 slice 310.
		if idx.PredicateString != "" {
			def += " WHERE " + idx.PredicateString
		}
		// DEFERRABLE [INITIALLY DEFERRED] — ruleutils.c appends ` DEFERRABLE` for a
		// deferrable EXCLUDE constraint (after the WHERE predicate, which goopg does
		// not yet emit) and ` INITIALLY DEFERRED` when initially deferred (INITIALLY
		// IMMEDIATE is the default, omitted like PG). DU-002 slice 143.
		if idx.Deferrable {
			def += " DEFERRABLE"
			if idx.InitiallyDeferred {
				def += " INITIALLY DEFERRED"
			}
		}
		return def
	}
	keyCols := "(" + strings.Join(idx.Columns, ", ") + ")"
	var keyword string
	if idx.Primary {
		keyword = "PRIMARY KEY"
	} else {
		keyword = "UNIQUE"
	}
	def := keyword
	// NULLS NOT DISTINCT (PG 15+) precedes the column list for a UNIQUE
	// constraint — ruleutils.c pg_get_constraintdef_worker emits
	// `UNIQUE NULLS NOT DISTINCT (cols)` (only for CONSTRAINT_UNIQUE, never a
	// PRIMARY KEY whose columns are already NOT NULL). DU-002 slice 135.
	if !idx.Primary && idx.NullsNotDistinct {
		def += " NULLS NOT DISTINCT"
	}
	def += " " + keyCols
	if len(idx.IncludeColumns) > 0 {
		def += " INCLUDE (" + strings.Join(idx.IncludeColumns, ", ") + ")"
	}
	// DEFERRABLE [INITIALLY DEFERRED] — ruleutils.c appends ` DEFERRABLE` for a
	// deferrable constraint and additionally ` INITIALLY DEFERRED` when the
	// constraint is initially deferred (INITIALLY IMMEDIATE is the default and is
	// omitted, like PG). DU-002 slice 139.
	if idx.Deferrable {
		def += " DEFERRABLE"
		if idx.InitiallyDeferred {
			def += " INITIALLY DEFERRED"
		}
	}
	return def
}

// buildPartitionConstraintDef renders the pg_get_partition_constraintdef text
// for a single-level partition child, given its parent (the table carrying
// PARTITION BY) and the child's own bound. Dispatches to a per-strategy local
// builder (LIST / RANGE / HASH), each mirroring the matching
// get_qual_for_{list,range,hash} in postgres/src/backend/partitioning/partbounds.c.
// Returns "" for any case out of scope for this slice: an expression-based
// partition key, or a multi-column RANGE key (see the per-strategy builders).
// Design 0134-0005ag; option 2 (local builder) — deliberately independent of
// the shared defaultExprToSQL deparser (operators_ddl.go), whose *IsNullExpr
// case does not self-parenthesize and is shared with the CHECK-constraint and
// index-predicate deparse paths.
func buildPartitionConstraintDef(parent *catalog.Table, pb catalog.PartitionBound) string {
	keys := parent.PartitionKey
	if len(keys) == 0 {
		return ""
	}
	for i := range keys {
		if i < len(parent.PartitionKeyExprs) && parent.PartitionKeyExprs[i] != nil {
			// Expression-based partition key — deferred (M0134-0005ag ledger row).
			return ""
		}
	}
	switch strings.ToUpper(parent.PartitionMethod) {
	case "LIST":
		return buildListPartitionConstraintDef(keys, pb)
	case "RANGE":
		return buildRangePartitionConstraintDef(keys, pb)
	case "HASH":
		return buildHashPartitionConstraintDef(parent.OID, keys, pb)
	}
	return ""
}

// buildListPartitionConstraintDef mirrors get_qual_for_list (partbounds.c:4065)
// for single-column LIST partitioning (PG only supports single-column LIST
// keys — Assert(key->partnatts == 1) — so len(keys) is always 1 for a
// well-formed catalog; the length check is defensive).
//
// Non-NULL values build an equality (single value) or `= ANY (ARRAY[...])`
// (multiple values) OpExpr/ScalarArrayOpExpr, ANDed with a `keyCol IS NOT
// NULL` NullTest. If the LIST bound includes NULL (list_has_null), PG instead
// ORs a `keyCol IS NULL` NullTest with the value-equality expr (get_qual_for_
// list :4176-4200). Every deparsed AND_EXPR/OR_EXPR self-wraps in an extra
// outer paren pair on top of each arm's own parens because PRETTYFLAG_PAREN
// is not set (ruleutils.c:9465 — see the design doc's "paren hazard"
// section); replicated here by always parenthesizing each atom and adding
// the outer wrap only when two atoms are joined.
func buildListPartitionConstraintDef(keys []string, pb catalog.PartitionBound) string {
	if len(keys) != 1 {
		return ""
	}
	k := keys[0]
	var nonNullVals []string
	hasNull := false
	for i, raw := range pb.InValues {
		if strings.EqualFold(raw, "null") {
			hasNull = true
			continue
		}
		var lit string
		if i < len(pb.InValueLiterals) {
			lit = pb.InValueLiterals[i]
		}
		if lit == "" {
			// Can't safely re-render this value as a SQL literal — bail to
			// NULL rather than emit a wrong-but-plausible qual.
			return ""
		}
		nonNullVals = append(nonNullVals, lit)
	}
	var opexpr string
	switch len(nonNullVals) {
	case 0:
	case 1:
		opexpr = "(" + k + " = " + nonNullVals[0] + ")"
	default:
		opexpr = "(" + k + " = ANY (ARRAY[" + strings.Join(nonNullVals, ", ") + "]))"
	}
	if hasNull {
		nulltest := "(" + k + " IS NULL)"
		if opexpr == "" {
			return nulltest
		}
		return "(" + nulltest + " OR " + opexpr + ")"
	}
	nulltest := "(" + k + " IS NOT NULL)"
	if opexpr == "" {
		return nulltest
	}
	return "(" + nulltest + " AND " + opexpr + ")"
}

// buildRangePartitionConstraintDef mirrors get_qual_for_range (partbounds.c:
// 4274) for the common single-column case: `(keyCol IS NOT NULL) AND (keyCol
// >= lower) AND (keyCol < upper)`, omitting either comparison arm when its
// bound is MINVALUE/MAXVALUE (get_range_key_properties returns no Const for
// an unbounded edge, so no arm is emitted for it). Multi-column RANGE keys
// require the lexicographic OR-chain construction in get_qual_for_range
// (partbounds.c:4236-4266 comment) — out of scope for this slice (no
// constraints.sql fixture forces it); returns "" (deferred, M0134-0005ag
// ledger row).
func buildRangePartitionConstraintDef(keys []string, pb catalog.PartitionBound) string {
	if len(keys) != 1 {
		return ""
	}
	if len(pb.FromValues) != 1 || len(pb.ToValues) != 1 {
		return ""
	}
	if len(pb.FromUnbounded) != 1 || len(pb.ToUnbounded) != 1 {
		// Legacy bound predating DU-002 slice 261 (no explicit unbounded
		// flags) — bail to NULL rather than guess from the sentinel string.
		return ""
	}
	k := keys[0]
	fromUnbounded := pb.FromUnbounded[0]
	toUnbounded := pb.ToUnbounded[0]
	var fromLit, toLit string
	if !fromUnbounded {
		if len(pb.FromValueLiterals) != 1 || pb.FromValueLiterals[0] == "" {
			return ""
		}
		fromLit = pb.FromValueLiterals[0]
	}
	if !toUnbounded {
		if len(pb.ToValueLiterals) != 1 || pb.ToValueLiterals[0] == "" {
			return ""
		}
		toLit = pb.ToValueLiterals[0]
	}
	arms := []string{"(" + k + " IS NOT NULL)"}
	if !fromUnbounded {
		arms = append(arms, "("+k+" >= "+fromLit+")")
	}
	if !toUnbounded {
		arms = append(arms, "("+k+" < "+toLit+")")
	}
	if len(arms) == 1 {
		return arms[0]
	}
	return "(" + strings.Join(arms, " AND ") + ")"
}

// buildHashPartitionConstraintDef mirrors get_qual_for_hash (partbounds.c:
// 3982): a single call to the built-in satisfies_hash_partition(parentoid,
// modulus, remainder, k1, k2, ...). No NOT NULL conjunct (HASH partitioning
// has no DEFAULT partition in PG, and NULL routes to a specific bucket like
// any other value), and no self-parenthesization at the top level (a bare
// FuncExpr does not get PG's need_paren wrap).
func buildHashPartitionConstraintDef(parentOID uint32, keys []string, pb catalog.PartitionBound) string {
	if len(keys) == 0 {
		return ""
	}
	args := []string{
		fmt.Sprintf("%d", parentOID),
		fmt.Sprintf("%d", pb.Modulus),
		fmt.Sprintf("%d", pb.Remainder),
	}
	args = append(args, keys...)
	return "satisfies_hash_partition(" + strings.Join(args, ", ") + ")"
}

// buildForeignKeyDefString builds the pg_get_constraintdef text for a FOREIGN
// KEY constraint, mirroring ruleutils.c pg_get_constraintdef_worker. pg_dump
// runs with search_path=” so the referenced relation is fully schema-qualified
// (`REFERENCES public.foo(id)`). Referential actions other than NO ACTION and a
// DEFERRABLE clause are appended; MATCH SIMPLE (the default) is omitted, as PG
// does. A trailing ` NOT VALID` is appended for an unvalidated FK
// (convalidated='f'). DU-002 slices 51, 307.
func buildForeignKeyDefString(ctx *Context, im *catalog.InMemory, fk catalog.ForeignKey, dbOid ...uint32) string {
	var refTbl *catalog.Table
	for _, t := range im.AllTables() {
		if t.Virtual || t.OID == 0 {
			continue
		}
		if strings.EqualFold(t.Name, fk.RefTable) {
			refTbl = t
			break
		}
	}
	refSchema := "public"
	refName := fk.RefTable
	refCols := fk.RefColumns
	if refTbl != nil {
		if refTbl.Schema != "" {
			refSchema = refTbl.Schema
		}
		refName = refTbl.Name
		if len(refCols) == 0 {
			// Default to the referenced table's primary-key columns.
			for _, idx := range im.IndexesOnTable(refTbl, dbOid...) {
				if idx.Primary {
					refCols = idx.Columns
					break
				}
			}
		}
	}
	var refQualified string
	if refSchema != "" && !RegObjectSchemaVisible(ctx, refSchema) {
		refQualified = refSchema + "." + refName
	} else {
		refQualified = refName
	}
	def := "FOREIGN KEY (" + strings.Join(fk.Columns, ", ") + ") REFERENCES " +
		refQualified + "(" + strings.Join(refCols, ", ") + ")"
	// MATCH FULL is emitted between the REFERENCES column list and the ON
	// UPDATE/DELETE clauses, mirroring pg_get_constraintdef_worker
	// (ruleutils.c). MATCH SIMPLE (the default, confmatchtype='s') and the
	// unimplemented MATCH PARTIAL produce no clause. DU-002 slice 309.
	if fk.MatchFull {
		def += " MATCH FULL"
	}
	if act := fkActionClause(fk.OnUpdate); act != "" {
		def += " ON UPDATE " + act
	}
	if act := fkActionClause(fk.OnDelete); act != "" {
		def += " ON DELETE " + act
		// PG15 confdelsetcols: an `ON DELETE SET NULL|DEFAULT` restricted to a
		// column subset appends ` (col, …)` after the action keyword
		// (ruleutils.c:2376, decompile_column_index_array). Only SET NULL / SET
		// DEFAULT can carry it. DU-002 slice 311.
		if len(fk.OnDeleteSetCols) > 0 &&
			(fk.OnDelete == parser.FKActionSetNull || fk.OnDelete == parser.FKActionSetDefault) {
			def += " (" + strings.Join(fk.OnDeleteSetCols, ", ") + ")"
		}
	}
	if fk.Deferrable {
		def += " DEFERRABLE"
		if fk.InitiallyDeferred {
			def += " INITIALLY DEFERRED"
		}
	}
	// The shared tail of pg_get_constraintdef_worker (ruleutils.c:2601-2604):
	// "Validated status is irrelevant when the constraint is NOT ENFORCED" —
	// conenforced is checked FIRST, and NOT VALID is only considered when the
	// constraint IS enforced. A NOT-VALID FK (pg_constraint.convalidated='f')
	// carries a trailing ` NOT VALID`; pg_dump re-emits either verbatim so the
	// restored FK matches. DU-002 slices 307, 431.
	if fk.NotEnforced {
		def += " NOT ENFORCED"
	} else if fk.NotValid {
		def += " NOT VALID"
	}
	return def
}

// fkActionClause renders the SQL keyword for a non-default FK referential
// action; NO ACTION (the default) returns "" so the clause is omitted. DU-002 slice 51.
func fkActionClause(a parser.FKAction) string {
	switch a {
	case parser.FKActionRestrict:
		return "RESTRICT"
	case parser.FKActionCascade:
		return "CASCADE"
	case parser.FKActionSetNull:
		return "SET NULL"
	case parser.FKActionSetDefault:
		return "SET DEFAULT"
	default:
		return ""
	}
}

// buildTriggerDefString reconstructs the CREATE TRIGGER statement for a
// row-/statement-level trigger, mirroring ruleutils.c
// pg_get_triggerdef_worker. pg_dump's getTriggers selects
// pg_get_triggerdef(t.oid, false) and emits the result verbatim (plus a
// trailing semicolon), so the spacing must match PG exactly:
//
//	CREATE TRIGGER <name> {BEFORE|AFTER|INSTEAD OF} <ev>[ OR <ev>…]
//	    ON <schema>.<table> FOR EACH {ROW|STATEMENT}
//	    EXECUTE FUNCTION <schema>.<func>(<'arg'>…)
//
// Events are emitted in PG's fixed order (INSERT, DELETE, UPDATE, TRUNCATE)
// regardless of the order they were declared. The target table and trigger
// function are schema-qualified because pg_dump runs with search_path=''.
// A column-specific `UPDATE OF col1, col2` list is reconstructed after the
// UPDATE event (DU-002 slice 326). A CONSTRAINT TRIGGER emits `CREATE CONSTRAINT
// TRIGGER` plus a `[NOT ]DEFERRABLE INITIALLY {DEFERRED|IMMEDIATE}` clause after
// the ON-table name (DU-002 slice 327). goopg's parser does not yet capture WHEN
// or REFERENCING transition-table clauses, so neither is emitted.
// DU-002 slice 319.
func buildTriggerDefString(tbl *catalog.Table, trig catalog.Trigger) string {
	var b strings.Builder
	// A CONSTRAINT TRIGGER deparses as `CREATE CONSTRAINT TRIGGER` (ruleutils.c
	// pg_get_triggerdef_worker, gated on a valid tgconstraint). DU-002 slice 327.
	if trig.IsConstraint {
		b.WriteString("CREATE CONSTRAINT TRIGGER ")
	} else {
		b.WriteString("CREATE TRIGGER ")
	}
	b.WriteString(trig.Name)
	b.WriteByte(' ')
	switch trig.Timing {
	case catalog.TriggerBefore:
		b.WriteString("BEFORE")
	case catalog.TriggerInsteadOf:
		b.WriteString("INSTEAD OF")
	default:
		b.WriteString("AFTER")
	}
	has := make(map[string]bool, len(trig.Events))
	for _, ev := range trig.Events {
		has[strings.ToLower(ev)] = true
	}
	first := true
	emit := func(kw string) {
		if first {
			b.WriteByte(' ')
			b.WriteString(kw)
			first = false
		} else {
			b.WriteString(" OR ")
			b.WriteString(kw)
		}
	}
	if has["insert"] {
		emit("INSERT")
	}
	if has["delete"] {
		emit("DELETE")
	}
	if has["update"] {
		emit("UPDATE")
		// A column-specific UPDATE trigger appends ` OF col1, col2` right after
		// the UPDATE event (ruleutils.c pg_get_triggerdef_worker). Column names
		// are quoted only when required. DU-002 slice 326.
		if len(trig.UpdateColumns) > 0 {
			b.WriteString(" OF ")
			for i, col := range trig.UpdateColumns {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(pgQuoteIdent(col))
			}
		}
	}
	if has["truncate"] {
		emit("TRUNCATE")
	}
	schema := tbl.Schema
	if schema == "" {
		schema = "public"
	}
	b.WriteString(" ON ")
	b.WriteString(schema)
	b.WriteByte('.')
	b.WriteString(tbl.Name)
	b.WriteByte(' ')
	// A constraint trigger emits its deferrability right after the ON-table name
	// (ruleutils.c pg_get_triggerdef_worker): `[NOT ]DEFERRABLE INITIALLY
	// {DEFERRED|IMMEDIATE} `. PG always spells out the full clause even for the
	// NOT DEFERRABLE INITIALLY IMMEDIATE default. DU-002 slice 327.
	if trig.IsConstraint {
		if !trig.Deferrable {
			b.WriteString("NOT ")
		}
		b.WriteString("DEFERRABLE INITIALLY ")
		if trig.InitDeferred {
			b.WriteString("DEFERRED ")
		} else {
			b.WriteString("IMMEDIATE ")
		}
	}
	// REFERENCING transition tables (ruleutils.c pg_get_triggerdef_worker): the
	// OLD/NEW statement-level row sets, emitted between the deferrability clause
	// and FOR EACH ROW as `REFERENCING OLD TABLE AS <o> NEW TABLE AS <n> `.
	// Either or both names may be present. DU-002 slice 328.
	if trig.OldTransitionTable != "" || trig.NewTransitionTable != "" {
		b.WriteString("REFERENCING ")
		if trig.OldTransitionTable != "" {
			b.WriteString("OLD TABLE AS ")
			b.WriteString(pgQuoteIdent(trig.OldTransitionTable))
			b.WriteByte(' ')
		}
		if trig.NewTransitionTable != "" {
			b.WriteString("NEW TABLE AS ")
			b.WriteString(pgQuoteIdent(trig.NewTransitionTable))
			b.WriteByte(' ')
		}
	}
	if trig.ForEachRow {
		b.WriteString("FOR EACH ROW ")
	} else {
		b.WriteString("FOR EACH STATEMENT ")
	}
	// A WHEN qualification deparses right after FOR EACH and before EXECUTE
	// FUNCTION (ruleutils.c pg_get_triggerdef_worker): `WHEN (<condition>) `. PG
	// builds OLD/NEW range-table entries so the condition's column references
	// render with lowercased `old.`/`new.` qualifiers; goopg's parser already
	// lowercases the unquoted qualifier onto the *ColumnRef, and defaultExprToSQL
	// preserves it (unlike the bare-column catalog.formatExprForAttrdef twin).
	// get_rule_expr (prettyFlags=0) fully parenthesizes the boolean OpExpr, so a
	// comparison renders as `(new.b <> old.b)` and the WHEN wrapper adds the outer
	// pair → `WHEN ((new.b <> old.b))`. DU-002 slice 329.
	if trig.WhenExpr != nil {
		b.WriteString("WHEN (")
		b.WriteString(defaultExprToSQL(trig.WhenExpr))
		b.WriteString(") ")
	}
	fnSchema := trig.FuncSchema
	if fnSchema == "" {
		fnSchema = "public"
	}
	b.WriteString("EXECUTE FUNCTION ")
	b.WriteString(fnSchema)
	b.WriteByte('.')
	b.WriteString(trig.FuncName)
	b.WriteByte('(')
	for i, a := range trig.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", "''"))
		b.WriteByte('\'')
	}
	b.WriteByte(')')
	return b.String()
}

// buildRuleDefString reconstructs the CREATE RULE statement for a DO-NOTHING
// rewrite rule, byte-identical to PostgreSQL's single-argument pg_get_ruledef
// (PRETTYFLAG_INDENT) which pg_dump's dumpRule emits verbatim. Both the
// unconditional form (DU-002 slice 324) and the conditional `WHERE (qual)` form
// (DU-002 slice 359) are modelled; an action command is never deparsed (see
// catalog.RuleInfo). PG's pretty-printer lays a conditional rule out as:
//
//	CREATE RULE r AS
//	    ON UPDATE TO public.t
//	   WHERE (old.a <> new.a) DO INSTEAD NOTHING;
//
// i.e. the WHERE clause goes on its own line with a 3-space indent and the DO
// action trails it on the SAME line; the unconditional form keeps DO on the
// `ON … TO …` line. The qual text is already the canonical parenthesized form
// stored by execCreateRule.
func buildRuleDefString(tbl *catalog.Table, r catalog.RuleInfo) string {
	schema := tbl.Schema
	if schema == "" {
		schema = "public"
	}
	var b strings.Builder
	b.WriteString("CREATE RULE ")
	b.WriteString(r.Name)
	b.WriteString(" AS\n    ON ")
	b.WriteString(strings.ToUpper(r.Event))
	b.WriteString(" TO ")
	b.WriteString(schema)
	b.WriteByte('.')
	b.WriteString(tbl.Name)
	if r.Qual != "" {
		b.WriteString("\n   WHERE ")
		b.WriteString(r.Qual)
	}
	b.WriteString(" DO ")
	if r.Instead {
		b.WriteString("INSTEAD ")
	}
	b.WriteString("NOTHING;")
	return b.String()
}

// evalMakeDate implements make_date(year, month, day) → date. M0097-0004.
func evalMakeDate(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 3 {
		return NullDatum, nil
	}
	yArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || yArg.IsNull() {
		return NullDatum, nil
	}
	mArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil || mArg.IsNull() {
		return NullDatum, nil
	}
	dArg, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil || dArg.IsNull() {
		return NullDatum, nil
	}
	t := time.Date(int(yArg.Int), time.Month(mArg.Int), int(dArg.Int), 0, 0, 0, 0, time.UTC)
	return NewTimeDatum(t), nil
}

// evalMakeTimestamp implements make_timestamp/make_timestamptz(y,m,d,h,min,sec).
// M0097-0004.
func evalMakeTimestamp(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 6 {
		return NullDatum, nil
	}
	args := make([]int64, 6)
	for i := 0; i < 6; i++ {
		d, err := evalExprSlot(x.Args[i], slot, ctx)
		if err != nil || d.IsNull() {
			return NullDatum, nil
		}
		args[i] = d.Int
	}
	t := time.Date(int(args[0]), time.Month(args[1]), int(args[2]),
		int(args[3]), int(args[4]), int(args[5]), 0, time.UTC)
	return NewTimeDatum(t), nil
}

// evalMakeTime implements make_time(h, min, sec) → time. M0097-0004.
func evalMakeTime(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 3 {
		return NullDatum, nil
	}
	h, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || h.IsNull() {
		return NullDatum, nil
	}
	m, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil || m.IsNull() {
		return NullDatum, nil
	}
	s, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil || s.IsNull() {
		return NullDatum, nil
	}
	t := time.Date(1970, 1, 1, int(h.Int), int(m.Int), int(s.Int), 0, time.UTC)
	return NewTimeDatum(t), nil
}

// regexpFirstMatchArray computes the text[] datum for the FIRST match of re
// against s, mirroring PostgreSQL's regexp_match/regexp_matches element
// semantics (postgres/src/backend/utils/adt/regexp.c setup_regexp_matches):
// when the pattern has parenthesized capture groups, the array holds those
// groups (NOT the overall match); with no groups, the array's sole element
// is the whole match. A capture group that did not participate in the match
// (e.g. the losing side of a `(a)|(b)` alternation) yields SQL NULL, not "".
// Does not perform PG array-literal escaping/quoting of element contents
// (pre-existing simplification, unchanged from this function's predecessor).
func regexpFirstMatchArray(re *regexp.Regexp, s string) Datum {
	idx := re.FindStringSubmatchIndex(s)
	if idx == nil {
		return NullDatum
	}
	return regexpMatchArrayDatum(re, s, idx)
}

// regexpMatchArrayDatum builds the text[] array-literal Datum for one match,
// given a submatch index slice as returned by FindStringSubmatchIndex /
// FindAllStringSubmatchIndex. Shared by regexpFirstMatchArray (single match)
// and regexpAllMatchesArrays (SRF, one call per match).
func regexpMatchArrayDatum(re *regexp.Regexp, s string, idx []int) Datum {
	if re.NumSubexp() == 0 {
		return NewStringDatum(formatTextArray([]string{s[idx[0]:idx[1]]}))
	}
	elems := make([]string, re.NumSubexp())
	nulls := make([]bool, re.NumSubexp())
	for i := 1; i <= re.NumSubexp(); i++ {
		lo, hi := idx[2*i], idx[2*i+1]
		if lo < 0 {
			nulls[i-1] = true
		} else {
			elems[i-1] = s[lo:hi]
		}
	}
	return NewStringDatum(formatTextArrayWithNulls(elems, nulls))
}

// regexpAllMatchesArrays mirrors regexp_matches(string, pattern, 'g')'s SRF
// semantics (postgres/src/backend/utils/adt/regexp.c setup_regexp_matches):
// with the 'g' flag, one Datum per match against s; without it, PG's SRF form
// still yields at most one row (the first match), same as the scalar case.
// Returns nil (zero rows) when there is no match, matching PG's SRF row count
// (unlike the scalar-position fallback, which returns SQL NULL for no match).
func regexpAllMatchesArrays(re *regexp.Regexp, s string, global bool) []Datum {
	if !global {
		if d := regexpFirstMatchArray(re, s); !d.IsNull() {
			return []Datum{d}
		}
		return nil
	}
	allIdx := re.FindAllStringSubmatchIndex(s, -1)
	if len(allIdx) == 0 {
		return nil
	}
	out := make([]Datum, len(allIdx))
	for i, idx := range allIdx {
		out[i] = regexpMatchArrayDatum(re, s, idx)
	}
	return out
}

// pgRegexFlagsToGoModifiers translates a PG regexp_* "flags" argument
// (postgres/src/backend/utils/adt/regexp.c:parse_re_flags, :385-449) into a
// Go regexp inline-modifier prefix plus the 'g'/global bit, which Go's
// regexp package has no inline equivalent for (call sites already loop for
// 'g' themselves). Per the design doc
// (docs/design/m0134-0070-regexp-flags-and-family.md):
//   - 'i' (case-insensitive)                       → Go (?i)
//   - 'm'/'n' (newline-sensitive) and PG's 'p'/'w'
//     partial-newline-sensitive variants            → Go (?m) (goopg does not
//     distinguish full vs. partial newline-sensitivity beyond ^/$ anchoring;
//     documented simplification, not a silent drop)
//   - 's' (PG's "single line, \n ordinary", i.e. PG's *default* newline
//     behavior) is a naming collision with Go's `(?s)` ("dot matches
//     newline") — they are NOT the same thing, so 's' must NOT map to Go's
//     (?s). It is a no-op here since goopg's default already matches PG's 's'
//     behavior.
//   - 'c'/'e'/'b'/'t'/'q'/'x' are accepted (PG ARE-mode/syntax selectors) but
//     not yet meaningfully implemented — NOT flagged unknown, matching PG's
//     own acceptance of them as valid selectors.
//   - 'g' sets global=true; not folded into goFlags.
//   - Any other character → 22023 (ERRCODE_INVALID_PARAMETER_VALUE; PG's own
//     parse_re_flags default case raises invalid_parameter_value, not
//     invalid_regular_expression, despite the ereport wording).
func pgRegexFlagsToGoModifiers(flags string) (goFlags string, global bool, err error) {
	var caseInsensitive, newlineSensitive bool
	for _, r := range flags {
		switch r {
		case 'g':
			global = true
		case 'i':
			caseInsensitive = true
		case 'm', 'n', 'p', 'w':
			newlineSensitive = true
		case 's', 'c', 'e', 'b', 't', 'q', 'x':
			// Accepted PG flag chars with no goopg-side effect (yet).
		default:
			return "", false, &ExecError{Code: "22023",
				Message: fmt.Sprintf("invalid regular expression option: %q", string(r))}
		}
	}
	if !caseInsensitive && !newlineSensitive {
		return "", global, nil
	}
	var b strings.Builder
	b.WriteString("(?")
	if caseInsensitive {
		b.WriteByte('i')
	}
	if newlineSensitive {
		b.WriteByte('m')
	}
	b.WriteByte(')')
	return b.String(), global, nil
}

// regexpLocalDotMatchesNewline computes PG's REG_NEWLINE-derived
// dot-matches-newline behavior LOCALLY for the four M0134-0070 Round E
// functions (regexp_count/like/instr/substr) only — it does NOT change
// pgRegexFlagsToGoModifiers's own return value (that function's "'s' is a
// no-op" contract is pinned by TestPgRegexFlagsToGoModifiers and shared by
// every other regexp_* call site, out of this slice's scope).
// postgres/src/backend/utils/adt/regexp.c:385-449 parse_re_flags: cflags
// default to REG_ADVANCED only (REG_NEWLINE unset), meaning PG's *default*
// (and explicit 's', "single line, \n ordinary") behavior is that `.`
// (and `[^...]`) DO match a newline; 'n'/'m' (REG_NEWLINE) and 'p'
// (REG_NLSTOP only) make `.` stop at a newline; 'w' (REG_NLANCH only)
// leaves `.` newline-matching but is otherwise handled by the shared
// (?m) ^/$ anchoring pgRegexFlagsToGoModifiers already applies. Go's RE2
// requires an explicit (?s) for `.` to match \n (its default is the
// opposite of PG's), so this reports whether that group must be added.
func regexpLocalDotMatchesNewline(flags string) bool {
	dotStops := false
	for _, r := range flags {
		switch r {
		case 'n', 'm', 'p':
			dotStops = true
		case 's':
			dotStops = false
		}
	}
	return !dotStops
}

// regexpApplyExpandedWhitespace implements the 'x' ("expanded"/free-spacing,
// REG_EXPANDED) PG regex flag (postgres/src/backend/utils/adt/regexp.c:439
// parse_re_flags) for the four M0134-0070 Round E functions: when 'x' is
// present, unescaped whitespace and '#'-to-end-of-line comments in the
// pattern are insignificant. Go's RE2 has no REG_EXPANDED equivalent, so
// this strips them from the pattern text before compiling — a
// simplification of PG's actual ARE tokenizer (e.g. it does not exempt
// bracket expressions), sufficient for the strings.sql fixture this slice
// targets.
func regexpApplyExpandedWhitespace(pattern, flags string) string {
	if !strings.ContainsRune(flags, 'x') {
		return pattern
	}
	var b strings.Builder
	inComment := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if inComment {
			if c == '\n' {
				inComment = false
				b.WriteByte(c)
			}
			continue
		}
		if c == '\\' && i+1 < len(pattern) {
			b.WriteByte(c)
			b.WriteByte(pattern[i+1])
			i++
			continue
		}
		if c == '#' {
			inComment = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// pgRegexpReplacementTemplate converts a PostgreSQL regexp_replace()
// replacement string into a Go regexp expansion template usable with
// (*regexp.Regexp).Expand{String}/ReplaceAllString. Mirrors
// postgres/src/backend/utils/adt/varlena.c:4357-4447
// (appendStringInfoRegexpSubstr), which walks the replacement string
// splitting on literal '\': "\1".."\9" -> single-digit backreference index
// (never multi-digit -- "*p - '0'" reads exactly one byte), "\&" -> whole
// match (pmatch[0]), "\\" -> emits one literal '\' and continues (does NOT
// treat the following char as an escape), any other "\c" -> emits a literal
// '\' then falls through to normal-text copying of 'c' (net effect: '\'
// followed by 'c', both literal, unchanged from input), and a trailing lone
// '\' at end of string is emitted as literal '\'. M0134-0070 Round G.
func pgRegexpReplacementTemplate(replacement string) string {
	// First pass: escape any literal '$' in the original replacement text so
	// Go's expander doesn't misinterpret it as a template reference.
	replacement = strings.ReplaceAll(replacement, "$", "$$")

	// Second pass: scan byte-by-byte for PG's '\'-escape grammar.
	var b strings.Builder
	b.Grow(len(replacement))
	for i := 0; i < len(replacement); i++ {
		c := replacement[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(replacement) {
			// Trailing lone '\' at end of string: emitted as literal '\'.
			b.WriteByte('\\')
			break
		}
		next := replacement[i+1]
		switch {
		case next >= '1' && next <= '9':
			b.WriteString("${")
			b.WriteByte(next)
			b.WriteByte('}')
			i++
		case next == '&':
			b.WriteString("${0}")
			i++
		case next == '\\':
			// "\\" emits one literal '\' and does NOT consume the next char
			// as an escape target -- advance past both backslashes only.
			b.WriteByte('\\')
			i++
		default:
			// Any other "\c": emit literal '\' then let the loop copy 'c'
			// through unchanged on the next iteration.
			b.WriteByte('\\')
		}
	}
	return b.String()
}

// regexpCompilePattern compiles a PG regexp_* pattern for the four
// M0134-0070 Round E functions, combining the shared pgRegexFlagsToGoModifiers
// output (goFlags — 'i'/(?i), 'm'/'n'/'p'/'w'/(?m)) with the LOCAL
// dot-matches-newline and expanded-whitespace handling above (both scoped to
// this slice's four call sites; see regexpLocalDotMatchesNewline's comment
// for why they aren't folded into the shared translator).
func regexpCompilePattern(pattern, flags, goFlags string) (*regexp.Regexp, error) {
	pattern = regexpApplyExpandedWhitespace(pattern, flags)
	pattern = pgPatternToGoRE2(pattern)
	prefix := goFlags
	if regexpLocalDotMatchesNewline(flags) {
		prefix = "(?s)" + prefix
	}
	return regexp.Compile(prefix + pattern)
}

// regexpInstrSubstrLocate implements the shared match-and-select core of
// regexp_instr() and regexp_substr() (postgres/src/backend/utils/adt/regexp.c
// :1198,1904 — near-identical start_search/n-th-match/subexpr selection,
// consolidated here so the two "pos" computations can't drift apart,
// M0134-0070 Round E). `start` is the 1-based char position to begin
// searching from; the search window is `s` sliced from that rune offset
// (mirrors evalSubstr's rune-window arithmetic — a documented simplification
// vs. PG's true start_search-offset-into-the-original-string approach: a
// `^`-anchored pattern would re-anchor at the window start here rather than
// the true string start). `n` selects the n-th non-overlapping match
// (1-based); `subexpr` selects a capture group within that match (0 = whole
// match). Returns ok=false — NOT an error — when n exceeds the match count,
// subexpr exceeds the pattern's capture-group count, or the selected group
// didn't participate in the match (regexp.c:1267-1273,1965-1971 "return
// 0/NULL" cases). so/eo are BYTE offsets into `window` (valid substring
// boundaries since match positions always land on rune boundaries for valid
// UTF-8 input).
func regexpInstrSubstrLocate(s, pattern, flags, goFlags string, start, n, subexpr int) (window string, so, eo int, ok bool, err error) {
	runes := []rune(s)
	winStart := start - 1
	if winStart > len(runes) {
		winStart = len(runes)
	}
	window = string(runes[winStart:])
	re, cerr := regexpCompilePattern(pattern, flags, goFlags)
	if cerr != nil {
		return "", 0, 0, false, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", cerr)}
	}
	npatterns := re.NumSubexp()
	if subexpr > npatterns {
		return window, 0, 0, false, nil
	}
	matches := re.FindAllStringSubmatchIndex(window, -1)
	if n > len(matches) {
		return window, 0, 0, false, nil
	}
	loc := matches[n-1]
	idx := subexpr * 2
	if idx+1 >= len(loc) {
		return window, 0, 0, false, nil
	}
	so, eo = loc[idx], loc[idx+1]
	if so < 0 || eo < 0 {
		return window, 0, 0, false, nil
	}
	return window, so, eo, true, nil
}

// regexpWindowCharPos converts a BYTE offset within `window` (as returned by
// regexpInstrSubstrLocate) to the 1-based char position within the ORIGINAL
// string that regexp_instr() reports, given the same `start` used to build
// the window — mirrors the byte→char idiom `case "strpos", "position"`
// already uses (expr.go ~11774), extended to add back the (start-1) runes
// the window skipped.
func regexpWindowCharPos(window string, start, byteOff int) int {
	return (start - 1) + len([]rune(window[:byteOff])) + 1
}

// evalRegexpMatchesSRF evaluates regexp_matches(string, pattern[, flags]) in
// SELECT-list SRF position (see projectSetOp.openSelectSrfMode). Invalid
// patterns / NULL string or pattern args yield zero rows rather than an
// error, matching the permissiveness of the pre-existing scalar case arm. An
// unrecognized flag character still raises 22023 (M0134-0070).
func evalRegexpMatchesSRF(sD, patD, flagsD Datum) ([]Datum, error) {
	if sD.IsNull() || patD.IsNull() {
		return nil, nil
	}
	flags := ""
	if !flagsD.IsNull() {
		flags = flagsD.StringValue()
	}
	goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
	if ferr != nil {
		return nil, ferr
	}
	pattern := goFlags + patD.StringValue()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil
	}
	return regexpAllMatchesArrays(re, sD.StringValue(), global), nil
}

// evalRegexpSplitToTable evaluates regexp_split_to_table(string, pattern[,
// flags]) in FROM-clause SRF position, one row per substring (plain text,
// not array-wrapped). Unlike evalRegexpMatchesSRF, this is strict like the
// scalar regexp_split_to_array case it mirrors: NULL string/pattern yields
// zero rows, but an explicit 'g' flag and an invalid pattern both raise
// errors rather than silently returning nothing — regexp_split_to_table and
// regexp_split_to_array share PG's setup_regexp_matches/split machinery
// (postgres/src/backend/utils/adt/regexp.c:1748-1797 rejects 'g', then
// forces glob=true internally so split always finds ALL matches; :1862-1897
// build_regexp_split_result — N matches → N+1 rows). M0134-0070 Round D.
func evalRegexpSplitToTable(sD, patD, flagsD Datum) ([]Datum, error) {
	if sD.IsNull() || patD.IsNull() {
		return nil, nil
	}
	flags := ""
	if !flagsD.IsNull() {
		flags = flagsD.StringValue()
	}
	goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
	if ferr != nil {
		return nil, ferr
	}
	if global {
		// regexp_split_to_table() rejects 'g' (regexp.c:1766-1773 — "User
		// mustn't specify 'g'"), then forces glob=true internally, i.e.
		// split always finds ALL matches regardless.
		return nil, &ExecError{Code: "22023",
			Message: `regexp_split_to_table() does not support the "global" option`}
	}
	pattern := goFlags + patD.StringValue()
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Unlike evalRegexpMatchesSRF's permissiveness (a matches-only
		// simplification), regexp_split_to_table follows the stricter
		// regexp_split_to_array precedent: no Pos, pure runtime evaluation
		// (RE_compile_and_cache has no errposition call). M0134-0070.
		return nil, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", err)}
	}
	parts := re.Split(sD.StringValue(), -1)
	vals := make([]Datum, len(parts))
	for i, s := range parts {
		vals[i] = NewStringDatum(s)
	}
	return vals, nil
}

// evalIsFinite implements isfinite(date/timestamp/timestamptz/interval),
// line-porting PG's date_finite / timestamp_finite / interval_finite
// (postgres/src/backend/utils/adt/{date,timestamp}.c): the result is FALSE
// only for a ±infinity sentinel (DATE_NOT_FINITE / TIMESTAMP_NOT_FINITE /
// INTERVAL_NOT_FINITE), TRUE for every other finite value. goopg carries the
// timestamp/date ±infinity sentinels on KindTime (INT64 extremes, TimeSubDate-
// agnostic) and the interval sentinels on KindInterval (unimplemented_feat
// #5(d-iv)), so both must be checked. NULL input propagates to NULL (isfinite
// is strict — no NotStrict marker on its pg_proc OIDs; see isfinite_test.go).
func evalIsFinite(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return NullDatum, nil
	}
	d, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || d.IsNull() {
		return NullDatum, nil
	}
	if d.IsTimestampNotFinite() || d.IsIntervalNotFinite() {
		return NewBoolDatum(false), nil
	}
	return NewBoolDatum(true), nil
}

// errIntervalRange is the SQLSTATE 22008 error PG raises
// (ERRCODE_DATETIME_VALUE_OUT_OF_RANGE, "interval out of range") when a justify
// step's day/month field overflows int32.
var errIntervalRange = &ExecError{Code: "22008", Message: "interval out of range"}

// evalJustify implements justify_hours()/justify_days()/justify_interval(),
// mirroring interval_justify_hours/interval_justify_days/
// interval_justify_interval (postgres/src/backend/utils/adt/timestamp.c). All
// three normalize a KindInterval's month/day/time (sub-day micros) fields into
// customary bounds. Since the interval carrier gained a real sub-day micros
// field (Datum.IntervalMicrosValue, populated by timestamp − timestamp and by
// sub-day literals), justify_hours is no longer the identity: it folds whole
// 24h chunks of the time field into days. M0097-0004 (extended 2026-07-11).
func evalJustify(name string, x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return NullDatum, nil
	}
	d, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || d.IsNull() || d.Kind != KindInterval {
		return d, err
	}
	months, days, micros := d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue()
	switch name {
	case "justify_hours":
		months, days, micros, err = justifyIntervalHours(months, days, micros)
	case "justify_days":
		// justify_days leaves the time field untouched; only rebalance
		// whole 30-day chunks of days into months.
		months, days = justifyIntervalDays(months, days)
	default: // justify_interval
		months, days, micros, err = justifyIntervalFull(months, days, micros)
	}
	if err != nil {
		return Datum{}, err
	}
	return NewIntervalDatumFull(months, days, micros), nil
}

// addDayS32 adds an int64 whole-day count (derived from the micros field) to
// the int32 day field, mirroring PG's pg_add_s32_overflow guard: a large time
// field can yield a whole-day count outside int32 range.
func addDayS32(day int32, whole int64) (int32, bool) {
	s := int64(day) + whole
	if s < math.MinInt32 || s > math.MaxInt32 {
		return 0, false
	}
	return int32(s), true
}

// justifyIntervalHours mirrors interval_justify_hours: move whole 24h chunks of
// the time (micros) field into the day field, then equalize the sign of day and
// time. months is passed through unchanged.
func justifyIntervalHours(months, days int32, micros int64) (int32, int32, int64, error) {
	wholeday := micros / usecsPerDay // TMODULO: truncates toward zero
	micros -= wholeday * usecsPerDay
	nd, ok := addDayS32(days, wholeday)
	if !ok {
		return 0, 0, 0, errIntervalRange
	}
	days = nd
	if days > 0 && micros < 0 {
		micros += usecsPerDay
		days--
	} else if days < 0 && micros > 0 {
		micros -= usecsPerDay
		days++
	}
	return months, days, micros, nil
}

// justifyIntervalFull mirrors interval_justify_interval: bring the time field
// within [0,24h) and the day field within [0,30d), then make the sign of all
// three fields equal. Pre-justifies days when day and time share a sign to
// avoid a spurious overflow, matching upstream exactly.
func justifyIntervalFull(months, days int32, micros int64) (int32, int32, int64, error) {
	// Pre-justify days if it might prevent overflow (day and time same sign).
	if (days > 0 && micros > 0) || (days < 0 && micros < 0) {
		wholemonth := days / 30
		days -= wholemonth * 30
		nm, ok := addDayS32(months, int64(wholemonth))
		if !ok {
			return 0, 0, 0, errIntervalRange
		}
		months = nm
	}
	// Fold whole 24h chunks of time into days.
	wholeday := micros / usecsPerDay
	micros -= wholeday * usecsPerDay
	nd, ok := addDayS32(days, wholeday)
	if !ok {
		return 0, 0, 0, errIntervalRange
	}
	days = nd
	// Fold whole 30-day chunks of days into months.
	wholemonth := days / 30
	days -= wholemonth * 30
	nm, ok := addDayS32(months, int64(wholemonth))
	if !ok {
		return 0, 0, 0, errIntervalRange
	}
	months = nm
	// Equalize the sign of month against day/time.
	if months > 0 && (days < 0 || (days == 0 && micros < 0)) {
		days += 30
		months--
	} else if months < 0 && (days > 0 || (days == 0 && micros > 0)) {
		days -= 30
		months++
	}
	// Equalize the sign of day against time.
	if days > 0 && micros < 0 {
		micros += usecsPerDay
		days--
	} else if days < 0 && micros > 0 {
		micros -= usecsPerDay
		days++
	}
	return months, days, micros, nil
}

// justifyIntervalDays is the pure month/day rebalancing core of justify_days()
// (evalJustify above): move whole 30-day chunks out of days into months, then
// equalize the sign of both fields — mirrors interval_justify_days
// (postgres/src/backend/utils/adt/timestamp.c). justify_days leaves the time
// (sub-day micros) field untouched; the full three-field normalization lives in
// justifyIntervalFull.
func justifyIntervalDays(months, days int32) (int32, int32) {
	wholeMonths := days / 30
	days -= wholeMonths * 30
	months += wholeMonths
	if months > 0 && days < 0 {
		days += 30
		months--
	} else if months < 0 && days > 0 {
		days -= 30
		months++
	}
	return months, days
}

// evalDateBin implements date_bin(step interval, source timestamp, origin timestamp).
// Bins the source timestamp into the bucket identified by origin aligned to step.
// M0097-0004.
func evalDateBin(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 3 {
		return NullDatum, nil
	}
	stepArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || stepArg.IsNull() || stepArg.Kind != KindInterval {
		return NullDatum, nil
	}
	srcArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil || srcArg.IsNull() || srcArg.Kind != KindTime {
		return NullDatum, nil
	}
	originArg, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil || originArg.IsNull() || originArg.Kind != KindTime {
		return NullDatum, nil
	}
	// Convert interval step to duration (days only for v0).
	stepDays := int64(stepArg.IntervalDaysValue())
	if stepDays == 0 {
		return NullDatum, nil
	}
	stepDur := time.Duration(stepDays) * 24 * time.Hour
	src := srcArg.TimeValue().UTC()
	origin := originArg.TimeValue().UTC()
	diff := src.Sub(origin)
	bucket := (diff / stepDur) * stepDur
	if diff < 0 {
		bucket = ((diff - stepDur + 1) / stepDur) * stepDur
	}
	return NewTimeDatum(origin.Add(bucket)), nil
}

// parseIntervalCastString parses a runtime interval-cast string (the
// `::interval` / `CAST(... AS interval)` path, as opposed to the
// parse-time `INTERVAL '...'` typed-literal syntax handled by
// splitEmbeddedInterval/evalIntervalLit). It delegates to the parser
// package's ParseIntervalBody so the cast path and the typed-literal path —
// sibling paths that must not diverge — share exactly one interval-body
// tokenizer (single-field "<n> <unit>", multi-field, and HH:MM:SS times).
// Anything ParseIntervalBody rejects fails here too so the caller can raise
// 22007 rather than silently pass the string through.
// M0122-0004; sub-day units unimplemented_feat #5; multi-field #5(b).
func parseIntervalCastString(s string) (months, days int32, micros int64, ok bool) {
	return parser.ParseIntervalBody(s)
}

// evalIntervalLit parses the integer body of an `interval 'N' unit`
// literal. The parser already normalised plurals so Unit is one
// of day / month / year.
//
// M0066-0002: caches the parsed N on the planner node so
// repeated evaluations in a hot loop skip the
// `strconv.ParseInt` cost.
func evalIntervalLit(x *optimizer.IntervalLit) (Datum, error) {
	if x.CacheValid {
		return NewIntervalDatumFull(x.CachedMonths, x.CachedDays, x.CachedMicros), nil
	}
	// Multi-field / HH:MM:SS bodies (`interval '1 day 05:00:00'`) are decoded
	// once by the parser (unimplemented_feat #5(b)) and carried pre-computed;
	// the trailing-unit typmod truncation never applies to these embedded
	// forms, so use the components directly.
	if x.PreComputed {
		x.CachedMonths, x.CachedDays, x.CachedMicros = x.PreMonths, x.PreDays, x.PreMicros
		x.CacheValid = true
		return NewIntervalDatumFull(x.PreMonths, x.PreDays, x.PreMicros), nil
	}
	// interval 'infinity' / '-infinity' / '+infinity' (unimplemented_feat
	// #5(d-iv)): a whole-body infinity token maps to PG's INTERVAL_NOBEGIN /
	// INTERVAL_NOEND sentinel and pre-empts the numeric / qualified paths — any
	// trailing typmod qualifier (`interval 'infinity' hour to minute`) is ignored
	// and must NOT drive truncIntervalToUnit, so it is recognised before both.
	if mo, d, mu, isInf := parser.IntervalInfinitySentinel(x.Value); isInf {
		x.CachedMonths, x.CachedDays, x.CachedMicros = mo, d, mu
		x.CacheValid = true
		return NewIntervalDatumFull(mo, d, mu), nil
	}
	var months, days int32
	var micros int64
	if val, fval, magOK := parser.ParseIntervalMagnitude(x.Value); magOK {
		var ok bool
		months, days, micros, ok = parser.IntervalUnitToParts(val, fval, x.Unit)
		if !ok {
			return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(), Message: fmt.Sprintf("interval unit %q is not supported in v0", x.Unit)}
		}
	} else if x.Qualified {
		// Complex body under a range/typmod qualifier
		// (`interval '1 day 5' hour to minute`): the body is not a single bare
		// magnitude, so decode the whole multi-field body up front. A trailing
		// unitless number resolves via the qualifier's low field (x.Unit) —
		// PostgreSQL's DecodeInterval `switch (range)` picks the same default
		// field (datetime.c). AdjustIntervalForTypmod's range truncation is then
		// applied below exactly as for the bare-magnitude case.
		// unimplemented_feat #5(d-iv) complex-body-under-range.
		var ok bool
		months, days, micros, ok = parser.ParseIntervalBodyWithDefault(x.Value, x.Unit)
		if !ok {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid interval count %q", x.Value)}
		}
	} else {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid interval count %q", x.Value)}
	}
	// Form 1 `interval 'N' <unit>`: the trailing unit is an SQL typmod
	// field that truncates the value to that field's granularity. For a range
	// (`hour to minute`) x.Unit is already the low field (the parser collapses
	// the range to it), and a SECOND(p) qualifier additionally rounds the
	// fractional seconds — both mirror AdjustIntervalForTypmod.
	if x.Qualified {
		months, days, micros = truncIntervalToUnit(months, days, micros, x.Unit)
		if x.HasPrec {
			micros = roundIntervalMicrosToPrec(micros, x.Prec)
		}
	}
	x.CachedMonths, x.CachedDays, x.CachedMicros = months, days, micros
	x.CacheValid = true
	return NewIntervalDatumFull(months, days, micros), nil
}

// applyIntervalCastTypmod evaluates a `CAST(x AS interval <qualifier>)` /
// `x::interval <qualifier>` whose target type carries an interval typmod (a
// field qualifier and/or SECOND precision, packed by the parser's
// packIntervalCastTypmod). Unlike a bare interval cast, the low field changes
// the DEFAULT UNIT a bare-magnitude body is interpreted in — PG's interval_in
// receives the typmod and DecodeInterval's `switch (range)` picks that field
// (`'90'::interval minute` = 90 minutes = 01:30:00, not 90 seconds) — after
// which AdjustIntervalForTypmod truncates to the field's granularity and rounds
// the fractional seconds to the precision. This mirrors the typed-literal path
// evalIntervalLit takes for `interval '90' minute`. An already-typed interval
// operand (`some_interval::interval minute`) is only truncated/rounded, never
// reinterpreted. unimplemented_feat #5(d-iv).
func applyIntervalCastTypmod(v Datum, typmod int64, pos int) (Datum, error) {
	if v.IsNull() {
		return NullDatum, nil
	}
	lowField, hasPrec, prec := parser.DecodeIntervalCastTypmod(typmod)
	// Precision-only typmod (`interval(2)`) has no low field, so a bare
	// magnitude still defaults to seconds (the typmod-free interval default).
	unit := lowField
	if unit == "" {
		unit = "second"
	}
	var months, days int32
	var micros int64
	switch v.Kind {
	case KindInterval:
		months, days, micros = v.IntervalMonthsValue(), v.IntervalDaysValue(), v.IntervalMicrosValue()
	case KindString:
		s := v.StringValue()
		// interval 'infinity' / '-infinity' carries through unchanged (the
		// typmod is ignored), mirroring evalIntervalLit's infinity pre-emption.
		if mo, d, mu, isInf := parser.IntervalInfinitySentinel(s); isInf {
			return NewIntervalDatumFull(mo, d, mu), nil
		}
		if val, fval, magOK := parser.ParseIntervalMagnitude(s); magOK {
			var ok bool
			months, days, micros, ok = parser.IntervalUnitToParts(val, fval, unit)
			if !ok {
				return Datum{}, &ExecError{Code: "22007", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type interval: %q", s)}
			}
		} else if mo, d, mu, ok := parser.ParseIntervalBodyWithDefault(s, unit); ok {
			months, days, micros = mo, d, mu
		} else {
			return Datum{}, &ExecError{Code: "22007", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type interval: %q", s)}
		}
	default:
		return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to interval"}
	}
	if lowField != "" {
		months, days, micros = truncIntervalToUnit(months, days, micros, lowField)
	}
	if hasPrec {
		micros = roundIntervalMicrosToPrec(micros, prec)
	}
	return NewIntervalDatumFull(months, days, micros), nil
}

// truncIntervalToUnit applies SQL interval typmod truncation for the
// trailing-qualifier form `interval 'N' <unit>`: the qualifier restricts
// the interval to that field's granularity, discarding (toward zero) every
// component below it — mirroring PostgreSQL's AdjustIntervalForTypmod
// (postgres/src/backend/utils/adt/timestamp.c). e.g. `interval '1.5' hour`
// = 01:00:00 (the 30 minutes below HOUR are dropped), whereas the
// typmod-free `interval '1.5 hours'` keeps them (01:30:00). Integer-valued
// literals are unaffected (nothing below the field to drop). Go's truncated
// integer division gives the toward-zero behavior PG uses for negatives.
func truncIntervalToUnit(months, days int32, micros int64, unit string) (int32, int32, int64) {
	switch unit {
	case "year":
		return (months / 12) * 12, 0, 0
	case "month":
		return months, 0, 0
	case "day":
		return months, days, 0
	case "hour":
		return months, days, (micros / usecsPerHour) * usecsPerHour
	case "minute":
		return months, days, (micros / usecsPerMinute) * usecsPerMinute
	default:
		// "second" (and any non-standard unit): no sub-field to drop.
		return months, days, micros
	}
}

// intervalPrecScales[p] = 10^(6-p) microseconds: the quantum a SECOND(p) typmod
// rounds the interval time field to. Mirrors IntervalScales in PostgreSQL's
// AdjustIntervalForTypmod (postgres/src/backend/utils/adt/timestamp.c).
var intervalPrecScales = [7]int64{1000000, 100000, 10000, 1000, 100, 10, 1}

// roundIntervalMicrosToPrec rounds the time (micros) field of an interval to a
// fractional-seconds precision p (0..6 digits), mirroring the precision arm of
// PostgreSQL's AdjustIntervalForTypmod: round-half-away-from-zero to 10^(6-p)
// microseconds (`interval '1.23456789' second(2)` → 00:00:01.23,
// `interval '1.999999' second(2)` → 00:00:02). p==6 (full precision) is a
// no-op. Go's truncated `%` matches C for the negative-time branch.
func roundIntervalMicrosToPrec(micros int64, p int) int64 {
	if p < 0 || p >= 6 {
		return micros
	}
	scale := intervalPrecScales[p]
	offset := scale / 2 // == IntervalOffsets[p]
	if micros >= 0 {
		micros += offset
	} else {
		micros -= offset
	}
	return micros - micros%scale
}

// evalInExpr evaluates `expr [NOT] IN (subquery | val_list)`.
// The inner set is materialised once per evaluation (no
// caching across rows in v0); for IN (subquery), the executor
// drains the inner plan and collects the first column per
// row. For IN (list), the list is evaluated against the
// outer row.
//
// Three-valued logic:
//   - operand NULL → result NULL.
//   - any inner NULL when outer doesn't match a non-NULL
//     value → NULL.
//   - inner empty → false (NOT IN: true).
//
// Multi-column subqueries raise 42601 unless the operand is a RowExpr,
// in which case element-wise tuple comparison is used (row-constructor IN).
func evalInExpr(x *optimizer.InExpr, slot SlotView, ctx *Context) (Datum, error) {
	// Row-constructor IN/NOT IN subquery: (a, b) IN (SELECT x, y FROM ...).
	// Route to element-wise tuple comparison. M0097-0020.
	if rowOp, ok := x.Operand.(*optimizer.RowExpr); ok && x.Plan != nil {
		return evalRowConstructorInExpr(x, rowOp, slot, ctx)
	}
	// Use evalExprSlot so CTIDExpr can access hasCTID from the slot. M0097-0062.
	operand, err := evalExprSlot(x.Operand, slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	// A NULL operand does NOT short-circuit to NULL: the answer depends
	// on whether the (sub)list is empty. `NULL IN (∅)` is FALSE and
	// `NULL NOT IN (∅)` is TRUE — the quantifier is vacuous, so the
	// operand's nullness never enters into it (PG scalar-array/sublink
	// semantics; ch.07 M2, fixed for the correlated-empty-inner case in
	// bundle stage S1b). Only a NON-empty list makes the result NULL.
	// The subquery therefore must run even for NULL operands; the cost
	// is bounded by the sublink result caches and, later, the S2 handles.
	operandNull := operand.IsNull()

	row := slotToRow(slot)
	values, err := collectInValues(x, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if operandNull {
		if len(values) == 0 {
			// Vacuous quantification over an empty list.
			switch {
			case x.AnyOp != 0 && x.AllOp:
				return NewBoolDatum(true), nil // op ALL(∅) — vacuously true
			case x.AnyOp != 0:
				return NewBoolDatum(false), nil // op ANY(∅) — vacuously false
			default:
				return NewBoolDatum(x.Negated), nil // IN → false, NOT IN → true
			}
		}
		return NullDatum, nil
	}
	// op ALL semantics (ScalarArrayOpExpr, useOr=false): `left op ALL(...)` —
	// AND of (left op elem) for each element; false as soon as one element
	// fails the comparison. NULL elements are skipped (same simplification
	// as the ANY branch below, not full three-valued NULL propagation).
	// M0122-0004.
	if x.AnyOp != 0 && x.AllOp {
		for _, v := range values {
			if v.IsNull() {
				continue
			}
			res, err := evalBinary(x.AnyOp, operand, v, 0, ctx)
			if err != nil {
				return Datum{}, err
			}
			if res.Kind == KindBool && !res.BoolValue() {
				return NewBoolDatum(false), nil
			}
		}
		return NewBoolDatum(true), nil
	}
	// op ANY semantics (ScalarArrayOpExpr): `left op ANY(array)` — OR of
	// (left op elem) for each element. Used for non-equality operators like
	// `col ~ ANY(ARRAY[...])`. M0097-0068.
	if x.AnyOp != 0 {
		for _, v := range values {
			if v.IsNull() {
				continue
			}
			res, err := evalBinary(x.AnyOp, operand, v, 0, ctx)
			if err != nil {
				return Datum{}, err
			}
			if res.Kind == KindBool && res.BoolValue() {
				return NewBoolDatum(true), nil
			}
		}
		return NewBoolDatum(false), nil
	}
	// != ANY semantics: return true if operand != at least one element (OR
	// of inequality comparisons). M0097-0067.
	if x.NotEqualAny {
		for _, v := range values {
			if v.IsNull() {
				continue // skip nulls in the list
			}
			eq, err := compareEq(operand, v)
			if err != nil {
				return Datum{}, err
			}
			if !(eq.Kind == KindBool && eq.BoolValue()) {
				// operand != v → found at least one mismatch → true
				return NewBoolDatum(true), nil
			}
		}
		// All elements equal operand (or list empty) → false
		return NewBoolDatum(false), nil
	}
	// Stage 11 (S3/D4.3): plain-equality IN/NOT IN over an uncorrelated
	// subquery probes a hashed value set instead of scanning linearly.
	// Falls through to the linear loop whenever the probe declines
	// (correlated, unhashable kinds, cross-family coercion, kill switch,
	// budget pressure) — the linear path is the correctness reference.
	if res, served := evalInHashProbe(x, operand, values, ctx); served {
		return res, nil
	}
	sawNull := false
	for _, v := range values {
		if v.IsNull() {
			sawNull = true
			continue
		}
		eq, err := compareEq(operand, v)
		if err != nil {
			return Datum{}, err
		}
		if eq.Kind == KindBool && eq.BoolValue() {
			return NewBoolDatum(!x.Negated), nil
		}
	}
	if sawNull {
		return NullDatum, nil
	}
	return NewBoolDatum(x.Negated), nil
}

// evalRowConstructorInExpr handles (a, b, ...) IN (SELECT x, y, ... FROM ...)
// using element-wise 3-valued-logic tuple comparison. M0097-0020.
func evalRowConstructorInExpr(x *optimizer.InExpr, rowOp *optimizer.RowExpr, slot SlotView, ctx *Context) (Datum, error) {
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	// Evaluate each element of the left-side row constructor.
	leftElems := make([]Datum, len(rowOp.Elems))
	for i, e := range rowOp.Elems {
		v, err := evalExprSlot(e, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		leftElems[i] = v
	}
	nCols := len(leftElems)

	// Push outer row for correlated subquery resolution.
	outerRow := slotToRow(slot)
	ctx.OuterRows = append(ctx.OuterRows, outerRow)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()

	op, err := Build(x.Plan)
	if err != nil {
		return Datum{}, err
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return Datum{}, err
	}
	defer func() { _ = op.Close() }()

	sawNullRow := false
	for {
		innerSlot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return Datum{}, err
		}
		rightRow := slotRow(innerSlot)
		if len(rightRow) != nCols {
			return Datum{}, &ExecError{
				Code: "42601",
				Pos:  x.Pos(),
				Message: fmt.Sprintf("row value has %d columns but subquery has %d columns",
					nCols, len(rightRow)),
			}
		}
		// Compare element-by-element with 3-valued logic:
		// rowFalse=true → at least one element is definitely not equal.
		// rowNull=true  → at least one element comparison is indeterminate (NULL).
		rowFalse := false
		rowNull := false
		for i := 0; i < nCols; i++ {
			left, right := leftElems[i], rightRow[i]
			if left.IsNull() || right.IsNull() {
				rowNull = true
				continue
			}
			eq, err := compareEq(left, right)
			if err != nil {
				return Datum{}, err
			}
			if !(eq.Kind == KindBool && eq.BoolValue()) {
				rowFalse = true
				break
			}
		}
		if !rowFalse && !rowNull {
			// All elements matched.
			return NewBoolDatum(!x.Negated), nil
		}
		if !rowFalse && rowNull {
			// No definitive mismatch but some NULLs — result may be NULL.
			sawNullRow = true
		}
	}
	if sawNullRow {
		return NullDatum, nil
	}
	return NewBoolDatum(x.Negated), nil
}

// evalRowFuncCallVsSubqueryExpr handles ROW(a,b,...) = (SELECT x,y,... FROM ...)
// by evaluating the subquery as a multi-column row and comparing element-wise.
// Op must be OpEq or OpNe. The rowArgs are the Args of the FuncCall{Name:"row",...}.
// M0097-0020.
func evalRowFuncCallVsSubqueryExpr(op parser.OpCode, rowArgs []optimizer.Expr, sqOp *optimizer.SubqueryExpr, slot SlotView, ctx *Context) (Datum, error) {
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	// Evaluate left-side elements.
	leftElems := make([]Datum, len(rowArgs))
	for i, e := range rowArgs {
		v, err := evalExprSlot(e, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		leftElems[i] = v
	}

	// Push outer row for correlated subquery resolution.
	outerRow := slotToRow(slot)
	ctx.OuterRows = append(ctx.OuterRows, outerRow)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()

	innerOp, err := Build(sqOp.Plan)
	if err != nil {
		return Datum{}, err
	}
	if err := innerOp.Open(ctx); err != nil {
		_ = innerOp.Close()
		return Datum{}, err
	}
	defer func() { _ = innerOp.Close() }()

	innerSlot, err := innerOp.Next()
	if err == EOF {
		return NullDatum, nil // empty subquery → NULL per SQL semantics
	}
	if err != nil {
		return Datum{}, err
	}
	rightRow := slotRow(innerSlot)

	// Drain: exactly one row allowed.
	if _, err2 := innerOp.Next(); err2 != EOF {
		if err2 == nil {
			return Datum{}, &ExecError{Code: "21000", Pos: sqOp.Pos(), Message: "more than one row returned by a subquery used as an expression"}
		}
		return Datum{}, err2
	}

	if len(rightRow) != len(leftElems) {
		return Datum{}, &ExecError{
			Code:    "42601",
			Pos:     sqOp.Pos(),
			Message: fmt.Sprintf("row value has %d columns but subquery has %d columns", len(leftElems), len(rightRow)),
		}
	}

	// Compare element-by-element with 3-valued logic.
	sawNull := false
	for i := 0; i < len(leftElems); i++ {
		left, right := leftElems[i], rightRow[i]
		if left.IsNull() || right.IsNull() {
			sawNull = true
			continue
		}
		eq, err := compareEq(left, right)
		if err != nil {
			return Datum{}, err
		}
		if eq.Kind == KindBool && !eq.BoolValue() {
			// Definitely not equal: row comparison is FALSE (for =) or TRUE (for !=).
			return NewBoolDatum(op == parser.OpNe), nil
		}
	}
	if sawNull {
		return NullDatum, nil
	}
	// All elements equal: row comparison is TRUE (for =) or FALSE (for !=).
	return NewBoolDatum(op == parser.OpEq), nil
}

// bindSubPlanParams evaluates a lowered sublink's Args against the
// current outer row, writes them into their ParamExec slots, and
// returns the projected cache key: the sublink's identity plus the
// bound VALUES — never the full outer row. Distinct outer rows that
// agree on the correlation columns therefore share a cache entry, and
// two sublink sites can never collide (the key embeds the expr
// pointer), which closes the correlated collectInValues collision
// hazard (ch.04 §2) for every lowered sublink. D4.1/D4.4.
func bindSubPlanParams(exprPtr any, parParam []int, args []optimizer.Expr, row Row, ctx *Context) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%p", exprPtr)
	for i, a := range args {
		v, err := evalExpr(a, row, ctx)
		if err != nil {
			return "", err
		}
		ctx.SetParamExec(parParam[i], v)
		sb.WriteByte(0x1f)
		sb.WriteString(datumKey(v))
	}
	return sb.String(), nil
}

// collectInValues returns the inner set for `IN (...)`. When
// the source is a subquery, drains it; the subquery must have
// exactly one column. Otherwise evaluates the value list.
func collectInValues(x *optimizer.InExpr, row Row, ctx *Context) ([]Datum, error) {
	if x.Plan != nil {
		// Check for query cancellation before each SubPlan evaluation.
		// Each call may scan millions of rows; this single atomic read
		// costs ~5 ns vs microseconds-to-seconds of SubPlan work.
		if ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		// Consult the subquery cache so correlated IN
		// subqueries are evaluated at most once per distinct
		// outer-row value rather than per outer row.  This
		// reduces Q20-like queries from O(outer×inner) to
		// O(inner) per distinct correlation key.
		//
		// For non-correlated subqueries (M0058-0001), the
		// inner plan returns the same set for every outer row,
		// so a constant cache key collapses re-evaluation to
		// a single execution.
		stat := ctx.subPlanStat(x)
		stat.Calls++
		lowered := len(x.ParParam) > 0
		var cacheKey string
		switch {
		case lowered:
			// D4.1: bind the params first — the projected key derives
			// from the bound values, and the inner plan reads the
			// slots instead of the OuterRows stack.
			var err error
			cacheKey, err = bindSubPlanParams(x, x.ParParam, x.Args, row, ctx)
			if err != nil {
				return nil, err
			}
		case x.IsNonCorrelated:
			cacheKey = nonCorrelatedCacheKey(x)
		default:
			cacheKey = subqueryCacheKey(row)
		}
		// Correlated results may be cached only when the inner plan is
		// free of volatile functions and LockRows (Stage 9 cacheability
		// gate, ch.07 M13). Non-correlated sublinks cache regardless —
		// upstream's InitPlan is evaluated once per statement even when
		// volatile (see subplan.go).
		cacheable := x.IsNonCorrelated || subPlanResultCacheable(ctx, x, x.Plan, false)
		// Only param-lowered keys are scope-independent (Stage 10);
		// everything else lives in the scoped store with the
		// historical clear-on-depth-change guard.
		scoped := !lowered
		if cacheable {
			if cached, ok := ctx.subqCacheGet(cacheKey, scoped); ok {
				stat.CacheHits++
				return cached, nil
			}
		}
		stat.CacheMisses++
		if !lowered {
			// Push the outer row so correlated refs inside the
			// IN-subquery resolve against it. Popped explicitly
			// below BEFORE the cache Put (with a defer as the
			// error-path belt): the Get above ran at the HOST
			// depth, and the scoped store clears whenever the
			// depth changes — a Put issued while the pushed scope
			// is still live lands one depth deeper, so the very
			// next host-depth Get would wipe it. That mismatch
			// silently re-ran every scoped non-correlated IN once
			// per outer row (the M0058-0001 pathology returning
			// through the Stage-10 store split; caught by the
			// Stage-11 hash tests).
			ctx.OuterRows = append(ctx.OuterRows, row)
			popped := false
			pop := func() {
				if !popped {
					popped = true
					ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1]
				}
			}
			defer pop()
			op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
			if err != nil {
				return nil, err
			}
			defer done()
			var out []Datum
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					return nil, err
				}
				r := slotRow(slot)
				if len(r) != 1 {
					return nil, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("subquery used as IN argument returned %d columns, expected 1", len(r))}
				}
				out = append(out, r[0])
			}
			pop()
			if cacheable {
				ctx.subqCachePut(cacheKey, scoped, out)
			}
			return out, nil
		}
		op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
		if err != nil {
			return nil, err
		}
		defer done()
		var out []Datum
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			r := slotRow(slot)
			if len(r) != 1 {
				return nil, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("subquery used as IN argument returned %d columns, expected 1", len(r))}
			}
			out = append(out, r[0])
		}
		// Cache the result so subsequent rows with the same
		// correlation values skip the inner-plan execution.
		if cacheable {
			ctx.subqCachePut(cacheKey, scoped, out)
		}
		return out, nil
	}
	// Evaluate each list element. When the list has a single element that
	// evaluates to an array literal "{e1,e2,...}", expand it into individual
	// elements so `x = ANY (ARRAY[...])` / `x = ANY ('{...}'::type[])` works
	// correctly. M0097-enum-any.
	rawOut := make([]Datum, 0, len(x.List))
	for _, e := range x.List {
		v, err := evalExpr(e, row, ctx)
		if err != nil {
			return nil, err
		}
		rawOut = append(rawOut, v)
	}
	// Expand array-literal elements: a KindString "{...}" in the list is treated
	// as an array of individual text values, matching PostgreSQL's = ANY (array)
	// semantics where the single operand is an array type.
	if len(rawOut) == 1 {
		v := rawOut[0]
		if v.Kind == KindString {
			s := v.StringValue()
			if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
				elems := parseTextArray(s)
				out := make([]Datum, len(elems))
				for i, el := range elems {
					if el == "NULL" {
						out[i] = NullDatum
					} else {
						out[i] = NewStringDatum(el)
					}
				}
				return out, nil
			}
		}
	}
	return rawOut, nil
}

// evalExistsExpr evaluates `[NOT] EXISTS (subquery)`. Opens
// the inner plan, asks for one row, returns the bool. Works
// regardless of column count — EXISTS only cares whether at
// least one row exists.
func evalExistsExpr(x *optimizer.ExistsExpr, row Row, ctx *Context) (Datum, error) {
	// Check for query cancellation before each EXISTS/NOT EXISTS evaluation.
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	if len(x.ParParam) > 0 {
		// D4.1 lowered path: bind the correlation params; the inner
		// plan reads ParamExec slots, so no OuterRows push. The
		// projected key is discarded — correlated EXISTS deliberately
		// has NO result cache yet: caching it would collapse per-row
		// re-execution of volatile inners (matrix M13) before the
		// Stage-9 cacheability gate exists to forbid that.
		if _, err := bindSubPlanParams(x, x.ParParam, x.Args, row, ctx); err != nil {
			return Datum{}, err
		}
	} else {
		// Push the outer row so correlated column refs in the inner
		// plan can resolve against it. Pop on return regardless of
		// outcome.
		// The push happens BELOW, after the host-depth cache check:
		// the scoped store clears whenever the OuterRows depth
		// changes, so cache Get/Put must both run at the HOST depth
		// or they ping-pong-clear against sibling sublinks' entries
		// (the collectInValues variant of this bug re-ran every
		// scoped non-correlated IN once per outer row; see the
		// Stage-11 depth-fix comment there).
	}

	stat := ctx.subPlanStat(x)
	stat.Calls++

	lowered := len(x.ParParam) > 0

	// For non-correlated EXISTS (M0058-0001), the inner plan
	// returns the same boolean for every outer row. Cache it
	// under a constant key.
	if x.IsNonCorrelated {
		// Scoped store: the IsNonCorrelated flag is only trustworthy
		// where lowering verified it, and a non-correlated EXISTS is
		// never lowered (no params) — keep the historical
		// clear-on-depth-change guard (Stage 10).
		cacheKey := nonCorrelatedCacheKey(x)
		if cached, ok := ctx.subqCacheGet(cacheKey, true); ok && len(cached) == 1 {
			stat.CacheHits++
			return cached[0], nil
		}
		stat.CacheMisses++
		val, err := existsWithScope(x, row, ctx, lowered)
		if err != nil {
			return Datum{}, err
		}
		ctx.subqCachePut(cacheKey, true, []Datum{val})
		return val, nil
	}
	return existsWithScope(x, row, ctx, lowered)
}

// existsWithScope runs existsImpl with the outer row pushed for the
// unlowered path (correlated refs — or a mislabelled IsNonCorrelated —
// resolve against it) and popped again before returning, so callers'
// cache operations always run at the host depth.
func existsWithScope(x *optimizer.ExistsExpr, row Row, ctx *Context, lowered bool) (Datum, error) {
	if !lowered {
		ctx.OuterRows = append(ctx.OuterRows, row)
		defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
	}
	return existsImpl(x, ctx)
}

func existsImpl(x *optimizer.ExistsExpr, ctx *Context) (Datum, error) {
	// Stage 9 (D4.2): the inner plan is built once per statement and
	// re-run via its handle; only the legacy path (kill switch off)
	// re-instantiates per call. The lockRowsOp maxDrain=1 EXISTS
	// optimisation (M0100-0005) is applied at handle build inside
	// acquireSubPlanOp.
	op, done, err := acquireSubPlanOp(ctx, x, x.Plan, true)
	if err != nil {
		return Datum{}, err
	}
	defer done()
	_, err = op.Next()
	hasRow := err == nil
	if err != nil && err != EOF {
		return Datum{}, err
	}
	return NewBoolDatum(hasRow != x.Negated), nil
}

// evalSubquery runs the inner plan inside a SubqueryExpr and
// returns its single cell. Multi-row results raise SQLSTATE
// 21000 (cardinality_violation); zero rows return NULL (per
// upstream's scalar-subquery semantics). Multi-column subqueries
// raise 42601 because v0's caller types the SubqueryExpr as a
// single value.
//
// v0 is always uncorrelated — the inner plan never sees the
// outer row. Correlated subqueries (parameter pull-up) are
// deferred; see docs/design/0003-0008-subqueries.md.
func evalSubquery(x *optimizer.SubqueryExpr, row Row, ctx *Context) (Datum, error) {
	// Check for query cancellation before each scalar SubPlan evaluation.
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	lowered := len(x.ParParam) > 0
	var loweredKey string
	if lowered {
		// D4.1: bind the correlation params before any inner-plan
		// path runs — both the CorrSubqOps rescan below and a full
		// rebuild read the slots via ExecParamRef.
		var bindErr error
		loweredKey, bindErr = bindSubPlanParams(x, x.ParParam, x.Args, row, ctx)
		if bindErr != nil {
			return Datum{}, bindErr
		}
	}
	// The unlowered push happens inside subqueryWithScope, not here:
	// the scoped result store clears whenever the OuterRows depth
	// changes, so the cache Get/Put below must run at the HOST depth
	// or they ping-pong-clear against sibling sublinks' entries (the
	// collectInValues variant of this bug re-ran every scoped
	// non-correlated IN once per outer row; Stage-11 depth fix).

	stat := ctx.subPlanStat(x)
	stat.Calls++

	// Fast path: correlated subquery whose inner plan is an index-probe
	// chain. Skip the result caches entirely — the operator rescans correctly
	// per outer row (Stage-9 handle, or the legacy CorrSubqOps registry
	// when the engine is off), so the key-build + map overhead is
	// unnecessary. Rescanning caches nothing, so this path needs no
	// volatility gate.
	if !x.IsNonCorrelated && planIsIndexScanBased(x.Plan) {
		return subqueryWithScope(x, row, ctx, lowered)
	}

	// Check cache for scalar subquery results. For non-correlated
	// subqueries (M0058-0001), use a constant cache key. Lowered
	// correlated subqueries key on the bound param VALUES (projected
	// key, D4.4) instead of the full outer row.
	var cacheKey string
	switch {
	case lowered:
		cacheKey = loweredKey
	case x.IsNonCorrelated:
		cacheKey = nonCorrelatedCacheKey(x)
	default:
		cacheKey = fmt.Sprintf("%p|%s", x, subqueryCacheKey(row))
	}
	// Correlated results may be served from the cache only when the
	// inner plan is volatility/LockRows-free (Stage 9 gate, ch.07 M13);
	// non-correlated results follow InitPlan semantics and always cache.
	cacheable := x.IsNonCorrelated || subPlanResultCacheable(ctx, x, x.Plan, false)
	// Stage 10: lowered keys are scope-independent; the rest keep the
	// clear-on-depth-change guard (see Context field comment).
	scoped := !lowered
	if cacheable {
		if cached, ok := ctx.subqCacheGet(cacheKey, scoped); ok {
			stat.CacheHits++
			if len(cached) == 1 {
				return cached[0], nil
			}
			return NullDatum, nil
		}
	}
	stat.CacheMisses++
	val, err := subqueryWithScope(x, row, ctx, lowered)
	if err != nil {
		return Datum{}, err
	}
	// Store in cache (host depth — see the comment above).
	if cacheable {
		ctx.subqCachePut(cacheKey, scoped, []Datum{val})
	}
	return val, nil
}

// subqueryWithScope runs subqueryImpl with the outer row pushed for the
// unlowered path and popped before returning, so callers' cache
// operations always run at the host depth.
func subqueryWithScope(x *optimizer.SubqueryExpr, row Row, ctx *Context, lowered bool) (Datum, error) {
	if !lowered {
		ctx.OuterRows = append(ctx.OuterRows, row)
		defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
	}
	return subqueryImpl(x, ctx)
}

func subqueryImpl(x *optimizer.SubqueryExpr, ctx *Context) (Datum, error) {
	// Correlated scalar subqueries inside aggregates are called once per outer
	// row. Avoid O(N²) execution via two optimization paths:
	//
	// 1. IndexScan path: the inner plan uses a btree index (OuterColumnRef key).
	//    Cache the pre-opened operator and rescan cheaply per outer row.
	//    Requires: planIsIndexScanBased(x.Plan).
	//
	// 2. Hash map path: inner plan is Project(Filter(SeqScan, col=OuterColumnRef)).
	//    Scan the inner table ONCE (O(N)) to build key→value map, then do
	//    O(1) lookups. Works even when no btree index exists.
	if !x.IsNonCorrelated {
		// Path 1: IndexScan-based (btree index exists). With the
		// Stage-9 engine the generic handle serves this (classified
		// rescanReOpen — identical lifecycle to the old CorrSubqOps
		// registry, which remains only for the legacy kill-switch
		// path).
		if planIsIndexScanBased(x.Plan) {
			if subPlanRescanEnabled() {
				op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
				if err != nil {
					return Datum{}, err
				}
				defer done()
				return subqueryReadOne(op, x)
			}
			if ctx.CorrSubqOps == nil {
				ctx.CorrSubqOps = make(map[*optimizer.SubqueryExpr]Operator)
			}
			op, found := ctx.CorrSubqOps[x]
			if found {
				// Re-Open of an already-built operator: the one
				// place goopg already does what upstream always
				// does (ExecReScan rather than re-instantiate).
				ctx.subPlanStat(x).Rescans++
			} else {
				ctx.subPlanStat(x).Rebuilds++
				var buildErr error
				op, buildErr = Build(x.Plan)
				if buildErr != nil {
					return Datum{}, buildErr
				}
				ctx.CorrSubqOps[x] = op
			}
			if err := op.Open(ctx); err != nil {
				delete(ctx.CorrSubqOps, x)
				_ = op.Close()
				return Datum{}, err
			}
			return subqueryReadOne(op, x)
		}

		// Path 2: Hash map for Project(Filter(SeqScan, col = OuterColumnRef)).
		// The map freezes the inner table's derived values for the whole
		// statement, so it is a result cache — gated on cacheability
		// (volatile projections must re-execute per row, ch.07 M13).
		if info, ok := extractCorrSubqHashInfo(x.Plan); ok &&
			subPlanResultCacheable(ctx, x, x.Plan, false) {
			if ctx.CorrSubqHashMaps == nil {
				ctx.CorrSubqHashMaps = make(map[*optimizer.SubqueryExpr]map[string]Datum)
			}
			hm, built := ctx.CorrSubqHashMaps[x]
			if built {
				ctx.subPlanStat(x).CacheHits++
			} else {
				// The map freezes a whole inner table's worth of
				// derived values, so it draws on the shared result
				// cache budget (Stage 10, D4.5/D6.4): a pre-build
				// reservation from the planner's row estimate, then
				// a post-build reconcile to the measured size. When
				// either step does not fit, the statement falls back
				// to the per-row rescan path below — always correct,
				// just uncached.
				reserved, fits := ctx.corrSubqHashMapReserve(info.scan)
				if !fits {
					op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
					if err != nil {
						return Datum{}, err
					}
					defer done()
					return subqueryReadOne(op, x)
				}
				// Building the map scans the whole inner table
				// once — a rebuild, but only ever one of them.
				st := ctx.subPlanStat(x)
				st.CacheMisses++
				st.Rebuilds++
				var hmErr error
				hm, hmErr = buildCorrSubqHashMap(info, ctx)
				if hmErr != nil {
					return Datum{}, hmErr
				}
				if ctx.corrSubqHashMapReconcile(reserved, hm) {
					ctx.CorrSubqHashMaps[x] = hm
				}
				// On reconcile failure the map still answers THIS
				// row (it is already built), it just is not
				// retained — the next row rebuilds or rescans.
			}
			// Look up the outer column value.
			outerVal, err := evalExprSlot(info.outerRef, nil, ctx)
			if err != nil {
				return Datum{}, err
			}
			if outerVal.IsNull() {
				return NullDatum, nil
			}
			result, found := hm[datumKey(outerVal)]
			if !found {
				return NullDatum, nil
			}
			return result, nil
		}
	}

	op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
	if err != nil {
		return Datum{}, err
	}
	defer done()
	return subqueryReadOne(op, x)
}

// planIsIndexScanBased returns true when n is a plan whose operators safely
// support repeated Open() calls without an intervening Close(). Currently
// true for IndexScan and Project(IndexScan) — the two shapes produced by
// the correlated-subquery OuterColumnRef → IndexScan rewrite.
func planIsIndexScanBased(n optimizer.Node) bool {
	switch x := n.(type) {
	case *optimizer.IndexScan:
		return true
	case *optimizer.Project:
		return planIsIndexScanBased(x.Child)
	case *optimizer.Aggregate:
		// aggregateOp.Open resets o.idx and rebuilds o.rows each time, and its
		// child is reopened via the child.Open call. Safe as long as the child
		// (IndexScan) is safe.
		return planIsIndexScanBased(x.Child)
	case *optimizer.Filter:
		// filterOp carries no state besides child+pred; Open just re-opens
		// the child. Admitting it lets Aggregate{Filter{IndexScan}} shapes
		// (an index probe with extra local conjuncts, e.g. TPC-H Q20's
		// date-windowed sum) ride the rescan path instead of rebuilding
		// per call. Kept in lockstep with the planner's
		// innerPlanIsIndexProbeCheap (S6 scalar policy).
		return planIsIndexScanBased(x.Child)
	}
	return false
}

// corrSubqHashInfo holds the components extracted from a hash-joinable
// correlated scalar subquery plan: Project(Filter(SeqScan, col = OuterColumnRef)).
type corrSubqHashInfo struct {
	scan       *optimizer.SeqScan        // inner table to scan
	scanColIdx int                     // index of the join key column in SeqScan output
	outerRef   optimizer.Expr // outer join-key value: OuterColumnRef or (lowered) ExecParamRef
	projExpr   optimizer.Expr            // project expression to evaluate for result
}

// extractCorrSubqHashInfo detects the pattern
// Project(Filter(SeqScan, ColumnRef{col} = OuterColumnRef)) — or the aggregate
// wrapper Aggregate(same) — and returns the components needed to build a hash
// map for O(N) build + O(1) lookup per outer row.
func extractCorrSubqHashInfo(n optimizer.Node) (corrSubqHashInfo, bool) {
	var projectTarget optimizer.Expr
	switch x := n.(type) {
	case *optimizer.Project:
		if len(x.Targets) != 1 {
			return corrSubqHashInfo{}, false
		}
		projectTarget = x.Targets[0]
		n = x.Child
	default:
		return corrSubqHashInfo{}, false
	}
	// n should now be Filter(SeqScan) or SeqScan.
	var filterPred optimizer.Expr
	switch x := n.(type) {
	case *optimizer.Filter:
		filterPred = x.Predicate
		n = x.Child
	default:
		return corrSubqHashInfo{}, false
	}
	scan, ok := n.(*optimizer.SeqScan)
	if !ok {
		return corrSubqHashInfo{}, false
	}
	// Filter predicate must be: ColumnRef = <outer value> (or reversed),
	// where the outer value is an OuterColumnRef (stack path) or, after
	// D4.1 lowering, an ExecParamRef reading a bound ParamExec slot —
	// both evaluate position-independently at lookup time.
	bop, ok := filterPred.(*optimizer.BinaryOp)
	if !ok || bop.Op != parser.OpEq {
		return corrSubqHashInfo{}, false
	}
	isOuterVal := func(e optimizer.Expr) bool {
		switch e.(type) {
		case *optimizer.OuterColumnRef, *optimizer.ExecParamRef:
			return true
		}
		return false
	}
	var innerCol *optimizer.ColumnRef
	var outerRef optimizer.Expr
	if c, ok2 := bop.Left.(*optimizer.ColumnRef); ok2 && isOuterVal(bop.Right) {
		innerCol, outerRef = c, bop.Right
	} else if c, ok2 := bop.Right.(*optimizer.ColumnRef); ok2 && isOuterVal(bop.Left) {
		innerCol, outerRef = c, bop.Left
	}
	if innerCol == nil || outerRef == nil {
		return corrSubqHashInfo{}, false
	}
	return corrSubqHashInfo{
		scan:       scan,
		scanColIdx: innerCol.Index,
		outerRef:   outerRef,
		projExpr:   projectTarget,
	}, true
}

// buildCorrSubqHashMap scans the inner SeqScan once and builds
// key → value map where key = datumKey(scan[joinColIdx]) and value = eval(projExpr).
func buildCorrSubqHashMap(info corrSubqHashInfo, ctx *Context) (map[string]Datum, error) {
	scanOp, err := Build(info.scan)
	if err != nil {
		return nil, err
	}
	if err := scanOp.Open(ctx); err != nil {
		_ = scanOp.Close()
		return nil, err
	}
	defer func() { _ = scanOp.Close() }()
	result := make(map[string]Datum)
	for {
		slot, nerr := scanOp.Next()
		if nerr == EOF {
			break
		}
		if nerr != nil {
			return nil, nerr
		}
		row := slotRow(slot)
		if info.scanColIdx >= len(row) {
			continue
		}
		keyDatum := row[info.scanColIdx]
		valDatum, verr := evalExprSlot(info.projExpr, slot, ctx)
		if verr != nil {
			return nil, verr
		}
		result[datumKey(keyDatum)] = valDatum
	}
	return result, nil
}

// subqueryReadOne reads exactly one row from op (the scalar subquery result).
// Returns NullDatum on EOF, error on cardinality violation or scan error.
func subqueryReadOne(op Operator, x *optimizer.SubqueryExpr) (Datum, error) {
	slot, err := op.Next()
	if err == EOF {
		return NullDatum, nil
	}
	if err != nil {
		return Datum{}, err
	}
	row := slotRow(slot)
	if len(row) != 1 {
		return Datum{}, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("scalar subquery returned %d columns, expected 1", len(row))}
	}
	val := row[0]
	// Drain to ensure the subquery returned at most one row.
	if _, err := op.Next(); err != EOF {
		if err == nil {
			return Datum{}, &ExecError{Code: "21000", Pos: x.Pos(), Message: "more than one row returned by a subquery used as an expression"}
		}
		return Datum{}, err
	}
	return val, nil
}

// evalArraySubquery implements ARRAY(SELECT ...) — runs the inner plan and
// collects all result rows (must be single-column) into a PostgreSQL text-array
// string like {v1,v2,...}. M0097-0127.
func evalArraySubquery(x *optimizer.ArraySubqueryExpr, row Row, ctx *Context) (Datum, error) {
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	ctx.OuterRows = append(ctx.OuterRows, row)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()

	op, err := Build(x.Plan)
	if err != nil {
		return Datum{}, err
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return Datum{}, err
	}
	defer func() { _ = op.Close() }()

	var elems []string
	var nulls []bool
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return Datum{}, err
		}
		r := slotRow(slot)
		if len(r) != 1 {
			return Datum{}, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("ARRAY subquery returned %d columns, expected 1", len(r))}
		}
		d := r[0]
		if d.IsNull() {
			elems = append(elems, "")
			nulls = append(nulls, true)
		} else {
			elems = append(elems, d.Format())
			nulls = append(nulls, false)
		}
	}
	return NewStringDatum(formatTextArrayWithNulls(elems, nulls)), nil
}

// evalMultiAssignSubqRow executes the subquery for a multi-column SET
// assignment and caches the full result row in ctx.MultiAssignSubqCache keyed
// by the *MultiAssignSubqRow pointer. The cache is cleared per-row by the
// update executor before evaluating SET expressions.
func evalMultiAssignSubqRow(x *optimizer.MultiAssignSubqRow, row Row, ctx *Context) ([]Datum, error) {
	key := uintptr(unsafe.Pointer(x))
	if ctx.MultiAssignSubqCache != nil {
		if cached, ok := ctx.MultiAssignSubqCache[key]; ok {
			return cached, nil
		}
	}
	ctx.OuterRows = append(ctx.OuterRows, row)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
	op, err := Build(x.Plan)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return nil, err
	}
	defer func() { _ = op.Close() }()
	slot, err := op.Next()
	if err == EOF {
		// No rows: return a slice of NullDatum values.
		nulls := make([]Datum, x.NCols)
		for i := range nulls {
			nulls[i] = NullDatum
		}
		if ctx.MultiAssignSubqCache == nil {
			ctx.MultiAssignSubqCache = make(map[uintptr][]Datum)
		}
		ctx.MultiAssignSubqCache[key] = nulls
		return nulls, nil
	}
	if err != nil {
		return nil, err
	}
	resultRow := slotRow(slot)
	if len(resultRow) != x.NCols {
		return nil, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("subquery returned %d columns, expected %d", len(resultRow), x.NCols)}
	}
	// Clone result so it outlives the operator close.
	result := make([]Datum, len(resultRow))
	copy(result, resultRow)
	// Drain to detect multiple rows.
	if _, err2 := op.Next(); err2 != EOF {
		if err2 == nil {
			return nil, &ExecError{Code: "21000", Pos: x.Pos(), Message: "more than one row returned by a subquery used as an expression"}
		}
		return nil, err2
	}
	if ctx.MultiAssignSubqCache == nil {
		ctx.MultiAssignSubqCache = make(map[uintptr][]Datum)
	}
	ctx.MultiAssignSubqCache[key] = result
	return result, nil
}

// evalMultiAssignSubqElem evaluates one column of a multi-column SET subquery.
func evalMultiAssignSubqElem(x *optimizer.MultiAssignSubqElem, row Row, ctx *Context) (Datum, error) {
	result, err := evalMultiAssignSubqRow(x.Row, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if x.ColIdx < 0 || x.ColIdx >= len(result) {
		return NullDatum, nil
	}
	return result[x.ColIdx], nil
}

// evalCaseExpr evaluates the SQL CASE expression. Two forms:
//
//	-- searched: each WHEN is a boolean predicate
//	-- simple:   each WHEN is `Operand = When`
//
// First match wins; ELSE is the fallback. Per upstream, NULL
// WHEN evaluates as "not matched" — never NULL-true.
func evalCaseExpr(x *optimizer.CaseExpr, row Row, ctx *Context) (Datum, error) {
	var operand Datum
	hasOperand := x.Operand != nil
	if hasOperand {
		v, err := evalExpr(x.Operand, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		operand = v
	}
	for _, w := range x.Whens {
		whenVal, err := evalExpr(w.When, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		var matched bool
		if hasOperand {
			eq, err := compareEq(operand, whenVal)
			if err != nil {
				return Datum{}, err
			}
			matched = eq.Kind == KindBool && eq.BoolValue()
		} else {
			matched = whenVal.Kind == KindBool && whenVal.BoolValue()
		}
		if matched {
			return evalExpr(w.Then, row, ctx)
		}
	}
	if x.Else != nil {
		return evalExpr(x.Else, row, ctx)
	}
	return NullDatum, nil
}

// compareEq computes `a = b` returning a KindBool datum
// (KindNull if either side is NULL). Helper for the simple-form
// CASE; reuses upstream-shaped equality semantics across the
// types v0 understands.
func compareEq(a, b Datum) (Datum, error) {
	if a.IsNull() || b.IsNull() {
		return NullDatum, nil
	}
	// NUMERIC arms: route NUMERIC and NUMERIC↔INT cross-kind
	// comparisons through compareDatum so they use scale-aware
	// equality. Without this, `IN (49, 14, ...)` against a
	// NUMERIC column (TPC-H Q16's `p_size in (...)` shape) always
	// returns false because the int literals don't match
	// KindNumeric values directly.
	if a.Kind == KindNumeric || b.Kind == KindNumeric {
		// compareDatum errors only on truly incompatible kinds;
		// for IN-list semantics we want false on those, so swallow
		// the error and report not-equal.
		cmp, err := compareDatum(a, b, 0)
		if err != nil {
			return NewBoolDatum(false), nil
		}
		return NewBoolDatum(cmp == 0), nil
	}
	// M0073-0001: treat KindString and KindStringArena as
	// equivalent for equality (likewise KindBytes /
	// KindBytesArena). The arena variant is a storage detail;
	// the logical Kind is "string" / "bytes".
	aIsString := a.Kind == KindString
	bIsString := b.Kind == KindString
	switch {
	case a.Kind == KindInt && b.Kind == KindInt:
		return NewBoolDatum(a.Int == b.Int), nil
	case a.Kind == KindBool && b.Kind == KindBool:
		return NewBoolDatum(a.BoolValue() == b.BoolValue()), nil
	case aIsString && bIsString:
		return NewBoolDatum(a.StringValue() == b.StringValue()), nil
	case a.Kind == KindTime && b.Kind == KindTime:
		return NewBoolDatum(a.TimeValue().Equal(b.TimeValue())), nil
	case a.Kind == KindInt && bIsString:
		return NewBoolDatum(fmt.Sprintf("%d", a.Int) == b.StringValue()), nil
	case aIsString && b.Kind == KindInt:
		return NewBoolDatum(a.StringValue() == fmt.Sprintf("%d", b.Int)), nil
	// KindEnum vs string: compare by label (used in = ANY with array literals).
	// M0097-enum-any.
	case a.Kind == KindEnum && bIsString:
		return NewBoolDatum(string(a.Buf) == b.StringValue()), nil
	case aIsString && b.Kind == KindEnum:
		return NewBoolDatum(a.StringValue() == string(b.Buf)), nil
	case a.Kind == KindEnum && b.Kind == KindEnum:
		return NewBoolDatum(a.Int == b.Int), nil
	}
	// Cross-kind fallback: exactly one side is an unknown-typed string
	// literal. PostgreSQL resolves an IN list at parse time
	// (parse_expr.c transformAExprIn → select_common_type →
	// coerce_to_common_type), so `d_date IN ('2001-07-13')` compares
	// date-to-date. goopg types a bare StringConst as `unknown` and
	// resolves coercion at runtime instead (design doc
	// root-0019-unknown-literal-coercion.md), so the coercion has to
	// happen here. Without it `d_date IN ('2001-07-13')` fell through to
	// the not-equal return below while the equivalent
	// `d_date = '2001-07-13'` matched — the `=` path reaches
	// compareDatum → promoteCrossKind → tryParseStringAs, which parses
	// the literal, and compareEq bypassed all of it (TPC-DS Q83).
	//
	// Delegating to compareDatum reuses exactly that promotion. As in
	// the NUMERIC arm above, an incompatible pair is not-equal rather
	// than an error, which is what IN-list semantics want.
	if aIsString != bIsString {
		cmp, err := compareDatum(a, b, 0)
		if err != nil {
			return NewBoolDatum(false), nil
		}
		return NewBoolDatum(cmp == 0), nil
	}
	return NewBoolDatum(false), nil
}

// stringFuncArgTypeName maps a non-string Datum kind to the type name in PG's
// 42883 message for the string-length builtins ("function length(integer) does
// not exist"). The same guard lives in the sibling `length` case. M0119-0006
// (65th slice).
func stringFuncArgTypeName(k DatumKind) string {
	switch k {
	case KindInt:
		return "integer"
	case KindNumeric:
		return "numeric"
	case KindBool:
		return "boolean"
	case KindTime:
		return "timestamp"
	}
	return "unknown"
}

// declaredBpcharTypmod returns the declared typmod of a bpchar-family
// (char/character/bpchar) argument expression, or 0 when the argument is not
// bpchar-typed. PG's grammar gives bare `char`/`character` an implicit length
// of 1 (gram.y CHARACTER opt_charset), so a missing typmod normalizes to 1 —
// the same default synthesizeBareCharTypmod applies to casts and the bare-char
// column type carries. Used by octet_length, whose PG implementation
// (bpcharoctetlen) returns the blank-PADDED datum size. M0119-0006 (65th slice).
func declaredBpcharTypmod(e optimizer.Expr) int64 {
	var name string
	var typmod int64
	switch n := e.(type) {
	case *optimizer.ColumnRef:
		if n.Type.IsArray {
			return 0
		}
		name = n.Type.Name
		if len(n.Type.Args) > 0 {
			typmod = n.Type.Args[0]
		}
	case *optimizer.CastExpr:
		name = n.TargetType
		typmod = n.Typmod
	case *optimizer.FuncCall:
		// coalesce/greatest/least/nullif take the first argument's type
		// (planner.exprType), so octet_length(coalesce(charcol, '')) still sees
		// the declared width.
		switch strings.ToLower(n.Name) {
		case "coalesce", "greatest", "least", "nullif":
			if len(n.Args) > 0 {
				return declaredBpcharTypmod(n.Args[0])
			}
		}
		return 0
	default:
		return 0
	}
	switch strings.ToLower(name) {
	case "char", "bpchar", "character":
	default:
		return 0
	}
	if typmod <= 0 {
		return 1
	}
	return typmod
}

// evalFuncCall resolves a function name against the in-tree registry.
// v0 is small: current_timestamp / now / current_date are the only
// no-arg time functions pgbench needs; HammerDB TPC-H also uses
// to_timestamp(text, fmt) to load TIMESTAMP columns.
func evalFuncCall(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	name := strings.ToLower(x.Name)
	// Strip pg_catalog. prefix for matching — these are schema-qualified
	// versions of the same built-in functions.
	if after, ok := strings.CutPrefix(name, "pg_catalog."); ok {
		name = after
	}
	// Strip a *user* schema qualifier for the amcheck scalar builtins. Unlike
	// most builtins, pg_amcheck qualifies bt_index_check / bt_index_parent_check
	// with the amcheck extension's install schema (e.g. `"public".bt_index_check`),
	// not pg_catalog — so the pg_catalog strip above misses them and dispatch
	// would 42883. This mirrors the FROM-clause SRF path for verify_heapam, which
	// discards the schema qualifier for known amcheck builtins
	// (internal/parser/select.go, M0110-0003 AC-002 gap #5). Only the amcheck
	// builtin names are stripped, so a same-named user function is unaffected.
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		switch name[dot+1:] {
		case "bt_index_check", "bt_index_parent_check", "verify_heapam":
			name = name[dot+1:]
		}
	}
	switch name {
	case "bt_index_check":
		// amcheck B-tree structural verification (slice S4 of 0110-0008).
		return evalBtIndexCheck(x, slot, ctx, false)
	case "bt_index_parent_check":
		return evalBtIndexCheck(x, slot, ctx, true)
	case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
		// All four are declared `timestamp with time zone` upstream
		// (pg_proc.dat: now/statement_timestamp/transaction_timestamp prorettype
		// 1184), so they render through timestamptz_out. `localtimestamp` below
		// is the plain-`timestamp` sibling and deliberately keeps NewTimeDatum.
		// M0119-0006 (40th slice).
		return NewTimestampTZDatum(ctx.Now), nil
	case "current_date":
		// Tag the result TimeSubDate (M0134-0084): current_date shares the
		// KindTime carrier with timestamp, and NewTimeDatum leaves TimeSub
		// unset, so `current_date::text` rendered the full "YYYY-MM-DD
		// 00:00:00" timestamp shape instead of a bare date — the reason
		// `date(now())::text = current_date::text` (expressions.sql) always
		// compared unequal regardless of the actual day. NewDateDatum is the
		// same site every other date-producing path already uses.
		t := ctx.Now.UTC()
		return NewDateDatum(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)), nil
	case "current_time":
		// Returns time-of-day anchored at epoch, matching parseTimeString convention.
		// Accepts optional precision arg: current_time(N) rounds the fractional
		// seconds the same way `::time(N)` does — roundTimeDatumToPrecision
		// (AdjustTimeForTypmod, date.c:1710) ROUNDS, it does not truncate.
		// M0134-0084: the previous floor-via-integer-division here disagreed
		// with the CastExpr Typmod path on any nanosecond value that rounds up,
		// making `now()::time(3) = current_time(3)`-shaped comparisons flaky
		// (they only matched when the discarded digits happened to floor and
		// round to the same value).
		t := ctx.Now.UTC()
		d := NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC))
		if len(x.Args) > 0 {
			prec, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && prec.Kind == KindInt {
				d = roundTimeDatumToPrecision(d, prec.Int)
			}
		}
		return d, nil
	case "current_catalog":
		return NewStringDatum("postgres"), nil
	case "pg_client_encoding":
		return evalPgClientEncoding(ctx)
	case "getdatabaseencoding":
		return evalGetDatabaseEncoding(ctx)
	case "current_setting":
		if len(x.Args) >= 1 {
			nameArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || nameArg.IsNull() {
				return NullDatum, nil
			}
			missingOK := false
			if len(x.Args) >= 2 {
				missingArg, err := evalExprSlot(x.Args[1], slot, ctx)
				if err == nil && !missingArg.IsNull() {
					missingOK = missingArg.BoolValue()
				}
			}
			if ctx != nil {
				getSetting := ctx.GetSettingDisplay
				if getSetting == nil {
					getSetting = ctx.GetSetting
				}
				if getSetting != nil {
					if value, ok := getSetting(nameArg.StringValue()); ok {
						return NewStringDatum(value), nil
					}
				}
			}
			if missingOK {
				return NullDatum, nil
			}
			return Datum{}, &ExecError{
				Code:    "42704",
				Pos:     x.Pos(),
				Message: fmt.Sprintf("unrecognized configuration parameter %q", nameArg.StringValue()),
			}
		}
		return NullDatum, nil
	case "pg_sleep":
		return evalPgSleep(x, slot, ctx)
	case "to_timestamp":
		return evalToTimestamp(x, slot, ctx)
	case "to_date":
		return evalToDate(x, slot, ctx)
	case "substr", "substring":
		return evalSubstr(x, slot, ctx)
	case "overlay":
		return evalOverlay(x, slot, ctx)
	case "date_part":
		return evalDatePart(x, slot, ctx)
	case "date_trunc":
		return evalDateTrunc(x, slot, ctx)
	case "timezone":
		// Implements AT LOCAL (1-arg) and AT TIME ZONE (2-arg). M0097-0004.
		// One-arg:  timezone(timetz)       → convert to session local time (UTC for goopg).
		// Two-arg:  timezone(zone, timetz) → convert timetz to the given timezone.
		if len(x.Args) == 0 {
			return NullDatum, nil
		}
		var src Datum
		var zoneStr string
		if len(x.Args) == 1 {
			// AT LOCAL: session timezone is UTC.
			zoneStr = "UTC"
			var err error
			src, err = evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
		} else {
			// AT TIME ZONE: zone is first arg, value is second arg.
			zoneArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			if zoneArg.IsNull() {
				return NullDatum, nil
			}
			zoneStr = zoneArg.StringValue()
			src, err = evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
		}
		if src.IsNull() {
			return NullDatum, nil
		}
		if src.Kind != KindTime {
			// Unsupported input type: pass through.
			return src, nil
		}
		newOffsetSecs, err := parseTimezoneOffsetString(zoneStr)
		if err != nil {
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("time zone %q not recognized", zoneStr)}
		}
		oldOffsetSecs := src.TimeTZOffsetSecs()
		// Int stores LOCAL time nanoseconds (epoch-anchored). Compute UTC then
		// apply new offset.
		utcNanos := src.Int - int64(oldOffsetSecs)*1_000_000_000
		newLocalNanos := utcNanos + int64(newOffsetSecs)*1_000_000_000
		// Wrap within [0, 24h).
		const dayNanos = int64(24 * 3600 * 1_000_000_000)
		newLocalNanos = ((newLocalNanos % dayNanos) + dayNanos) % dayNanos
		result := src
		result.Int = newLocalNanos
		result.Scale = int16(newOffsetSecs / 60)
		return result, nil
	case "pg_get_viewdef":
		// pg_get_viewdef(view [, …]) → text: the view's defining SELECT.
		// goopg stores the raw view body (catalog.Table.ViewDef) captured at
		// parse time and echoes it here, terminated with ';' — pg_dump's
		// createViewAsClause strips the trailing ';' and wraps it in
		// `CREATE VIEW … AS <body>`. The first argument is an OID (pg_dump) or a
		// view name (psql). NULL for an unknown/non-view object, so callers that
		// pretty-print over an empty set are unaffected. M0110-0001 (DU-002).
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		var view *catalog.Table
		if arg.Kind == KindInt {
			if t, found := im.LookupTableByOID(uint32(arg.Int)); found {
				view = t
			}
		} else if v, perr := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32); perr == nil {
			if t, found := im.LookupTableByOID(uint32(v)); found {
				view = t
			}
		} else if name := strings.TrimSpace(arg.StringValue()); name != "" {
			if parsed, perr := parser.Parse("SELECT 1 FROM " + name); perr == nil && len(parsed) == 1 {
				if sel, ok := parsed[0].(*parser.SelectStmt); ok && len(sel.From) == 1 {
					rv := sel.From[0]
					if t, found := im.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name}); found {
						view = t
					}
				}
			}
		}
		if view == nil || view.View == nil || view.ViewDef == "" {
			return NullDatum, nil
		}
		def := view.ViewDef
		// A `CREATE VIEW v (c1, c2, …) AS …` explicit column list renames the
		// view's output columns. PG's pg_get_viewdef bakes those names into the
		// SELECT as `expr AS cN`; goopg captures the body verbatim (no deparser),
		// so splice the aliases into the raw select-list text. Skipped (raw text
		// returned) when the view has no explicit column list. M0110-0001 (DU-002).
		if len(view.ViewColumnAliases) > 0 {
			def = applyViewColumnAliases(def, view.ViewColumnAliases)
		}
		return NewStringDatum(def + ";"), nil
	case "pg_collation_for":
		// Left unchanged deliberately for the ordered-set-aggregate WITHIN
		// GROUP collation merge (M0134-0001 S20): this runtime path sees only
		// the evaluated Datum, never the parser-level WITHIN GROUP clause, so
		// it structurally cannot implement PG's merge rule. See "Sibling-pair
		// analysis" in docs/design/0134-0001-p8-ordered-set-agg-collation.md.
		if len(x.Args) == 1 {
			switch arg := x.Args[0].(type) {
			case *optimizer.StringConst:
				// Fast path: the planner's foldPgCollationFor already computed the
				// final answer at plan time (mirrors pg_typeof's fold). M0122-0005.
				return NewStringDatum(arg.Value), nil
			case *optimizer.NullConst:
				return NullDatum, nil
			case *optimizer.CollateExpr:
				// Runtime path (reached only if some resolver other than the main
				// planner.resolveExpr — e.g. plpgsql's expression compiler —
				// produced this call without going through the plan-time fold):
				// an explicit `expr COLLATE name` states its own collation.
				return NewStringDatum(catalog.QuoteCollationIdent(arg.CollationName)), nil
			}
			// Runtime path, no static fold available: approximate from the
			// evaluated Datum kind. A collatable (string) result defaults to
			// "default"; anything else is conservatively reported as having no
			// determinable collation rather than guessing a name PG wouldn't use.
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if v.Kind == KindString {
				return NewStringDatum("default"), nil
			}
		}
		return NullDatum, nil
	case "to_char":
		return evalToChar(x, slot, ctx)
	case "age":
		return evalAge(x, slot, ctx)
	case "make_date":
		return evalMakeDate(x, slot, ctx)
	case "make_timestamp", "make_timestamptz":
		return evalMakeTimestamp(x, slot, ctx)
	case "make_time":
		return evalMakeTime(x, slot, ctx)
	case "isfinite":
		return evalIsFinite(x, slot, ctx)
	case "justify_hours", "justify_days", "justify_interval":
		return evalJustify(name, x, slot, ctx)
	case "date_bin":
		return evalDateBin(x, slot, ctx)
	case "set_config":
		// set_config(setting_name, new_value, is_local) → text
		// vacuumdb calls SELECT pg_catalog.set_config('search_path', '', false)
		// to restrict the search path for security. Accept and return new_value.
		if len(x.Args) >= 2 {
			nameArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || nameArg.IsNull() {
				return NullDatum, nil
			}
			newVal, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			isLocal := false
			if len(x.Args) >= 3 {
				localArg, err := evalExprSlot(x.Args[2], slot, ctx)
				if err == nil && !localArg.IsNull() {
					isLocal = localArg.BoolValue()
				}
			}
			if ctx != nil && ctx.SetSetting != nil {
				if err := ctx.SetSetting(nameArg.StringValue(), newVal.Format(), isLocal); err != nil {
					return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(), Message: err.Error()}
				}
				getSetting := ctx.GetSettingDisplay
				if getSetting == nil {
					getSetting = ctx.GetSetting
				}
				if getSetting != nil {
					if value, ok := getSetting(nameArg.StringValue()); ok {
						return NewStringDatum(value), nil
					}
				}
			}
			return newVal, nil
		}
		return NullDatum, nil
	case "pg_backend_pid":
		// pg_backend_pid() → int4: the PID of the server process attached to the
		// current session. goopg is a single OS process multiplexing connections,
		// so the "PID" is the per-connection integer assigned at connect time
		// (s.nextPID) and reported in BackendKeyData; it is what
		// pg_stat_activity.pid joins on and what pg_cancel_backend(pid) targets.
		// Resolved via the activity registry (ctx.backendPID()), falling back to
		// the goopg.backend_pid GUC the server stamps at startup. M0118-0008
		// (detach-partition-concurrently-3/4 s2snitch step).
		if ctx != nil {
			pidStr := ctx.backendPID()
			if pidStr == "" && ctx.GetSetting != nil {
				if v, ok := ctx.GetSetting("goopg.backend_pid"); ok {
					pidStr = v
				}
			}
			if pidStr != "" {
				if n, err := strconv.ParseInt(pidStr, 10, 32); err == nil {
					return NewIntDatum(n), nil
				}
			}
		}
		return NewIntDatum(0), nil
	case "pg_my_temp_schema":
		// pg_my_temp_schema() → oid: the OID of the current session's temporary
		// namespace (pg_temp_<id>), or 0 (InvalidOid) if the session has not
		// created a temporary object. goopg models the per-backend temp namespace
		// in the shared catalog keyed by the session's temp-owner token; the
		// namespace is established lazily on the first CREATE TEMPORARY object and
		// persists until the session exits (matching PostgreSQL, which reuses
		// pg_temp_N even after every temp object is dropped). M0118-0009
		// (temp-schema-cleanup, design 0118-0091).
		if ctx != nil {
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
				oid := im.TempNamespaceOID(sessionTempOwner(ctx))
				return NewIntDatum(int64(oid)), nil
			}
		}
		return NewIntDatum(0), nil
	case "pg_cancel_backend":
		// pg_cancel_backend(pid int4) → bool: signal the backend whose
		// pg_backend_pid() == pid to cancel its currently-executing query (the
		// SQL analog of sending SIGINT). goopg multiplexes connections in one OS
		// process, so this reaches back to the server's process-wide cancel
		// registry via ctx.CancelBackend. Returns true if such a backend is
		// connected (signal sent — true even if the target is idle, matching PG),
		// false otherwise (PG additionally emits a WARNING for an unknown pid).
		// Strict: NULL arg → NULL. M0118-0008 (detach-partition-concurrently-3/4).
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		pidArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		if pidArg.IsNull() {
			return NullDatum, nil
		}
		if ctx != nil && ctx.CancelBackend != nil {
			return NewBoolDatum(ctx.CancelBackend(int32(pidArg.Int))), nil
		}
		return NewBoolDatum(false), nil
	case "pg_terminate_backend":
		// pg_terminate_backend(pid int4) → bool: terminate the backend whose
		// pg_backend_pid() == pid (the SQL analog of sending SIGTERM). When the
		// target is THIS backend (pid == our own pg_backend_pid()), the query is
		// aborted immediately via ErrSelfTerminate: the server emits the FATAL
		// "terminating connection due to administrator command" ErrorResponse and
		// closes the connection, so the client sees no result row — exactly as PG
		// does, where the SIGTERM is processed at CHECK_FOR_INTERRUPTS inside the
		// function and the connection dies before a value is returned. A peer pid
		// goes through ctx.TerminateBackend (process-wide registry) and returns a
		// bool. Strict: NULL arg → NULL. M0118-0009 (temp-schema-cleanup
		// process-exit permutation).
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		pidArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		if pidArg.IsNull() {
			return NullDatum, nil
		}
		target := int32(pidArg.Int)
		// Self-termination: abort the current query so the client receives only
		// the FATAL + connection close. Resolve our own backend PID the same way
		// pg_backend_pid() does.
		if ctx != nil {
			if selfStr := ctx.backendPID(); selfStr != "" {
				if self, perr := strconv.ParseInt(selfStr, 10, 32); perr == nil && int32(self) == target {
					return NullDatum, ErrSelfTerminate
				}
			}
		}
		if ctx != nil && ctx.TerminateBackend != nil {
			return NewBoolDatum(ctx.TerminateBackend(target)), nil
		}
		return NewBoolDatum(false), nil
	case "pg_notify":
		// pg_notify(channel text, payload text) → void: the SQL-function form of
		// the NOTIFY statement. Buffers a notification (delivered to LISTENers at
		// the current transaction's commit) via ctx.QueueNotify, which the server
		// wires to the connection's notify buffer. A NULL channel/payload is
		// treated as the empty string, matching pg_notify(PG_FUNCTION_ARGS)
		// (postgres/src/backend/commands/async.c:556-576), which then feeds
		// Async_Notify's length checks (async.c:604-621): an empty channel name
		// or one whose length reaches NAMEDATALEN (64, i.e. 63+ bytes) errors
		// with ERRCODE_INVALID_PARAMETER_VALUE. M0118-0009, M0134-0091.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		chArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		channel := ""
		if !chArg.IsNull() {
			channel = chArg.StringValue()
		}
		payload := ""
		if len(x.Args) >= 2 {
			pArg, perr := evalExprSlot(x.Args[1], slot, ctx)
			if perr != nil {
				return NullDatum, perr
			}
			if !pArg.IsNull() {
				payload = pArg.StringValue()
			}
		}
		if channel == "" {
			// Async_Notify's ereport has no errposition() call, so PG attaches
			// no cursor position to this error — Pos stays 0 (no LINE/^
			// pointer on the wire). M0134-0070/M0134-0091.
			return NullDatum, &ExecError{Code: "22023", Message: "channel name cannot be empty"}
		}
		if len(channel) >= 64 {
			return NullDatum, &ExecError{Code: "22023", Message: "channel name too long"}
		}
		if ctx != nil && ctx.QueueNotify != nil {
			ctx.QueueNotify(channel, payload)
		}
		// pg_notify returns void — a non-NULL empty value (like the advisory-lock
		// builtins), so `count(pg_notify(...))` counts every row (PostgreSQL's
		// async-notify `bignotify` step expects 1000, not 0 from count skipping
		// NULLs). It still renders as an empty field in a scalar SELECT.
		return NewStringDatum(""), nil
	case "pg_notification_queue_usage":
		// pg_notification_queue_usage() → float8 in [0, 1]: the fraction of the
		// asynchronous notification queue currently occupied by notifications
		// that have been committed but not yet delivered to every listener. The
		// server wires ctx.NotifyQueueUsage to notifyHub.QueueUsage; absent it
		// (unit/embedded contexts) the queue is reported empty. Rendered as a
		// formatted float8 like random(), so a `> 0` comparison resolves
		// correctly. M0118-0009 (async-notify).
		usage := 0.0
		if ctx != nil && ctx.NotifyQueueUsage != nil {
			usage = ctx.NotifyQueueUsage()
		}
		// Format with the minimal 'g' representation so an empty queue renders as
		// "0" (not "0.000000000000000"): a float8 string with a fractional part
		// of only zeros compares incorrectly against an integer literal in
		// goopg's text-vs-int comparison path, so `... > 0` would wrongly be true.
		// "0" compares correctly. M0118-0009.
		return NewStringDatum(strconv.FormatFloat(usage, 'g', -1, 64)), nil
	case "current_database":
		return NewStringDatum("postgres"), nil
	case "current_schema":
		return currentSchemaFromSearchPath(ctx)
	case "current_schemas":
		// current_schemas(include_implicit boolean) -> name[]. Returns the
		// schemas in the effective search path that actually exist, rendered as
		// a `{a,b}` text array literal so clients (e.g. pg_dump's parsePGArray)
		// can parse it. When include_implicit is true, the implicitly-searched
		// pg_catalog is prepended (mirrors PG's current_schemas semantics).
		includeImplicit := false
		if len(x.Args) >= 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && !v.IsNull() {
				includeImplicit = v.BoolValue()
			}
		}
		return currentSchemasArray(ctx, includeImplicit)

	// generate_series used as a scalar expression (not FROM clause).
	// Returns the start value only — full SRF semantics require planner rework.
	// Sufficient for CTAS patterns like `SELECT generate_series(1,10)` where
	// the table will have 1 row rather than N. M0096-0008.
	case "generate_series":
		if len(x.Args) >= 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			return v, nil
		}
		return NewIntDatum(1), nil

	// ── Enum support functions (M0097-0063) ──────────────────────────────
	// enum_first(anyenum) — first value in the enum ordering.
	// enum_last(anyenum) — last value in the enum ordering.
	// enum_range(anyenum) — all enum values as an array.
	// enum_range(anyenum, anyenum) — bounded range as an array.
	// Arguments are typically NULL::typename or value::typename casts; we
	// extract the type name from the CastExpr rather than the runtime value.
	case "enum_first":
		typeName := enumTypeNameFromArgs(x.Args)
		if typeName == "" || ctx == nil || ctx.Catalog == nil {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		et, ok := im.LookupEnum(typeName)
		if !ok || len(et.Values) == 0 {
			return NullDatum, nil
		}
		return NewStringDatum(et.Values[0].Label), nil

	case "enum_last":
		typeName := enumTypeNameFromArgs(x.Args)
		if typeName == "" || ctx == nil || ctx.Catalog == nil {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		et, ok := im.LookupEnum(typeName)
		if !ok || len(et.Values) == 0 {
			return NullDatum, nil
		}
		lastLabel := et.Values[len(et.Values)-1].Label
		if isUnsafeEnumValue(ctx, typeName, lastLabel) {
			return Datum{}, enumUnsafeError(lastLabel, typeName, x.Pos())
		}
		return NewStringDatum(lastLabel), nil

	case "enum_range", "enum_range_bounds":
		typeName := enumTypeNameFromArgs(x.Args)
		if typeName == "" || ctx == nil || ctx.Catalog == nil {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		et, ok := im.LookupEnum(typeName)
		if !ok {
			return NullDatum, nil
		}
		vals := et.Values
		if len(x.Args) >= 2 {
			// enum_range(lo, hi): lo=NULL means start from first; hi=NULL means end at last.
			loVal, loErr := evalExprSlot(x.Args[0], slot, ctx)
			hiVal, hiErr := evalExprSlot(x.Args[1], slot, ctx)
			if loErr == nil && !loVal.IsNull() {
				loStr := loVal.StringValue()
				for i, v := range vals {
					if strings.EqualFold(v.Label, loStr) {
						vals = vals[i:]
						break
					}
				}
			}
			if hiErr == nil && !hiVal.IsNull() {
				hiStr := hiVal.StringValue()
				for i, v := range vals {
					if strings.EqualFold(v.Label, hiStr) {
						vals = vals[:i+1]
						break
					}
				}
			}
		}
		// Check if any value in range is an unsafe pending enum value.
		for _, ev := range vals {
			if isUnsafeEnumValue(ctx, typeName, ev.Label) {
				return Datum{}, enumUnsafeError(ev.Label, typeName, x.Pos())
			}
		}
		// Convert []EnumValue → []string for formatTextArray.
		labels := make([]string, len(vals))
		for i, ev := range vals {
			labels[i] = ev.Label
		}
		return NewStringDatum(formatTextArray(labels)), nil

	// ── Advisory lock functions (M0096-0003) ──────────────────────────────
	// All variants block/return immediately depending on lock availability.
	// pg_advisory_lock / pg_advisory_xact_lock return non-NULL (void-like)
	// on success so that `IS NOT NULL` predicates in WHERE clauses evaluate
	// to true (matching PostgreSQL's behaviour for void-returning functions).

	case "pg_advisory_lock":
		// pg_advisory_lock(bigint) or pg_advisory_lock(int4, int4) → void
		return evalAdvisoryLock(x, slot, ctx, false, false, false)

	case "pg_advisory_unlock":
		// pg_advisory_unlock(bigint) → boolean
		// pg_advisory_unlock(int4, int4) → boolean
		return evalAdvisoryUnlock(x, slot, ctx, false)

	case "pg_advisory_unlock_all":
		// pg_advisory_unlock_all() → void
		return evalAdvisoryUnlockAll(ctx)

	case "pg_advisory_xact_lock":
		// pg_advisory_xact_lock(bigint) or pg_advisory_xact_lock(int4, int4) → void  (xact-scoped)
		return evalAdvisoryLock(x, slot, ctx, false, true, false)

	case "pg_try_advisory_xact_lock":
		// pg_try_advisory_xact_lock(bigint) or pg_try_advisory_xact_lock(int4, int4) → boolean
		return evalAdvisoryLock(x, slot, ctx, true, true, false)

	case "pg_try_advisory_lock":
		// pg_try_advisory_lock(bigint) → boolean  (non-blocking)
		return evalAdvisoryLock(x, slot, ctx, true, false, false)

	// ── Shared-mode advisory lock variants (M0097-0021) ──────────────────
	case "pg_advisory_lock_shared":
		// pg_advisory_lock_shared(bigint) or pg_advisory_lock_shared(int4, int4) → void
		return evalAdvisoryLock(x, slot, ctx, false, false, true)
	case "pg_advisory_xact_lock_shared":
		// pg_advisory_xact_lock_shared(bigint) or pg_advisory_xact_lock_shared(int4, int4) → void
		return evalAdvisoryLock(x, slot, ctx, false, true, true)
	case "pg_try_advisory_lock_shared":
		// pg_try_advisory_lock_shared(bigint) → boolean
		return evalAdvisoryLock(x, slot, ctx, true, false, true)
	case "pg_try_advisory_xact_lock_shared":
		// pg_try_advisory_xact_lock_shared(bigint) → boolean
		return evalAdvisoryLock(x, slot, ctx, true, true, true)
	case "pg_advisory_unlock_shared":
		// pg_advisory_unlock_shared(bigint) or pg_advisory_unlock_shared(int4, int4) → boolean
		return evalAdvisoryUnlock(x, slot, ctx, true)

	// ── Boolean comparison functions (M0097-0003) ─────────────────────────
	// These are the C-level backing functions for bool operators; the
	// boolean.sql regress test calls them explicitly.
	case "booleq":
		if len(x.Args) == 2 {
			a, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			b, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err2 != nil {
				return NullDatum, nil
			}
			if a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() == b.BoolValue()), nil
		}
	case "boolne":
		if len(x.Args) == 2 {
			a, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			b, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err2 != nil {
				return NullDatum, nil
			}
			if a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() != b.BoolValue()), nil
		}

	// ── Aggregate state functions for bool_and / bool_or ─────────────────
	// These are strict (return NULL if either arg is NULL), matching PG's
	// booland_statefunc / boolor_statefunc internals.
	case "booland_statefunc":
		if len(x.Args) == 2 {
			a, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || a.IsNull() {
				return NullDatum, nil
			}
			b, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err2 != nil || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() && b.BoolValue()), nil
		}
	case "boolor_statefunc":
		if len(x.Args) == 2 {
			a, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || a.IsNull() {
				return NullDatum, nil
			}
			b, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err2 != nil || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() || b.BoolValue()), nil
		}

	case "array_subscript":
		// Array element access: arr[idx] (1-based). Used for SQL a[N] syntax. M0097-0003.
		// Returns the element as its natural type (int for integer arrays, else text).
		if len(x.Args) == 2 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			idxDatum, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() || idxDatum.IsNull() {
				return NullDatum, nil
			}
			n := idxDatum.Int
			sv := arr.StringValue()
			if len(sv) >= 2 && sv[0] == '(' {
				// Geometric point "(x,y)": PostgreSQL subscripts a point
				// 0-based, returning the i-th coordinate as float8
				// (point[0]=x, point[1]=y). goopg backs `point` with its text
				// representation, so detect the literal shape here. Only
				// indices 0/1 are defined; anything else yields NULL.
				if pt, ok := parsePointText(sv); ok {
					if n == 0 || n == 1 {
						s := strconv.FormatFloat(pt[n], 'f', -1, 64)
						if m, sc, perr := parseNumeric(s); perr == nil {
							return newNumeric(m, int(sc)), nil
						}
						return NewStringDatum(s), nil
					}
					return NullDatum, nil
				}
			}
			if len(sv) < 2 || sv[0] != '{' {
				// Not an array literal: a fixed-length pseudo-array type — most
				// importantly `name` — is being subscripted. PostgreSQL indexes
				// these 0-based and returns the Nth character (as "char").
				// pg_dump's getTypes detects an auto-generated array type with
				// `typname[0] = '_'`; without 0-based name subscripting that
				// test is NULL and the array type wrongly dumps as a base type.
				// DU-002 slice 89.
				runes := []rune(sv)
				if n < 0 || int(n) >= len(runes) {
					return NullDatum, nil
				}
				return NewStringDatum(string(runes[n])), nil
			}
			elems := parseTextArray(sv)
			if n < 1 || int(n) > len(elems) {
				return NullDatum, nil
			}
			elem := elems[n-1]
			// The planner stamped the element type onto ReturnType (its exprType
			// array_subscript arm). Re-type the element text through that type's
			// own input path so the subscript yields the element type's Datum
			// kind instead of text — otherwise compareDatum never reaches the
			// interval_cmp_value / numeric ladders and `c[1] = c[2]` compares the
			// two element SPELLINGS. M0119-0006.
			if d, ok := arraySubscriptElemDatum(x.ReturnType, elem); ok {
				return d, nil
			}
			// Try to infer element type: if the element looks like a plain integer
			// (no decimal point, no quotes), return an integer datum for correct
			// psql alignment and comparison semantics. Matches PG's behaviour where
			// ARRAY[1,2,3][1] returns int4, not text.
			if iv, err2 := strconv.ParseInt(elem, 10, 64); err2 == nil && !strings.Contains(elem, ".") {
				return NewIntDatum(iv), nil
			}
			return NewStringDatum(elem), nil
		}
		return NullDatum, nil

	case "array_slice":
		// Array slice access: arr[lower:upper] (1-based, inclusive, both
		// bounds optional — the planner stamps an omitted bound as a
		// NullConst literal). PG's array_ref (arrayfuncs.c) clamps an
		// out-of-range bound to the array's actual bound rather than
		// erroring, and lower > upper yields an empty array — never NULL
		// or an error. M0134-0079.
		if len(x.Args) == 3 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() {
				return NullDatum, nil
			}
			sv := arr.StringValue()
			elems := splitArrayElements(sv)
			if elems == nil {
				// Not array-literal text (e.g. a fixed-length pseudo-array
				// type) — slicing isn't defined for it; fall back to NULL
				// rather than mis-splitting arbitrary text.
				return NullDatum, nil
			}
			bound := func(argExpr optimizer.Expr, deflt int) (int, error) {
				if _, isNull := argExpr.(*optimizer.NullConst); isNull {
					return deflt, nil
				}
				d, err := evalExprSlot(argExpr, slot, ctx)
				if err != nil {
					return 0, err
				}
				if d.IsNull() {
					return deflt, nil
				}
				return int(d.Int), nil
			}
			lower, err := bound(x.Args[1], 1)
			if err != nil {
				return NullDatum, err
			}
			upper, err := bound(x.Args[2], len(elems))
			if err != nil {
				return NullDatum, err
			}
			if lower < 1 {
				lower = 1
			}
			if upper > len(elems) {
				upper = len(elems)
			}
			if lower > upper {
				return NewStringDatum("{}"), nil
			}
			var sb strings.Builder
			sb.WriteByte('{')
			for i := lower; i <= upper; i++ {
				if i > lower {
					sb.WriteByte(',')
				}
				sb.WriteString(elems[i-1])
			}
			sb.WriteByte('}')
			return NewStringDatum(sb.String()), nil
		}
		return NullDatum, nil

	case "array_upper":
		// array_upper(anyarray, int) → int: upper bound of specified dimension (1-based).
		// For 1-D arrays, returns the number of elements (lower is always 1).
		// Returns NULL for empty arrays, NULL inputs, or dim != 1.
		if len(x.Args) == 2 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			dimDatum, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() || dimDatum.IsNull() {
				return NullDatum, nil
			}
			dim := dimDatum.Int
			if dimDatum.Kind == KindString {
				dim, _ = strconv.ParseInt(dimDatum.StringValue(), 10, 64)
			}
			if dim != 1 {
				return NullDatum, nil
			}
			elems := parseTextArray(arr.StringValue())
			if len(elems) == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(int64(len(elems))), nil
		}
		return NullDatum, nil

	case "array_lower":
		// array_lower(anyarray, int) → int: lower bound of specified dimension.
		// For standard PostgreSQL arrays the lower bound is always 1.
		// Returns NULL for empty arrays, NULL inputs, or dim != 1.
		if len(x.Args) == 2 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			dimDatum, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() || dimDatum.IsNull() {
				return NullDatum, nil
			}
			dim := dimDatum.Int
			if dimDatum.Kind == KindString {
				dim, _ = strconv.ParseInt(dimDatum.StringValue(), 10, 64)
			}
			if dim != 1 {
				return NullDatum, nil
			}
			elems := parseTextArray(arr.StringValue())
			if len(elems) == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(1), nil
		}
		return NullDatum, nil

	case "array_length":
		// array_length(anyarray, int) → int: number of elements in the specified dimension.
		// Equivalent to array_upper - array_lower + 1 = upper (since lower=1).
		// Returns NULL for empty arrays, NULL inputs, or dim != 1.
		if len(x.Args) == 2 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			dimDatum, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() || dimDatum.IsNull() {
				return NullDatum, nil
			}
			dim := dimDatum.Int
			if dimDatum.Kind == KindString {
				dim, _ = strconv.ParseInt(dimDatum.StringValue(), 10, 64)
			}
			if dim != 1 {
				return NullDatum, nil
			}
			elems := parseTextArray(arr.StringValue())
			if len(elems) == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(int64(len(elems))), nil
		}
		return NullDatum, nil

	case "cardinality":
		// cardinality(anyarray) → int: total number of elements across all
		// dimensions (postgres/src/backend/utils/adt/array_userfuncs.c
		// array_cardinality → ArrayGetNItems). Unlike array_length, takes no
		// dimension argument and returns 0 (not NULL) for an empty array;
		// NULL input still yields NULL. goopg arrays are 1-D only, so this is
		// just the element count.
		if len(x.Args) == 1 {
			arr, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() {
				return NullDatum, nil
			}
			elems := parseTextArray(arr.StringValue())
			return NewIntDatum(int64(len(elems))), nil
		}
		return NullDatum, nil

	case "array_fill":
		// array_fill(val, dims_array[, lb_array]) → fills an array with val repeated N times.
		// array_fill(1.0, ARRAY[4]) = {1.0,1.0,1.0,1.0}. Only 1-D supported. M0097-0113.
		if len(x.Args) >= 2 {
			val, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			dimsD, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if val.IsNull() || dimsD.IsNull() {
				return NullDatum, nil
			}
			// dimsD is an array like {4} — parse it and get the first dimension.
			dimElems := parseTextArray(dimsD.StringValue())
			n := int64(0)
			if len(dimElems) > 0 {
				n, _ = strconv.ParseInt(dimElems[0], 10, 64)
			}
			valStr := val.Format()
			if val.IsNull() {
				valStr = "NULL"
			}
			elems := make([]string, n)
			for i := range elems {
				elems[i] = valStr
			}
			return NewStringDatum("{" + strings.Join(elems, ",") + "}"), nil
		}
		return NullDatum, nil

	case "array_construct":
		// array_construct(e1, e2, ...) → text representation of array {v1,v2,...}
		// Used to evaluate ARRAY[e1, e2, ...] constructors. M0097-0042.
		var sb strings.Builder
		sb.WriteByte('{')
		for i, arg := range x.Args {
			if i > 0 {
				sb.WriteByte(',')
			}
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if v.IsNull() {
				sb.WriteString("NULL")
			} else {
				sb.WriteString(v.Format())
			}
		}
		sb.WriteByte('}')
		return NewStringDatum(sb.String()), nil

	case "row":
		// ROW(e1, e2, ...) → composite record literal displayed as (v1,v2,...).
		// PostgreSQL's row constructor; the parser folds ROW(...) into a FuncCall
		// with name "row". Used in union.sql set-op tests. M0097-0042.
		var sbRow strings.Builder
		sbRow.WriteByte('(')
		for i, arg := range x.Args {
			if i > 0 {
				sbRow.WriteByte(',')
			}
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil {
				return NullDatum, err
			}
			if v.IsNull() {
				// PostgreSQL row constructor: NULL elements appear as empty fields.
				// e.g. ROW(0,NULL,NULL) → "(0,,)" matching composite type display.
			} else {
				s := v.Format()
				// Quote values containing commas, parens, quotes, backslashes, spaces.
				needsQ := false
				if s == "" {
					needsQ = true
				} else {
					for _, c := range s {
						if c == ',' || c == '(' || c == ')' || c == '"' || c == '\\' || c == ' ' || c == '\t' {
							needsQ = true
							break
						}
					}
				}
				if needsQ {
					sbRow.WriteByte('"')
					for _, c := range s {
						if c == '"' || c == '\\' {
							sbRow.WriteByte('\\')
						}
						sbRow.WriteRune(c)
					}
					sbRow.WriteByte('"')
				} else {
					sbRow.WriteString(s)
				}
			}
		}
		sbRow.WriteByte(')')
		return NewStringDatum(sbRow.String()), nil

	case "parse_ident":
		// parse_ident(str text [, strict boolean = true]) → text[]
		// Parses a qualified SQL identifier string and returns its components
		// as a text array {comp1,comp2,...}. M0097-0003.
		if len(x.Args) >= 1 {
			strDatum, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || strDatum.IsNull() {
				return NullDatum, nil
			}
			strict := true
			if len(x.Args) >= 2 {
				strictDatum, err2 := evalExprSlot(x.Args[1], slot, ctx)
				if err2 == nil && !strictDatum.IsNull() {
					strict = strictDatum.BoolValue()
				}
			}
			input := strDatum.StringValue()
			components, msg, detail := parseIdentString(input, strict)
			if msg != "" {
				return Datum{}, &ExecError{Code: "42602", Pos: x.Pos(), Message: msg, Detail: detail}
			}
			// Format as PostgreSQL text array: {comp1,"comp2",...}
			return NewStringDatum(formatTextArray(components)), nil
		}
		return NullDatum, nil

	// ── pg_input_is_valid / pg_input_error_info stubs (M0097-0003) ───────
	// These PostgreSQL 16+ functions validate whether a string is valid input
	// for a given type. Stub returns true (best-effort) — returning an error
	// would cause boolean.sql to hang waiting for a SRF response.
	case "pg_input_is_valid":
		// M0097-0018: enhanced to validate xid/xid8 inputs.
		if len(x.Args) == 2 {
			val, _ := evalExprSlot(x.Args[0], slot, ctx)
			typName, _ := evalExprSlot(x.Args[1], slot, ctx)
			if val.IsNull() || typName.IsNull() {
				return NullDatum, nil
			}
			v := strings.TrimSpace(val.StringValue())
			t := strings.ToLower(strings.TrimSpace(typName.StringValue()))
			switch t {
			case "bool", "boolean":
				return NewBoolDatum(isValidBoolInput(v)), nil
			case "int2", "smallint":
				_, err := parseIntegerInput(v, "smallint", 16)
				return NewBoolDatum(err == nil), nil
			case "int4", "integer", "int":
				_, err := parseIntegerInput(v, "integer", 32)
				return NewBoolDatum(err == nil), nil
			case "int8", "bigint":
				_, err := parseIntegerInput(v, "bigint", 64)
				return NewBoolDatum(err == nil), nil
			case "float4", "real":
				_, err := strconv.ParseFloat(v, 32)
				return NewBoolDatum(err == nil), nil
			case "float8", "double precision":
				_, err := strconv.ParseFloat(v, 64)
				return NewBoolDatum(err == nil), nil
			case "oid":
				// oid is uint32: 0..4294967295. Negative wraps around. M0097-0003.
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil && n < 0 {
					n += 4294967296
				}
				return NewBoolDatum(err == nil && n >= 0 && n <= 4294967295), nil
			case "oidvector":
				// oidvector: space-separated oid values. M0097-0003.
				msg, _ := validateOidVector(v)
				return NewBoolDatum(msg == ""), nil
			case "uuid":
				return NewBoolDatum(isValidUUIDStr(v)), nil
			case "tid":
				_, _, ok := parseTidInput(v)
				return NewBoolDatum(ok), nil
			case "xid":
				_, err := parseXid(v)
				return NewBoolDatum(err == nil), nil
			case "xid8":
				_, err := parseXid8(v)
				return NewBoolDatum(err == nil), nil
			case "pg_snapshot":
				return NewBoolDatum(parsePgSnapshotValid(v)), nil
			case "pg_lsn":
				_, err := parsePgLSN(v)
				return NewBoolDatum(err == nil), nil
			case "time", "timetz":
				_, err := parseTimeString(v)
				return NewBoolDatum(err == nil), nil
			case "date":
				// 'infinity' / '-infinity' are valid date input (#5(d-iv)).
				if _, ok := parseDateInfinityLiteral(v); ok {
					return NewBoolDatum(true), nil
				}
				// M0125-0007 / M0119-0006: pg_input_is_valid must agree with the
				// cast path, so it goes through the same entry point (unpadded
				// fields, trailing era token, no year zero).
				_, err := parsePGDateText(v)
				return NewBoolDatum(err == nil), nil
			case "timestamp", "timestamptz":
				// 'infinity' / '-infinity' are valid timestamp input (#5(d-iv)).
				if _, ok := parseTimestampInfinityLiteral(v); ok {
					return NewBoolDatum(true), nil
				}
				_, err := parseCopyTimestampZone(v, tsZoneModeForType(t))
				return NewBoolDatum(err == nil), nil
			case "bytea":
				// bytea: reuse byteaIn (bytea.go, mirrors byteain() in
				// varlena.c) so the valid/invalid answer agrees with the CAST
				// path and pg_input_error_info. M0134-0070.
				_, err := byteaIn(v, 0)
				return NewBoolDatum(err == nil), nil
			case "box":
				// M0134-0094: agrees with the typed-literal/coercion path,
				// same parseBoxLiteral.
				_, _, _, _, ok := parseBoxLiteral(v)
				return NewBoolDatum(ok), nil
			case "circle":
				// M0134-0098: agrees with the typed-literal/coercion path,
				// same parseCircleLiteral.
				_, _, _, ok := parseCircleLiteral(v)
				return NewBoolDatum(ok), nil
			default:
				// varchar(N) / character varying(N) / char(N) / bpchar(N). M0097-0003.
				if valid, ok := pgInputIsValidTypedLen(v, t); ok {
					return NewBoolDatum(valid), nil
				}
				// Check if it's a registered enum type. M0097-0071.
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if et, isEnum := im.LookupEnum(t); isEnum {
						for _, ev := range et.Values {
							if strings.EqualFold(ev.Label, v) {
								return NewBoolDatum(true), nil
							}
						}
						return NewBoolDatum(false), nil
					}
				}
			}
		}
		return NewBoolDatum(true), nil
	case "pg_input_error_info":
		return NullDatum, nil

	// ── UUID functions ─────────────────────────────────────────────────────
	case "gen_random_uuid", "uuidv4":
		u, genErr := genUUIDv4()
		if genErr != nil {
			return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "gen_random_uuid: " + genErr.Error()}
		}
		return NewStringDatum(u), nil
	case "uuidv7":
		var uuidV7Ns int64
		if len(x.Args) == 1 {
			iv, ivErr := evalExprSlot(x.Args[0], slot, ctx)
			if ivErr != nil {
				return NullDatum, ivErr
			}
			if iv.Kind == KindInterval {
				shifted, aerr := addTimeInterval(NewTimeDatum(ctx.Now), iv, false, x.Pos())
				if aerr != nil {
					return NullDatum, aerr
				}
				ts := shifted.TimeValue()
				u, genErr := genUUIDv7FromTime(ts)
				if genErr != nil {
					return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "uuidv7: " + genErr.Error()}
				}
				return NewStringDatum(u), nil
			} else {
				uuidV7Ns = uuidV7RealTimeNs()
			}
		} else {
			uuidV7Ns = uuidV7RealTimeNs()
		}
		u, genErr := genUUIDv7(uuidV7Ns)
		if genErr != nil {
			return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "uuidv7: " + genErr.Error()}
		}
		return NewStringDatum(u), nil
	case "uuid_extract_version":
		if len(x.Args) == 1 {
			v, evalErr := evalExprSlot(x.Args[0], slot, ctx)
			if evalErr != nil || v.IsNull() {
				return NullDatum, evalErr
			}
			b, ok := uuidToBytes(v.StringValue())
			if !ok || b[8]&0xC0 != 0x80 {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(b[6] >> 4)}, nil
		}
		return NullDatum, nil
	case "uuid_extract_timestamp":
		if len(x.Args) == 1 {
			v, evalErr := evalExprSlot(x.Args[0], slot, ctx)
			if evalErr != nil || v.IsNull() {
				return NullDatum, evalErr
			}
			b, ok := uuidToBytes(v.StringValue())
			if !ok || b[8]&0xC0 != 0x80 {
				return NullDatum, nil
			}
			switch b[6] >> 4 {
			case 1:
				timeLow := uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
				timeMid := uint64(b[4])<<8 | uint64(b[5])
				timeHi := uint64(b[6]&0x0F)<<8 | uint64(b[7])
				gregTicks := (timeHi << 48) | (timeMid << 32) | timeLow
				const gregToUnix = uint64(0x01B21DD213814000)
				unixNs := (int64(gregTicks) - int64(gregToUnix)) * 100
				return NewTimeDatum(time.Unix(0, unixNs).UTC()), nil
			case 7:
				ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 |
					int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
				return NewTimeDatum(time.UnixMilli(ms).UTC()), nil
			}
		}
		return NullDatum, nil

	// ── Size functions (M0097-0018) ───────────────────────────────────────
	case "pg_size_pretty":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return NewStringDatum(sizePretty(v.Int)), nil
			}
			// KindNumeric and other: use exact big.Int/big.Rat arithmetic
			// to match PG's pg_size_pretty(numeric) algorithm. M0097-0018.
			return NewStringDatum(sizePrettyBig(strings.TrimSpace(v.Format()))), nil
		}

	case "pg_size_bytes":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			bytes, err2 := parseSizeBytes(s.StringValue())
			if err2 != nil {
				if ee, ok := err2.(*ExecError); ok {
					return Datum{}, ee
				}
				return Datum{}, &ExecError{Code: "22023", Message: err2.Error()}
			}
			return Datum{Kind: KindInt, Int: bytes}, nil
		}

	case "pg_database_size":
		// Stub: return 8 MB. M0097-0018.
		return Datum{Kind: KindInt, Int: 8 * 1024 * 1024}, nil

	case "pg_relation_size":
		return evalPgRelationSize(x, slot, ctx)

	case "pg_total_relation_size":
		return evalPgTotalRelationSize(x, slot, ctx)

	case "pg_indexes_size":
		return evalPgIndexesSize(x, slot, ctx)

	case "pg_table_size":
		return evalPgTableSize(x, slot, ctx)

	// ── xid8 comparison function (M0097-0018) ─────────────────────────────
	case "xid8cmp":
		if len(x.Args) == 2 {
			a, err1 := evalExprSlot(x.Args[0], slot, ctx)
			b, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err1 != nil || err2 != nil || a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			var aVal, bVal uint64
			if a.Kind == KindInt {
				aVal = uint64(a.Int)
			} else {
				aVal, _ = strconv.ParseUint(strings.TrimSpace(a.StringValue()), 10, 64)
			}
			if b.Kind == KindInt {
				bVal = uint64(b.Int)
			} else {
				bVal, _ = strconv.ParseUint(strings.TrimSpace(b.StringValue()), 10, 64)
			}
			if aVal < bVal {
				return Datum{Kind: KindInt, Int: -1}, nil
			}
			if aVal > bVal {
				return Datum{Kind: KindInt, Int: 1}, nil
			}
			return Datum{Kind: KindInt, Int: 0}, nil
		}
		return NullDatum, nil

	// ── Hash / crypto functions (M0097-0011) ─────────────────────────────
	case "md5":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			h := md5.Sum([]byte(v.StringValue()))
			return NewStringDatum(hex.EncodeToString(h[:])), nil
		}
	case "sha224":
		// sha224(bytea) -> bytea (cryptohashfuncs.c sha224_bytea, PG_RETURN_BYTEA_P).
		// Digest length 28 bytes. postgres/src/include/common/sha2.h:20.
		// M0134-0070 — previously fell through the switch ("function does not exist").
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			h := sha256.Sum224(raw)
			return NewBytesDatum(h[:]), nil
		}
	case "sha256":
		// sha256(bytea) -> bytea (cryptohashfuncs.c sha256_bytea, PG_RETURN_BYTEA_P).
		// Digest length 32 bytes. postgres/src/include/common/sha2.h:21.
		// M0134-0070 — used to return hex TEXT (KindString) instead of bytea.
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			h := sha256.Sum256(raw)
			return NewBytesDatum(h[:]), nil
		}
	case "sha384":
		// sha384(bytea) -> bytea (cryptohashfuncs.c sha384_bytea, PG_RETURN_BYTEA_P).
		// Digest length 48 bytes. postgres/src/include/common/sha2.h:22.
		// M0134-0070 — previously fell through the switch ("function does not exist").
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			h := sha512.Sum384(raw)
			return NewBytesDatum(h[:]), nil
		}
	case "sha512":
		// sha512(bytea) -> bytea (cryptohashfuncs.c sha512_bytea, PG_RETURN_BYTEA_P).
		// Digest length 64 bytes. postgres/src/include/common/sha2.h:23.
		// M0134-0070 — used to return hex TEXT (KindString) instead of bytea.
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			h := sha512.Sum512(raw)
			return NewBytesDatum(h[:]), nil
		}
	case "digest":
		// digest(text, algorithm) — subset: only 'md5', 'sha256', 'sha512'
		if len(x.Args) == 2 {
			s, err1 := evalExprSlot(x.Args[0], slot, ctx)
			alg, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err1 != nil || err2 != nil || s.IsNull() || alg.IsNull() {
				return NullDatum, nil
			}
			switch strings.ToLower(alg.StringValue()) {
			case "md5":
				h := md5.Sum([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			case "sha256":
				h := sha256.Sum256([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			case "sha512":
				h := sha512.Sum512([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			}
		}
		return NullDatum, nil

	// ── POSIX regex functions (M0097-0011) ────────────────────────────────
	case "regexp_match", "regexp_matches":
		// regexp_match(string, pattern [, flags]) → text[] (at most one match)
		// regexp_matches(string, pattern [, flags]) → setof text[] (PG is an
		// SRF that yields one row per match with the 'g' flag). This scalar
		// path handles regexp_match always, plus regexp_matches whenever it
		// is NOT a bare SELECT-list target (e.g. nested in a larger
		// expression, or in a context buildSelectSrfProjectSet doesn't cover
		// such as WHERE/GROUP BY) — it returns at most the FIRST match's
		// capture-group array, matching regexp_match's own behavior. A bare
		// `regexp_matches(...)` SELECT-list target is instead planned as a
		// RegexpMatchesCol and expanded per-match by projectSetOp (see
		// operators_project_set.go / evalRegexpMatchesSRF), which is the only
		// path that honours the 'g' flag's one-row-per-match semantics. The
		// FROM-clause form (`FROM regexp_matches(...)`) is still unwired
		// (ledger M0122-0002/regexp_matches-srf).
		if len(x.Args) >= 2 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			pat, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || pat.IsNull() {
				return NullDatum, nil
			}
			caseInsensitive := false
			if len(x.Args) >= 3 {
				flags, e3 := evalExprSlot(x.Args[2], slot, ctx)
				if e3 == nil && !flags.IsNull() {
					caseInsensitive = strings.Contains(flags.StringValue(), "i")
				}
			}
			pattern := pat.StringValue()
			if caseInsensitive {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return NullDatum, nil
			}
			return regexpFirstMatchArray(re, s.StringValue()), nil
		}

	// ── Sequence functions (M0097-0009) ───────────────────────────────────
	case "nextval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExprSlot(a, slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalNextval(args, ctx)
	case "currval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExprSlot(a, slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalCurrval(args, ctx)
	case "setval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExprSlot(a, slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalSetval(args, ctx)
	case "lastval":
		return evalLastval(ctx)
	case "pg_get_serial_sequence":
		// pg_get_serial_sequence(table_name, column_name) → text
		// Real PG (ruleutils.c pg_get_serial_sequence) resolves the column's
		// attnum then scans pg_depend for an auto/internal dependency from a
		// sequence relation onto that column, returning NULL when the column
		// owns no sequence. Previously this fabricated "table_column_seq"
		// unconditionally — wrong for a renamed sequence, an explicit OWNED BY
		// target, or any plain (non-serial) column. M0122 follow-up.
		if len(x.Args) == 2 {
			tbl, err1 := evalExprSlot(x.Args[0], slot, ctx)
			col, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err1 != nil || err2 != nil || tbl.IsNull() || col.IsNull() {
				return NullDatum, nil
			}
			tblArg := strings.ToLower(strings.TrimSpace(tbl.StringValue()))
			colName := strings.ToLower(strings.TrimSpace(col.StringValue()))
			// Ownership is recorded under the bare table name (see
			// SetSequenceOwnedBy call sites in operators_ddl.go/
			// operators_sequence.go), so strip any schema qualifier the
			// caller passed before matching.
			bareTbl := tblArg
			if i := strings.LastIndex(tblArg, "."); i >= 0 {
				bareTbl = tblArg[i+1:]
			}
			seqName := FindSequenceOwnedBy(bareTbl+"."+colName, ctxSeqDBOid(ctx))
			if seqName == "" {
				return NullDatum, nil
			}
			schema := "public"
			if s := LookupSequence(seqName, ctxSeqDBOid(ctx)); s != nil && s.schema != "" {
				schema = s.schema
			}
			return NewStringDatum(pgQuoteIdent(schema) + "." + pgQuoteIdent(seqName)), nil
		}
		return NullDatum, nil
	case "pg_get_indexdef":
		// pg_get_indexdef(indexrelid) → text — reconstructs CREATE INDEX DDL.
		// M0097-0023. The 3-arg form pg_get_indexdef(indexrelid, colno,
		// pretty) is pg_get_indexdef_ext (ruleutils.c:1198-1217): colno == 0
		// behaves exactly like the 1-arg form; colno != 0 selects just the
		// Nth index attribute's bare column name/expression with none of the
		// CREATE INDEX decoration (catalog.BuildIndexDefColumn). `pretty`
		// (3rd arg) only affects real PG's line-wrapping of long expression
		// text (PRETTYFLAG_INDENT) — goopg has no multi-line expression
		// deparse, so it is parsed but has no observable effect. M0134-0002
		// C19.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		var colno int
		if len(x.Args) >= 2 {
			colArg, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return NullDatum, nil
			}
			if !colArg.IsNull() {
				if colArg.Kind == KindInt {
					colno = int(colArg.Int)
				} else {
					v, _ := strconv.ParseInt(strings.TrimSpace(colArg.StringValue()), 10, 32)
					colno = int(v)
				}
			}
		}
		for _, idx := range ctx.Catalog.AllIndexes() {
			if idx.OID != targetOID {
				continue
			}
			if colno != 0 {
				return NewStringDatum(catalog.BuildIndexDefColumn(idx, colno)), nil
			}
			return NewStringDatum(buildIndexDefString(idx)), nil
		}
		return NullDatum, nil

	case "pg_indexam_has_property":
		// pg_indexam_has_property(am_oid, propname) → bool — AM-wide
		// capability probe (postgres/src/backend/utils/adt/amutils.c).
		// M0134-0090.
		if len(x.Args) < 2 {
			return NullDatum, nil
		}
		amArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		propArg, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if amArg.IsNull() || propArg.IsNull() {
			return NullDatum, nil
		}
		amOID, ok := resolveOIDArg(amArg)
		if !ok {
			return NullDatum, nil
		}
		amName, ok := catalog.AccessMethodNameByOID(amOID)
		if !ok {
			return NullDatum, nil
		}
		return indexAMAMLevelProperty(amName, propArg.StringValue()).toDatum(), nil

	case "pg_index_has_property":
		// pg_index_has_property(index_oid, propname) → bool — index-wide
		// capability probe. M0134-0090.
		if len(x.Args) < 2 {
			return NullDatum, nil
		}
		idxArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		propArg, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if idxArg.IsNull() || propArg.IsNull() {
			return NullDatum, nil
		}
		idxOID, ok := resolveOIDArg(idxArg)
		if !ok {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		idx, found := im.LookupIndexByOID(idxOID)
		if !found {
			return NullDatum, nil
		}
		return indexAMIndexLevelProperty(indexAMNameForCapabilityLookup(idx), propArg.StringValue()).toDatum(), nil

	case "pg_index_column_has_property":
		// pg_index_column_has_property(index_oid, colno, propname) → bool —
		// per-column capability probe (indoption ASC/DESC/NULLS bits +
		// per-AM column flags). M0134-0090.
		if len(x.Args) < 3 {
			return NullDatum, nil
		}
		idxArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		colArg, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		propArg, err := evalExprSlot(x.Args[2], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if idxArg.IsNull() || colArg.IsNull() || propArg.IsNull() {
			return NullDatum, nil
		}
		idxOID, ok := resolveOIDArg(idxArg)
		if !ok {
			return NullDatum, nil
		}
		var attno int
		if colArg.Kind == KindInt {
			attno = int(colArg.Int)
		} else {
			v, err := strconv.ParseInt(strings.TrimSpace(colArg.StringValue()), 10, 32)
			if err != nil {
				return NullDatum, nil
			}
			attno = int(v)
		}
		// "Reject attno 0 immediately" — amutils.c's pg_index_column_has_property.
		if attno <= 0 {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		idx, found := im.LookupIndexByOID(idxOID)
		if !found {
			return NullDatum, nil
		}
		natts := len(idx.Columns) + len(idx.IncludeColumns)
		if attno > natts {
			return NullDatum, nil
		}
		amName := indexAMNameForCapabilityLookup(idx)
		c, ok := catalog.IndexAMCapabilityByName(amName)
		if !ok {
			return NullDatum, nil
		}
		return indexAMColumnLevelProperty(amName, c, idx, attno, propArg.StringValue()).toDatum(), nil

	case "pg_get_statisticsobjdef":
		// pg_get_statisticsobjdef(oid) → text — reconstructs CREATE STATISTICS DDL.
		// pg_dump's dumpStatisticsExt emits the result verbatim (plus a semicolon).
		// DU-002 slice 314.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if obj, ok := im.StatisticsByOID(targetOID); ok {
				def := im.BuildStatisticsObjDef(obj)
				if def == "" {
					return NullDatum, nil
				}
				return NewStringDatum(def), nil
			}
		}
		return NullDatum, nil

	case "pg_get_statisticsobjdef_columns":
		// pg_get_statisticsobjdef_columns(oid) → text — columns-only rendering used
		// by psql \d+'s "Statistics objects:" footer (describe.c appends its own
		// "FROM <regclass>"). ruleutils.c pg_get_statisticsobjdef_columns.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if obj, ok := im.StatisticsByOID(targetOID); ok {
				def := im.BuildStatisticsObjDefColumns(obj)
				if def == "" {
					return NullDatum, nil
				}
				return NewStringDatum(def), nil
			}
		}
		return NullDatum, nil

	case "pg_get_ruledef", "pg_get_ruledef_ext":
		// pg_get_ruledef(oid [, pretty bool]) → text — reconstructs the CREATE
		// RULE statement. pg_dump's dumpRule selects pg_get_ruledef(r.oid) and
		// emits the result verbatim with a trailing newline. Only unconditional
		// DO-NOTHING rules are modelled (catalog.RuleInfo); they live in their
		// owning table's Rules slice (no central rule registry), so scan all
		// tables for the OID. DU-002 slice 324.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for _, tbl := range im.AllTables() {
				if tbl.Virtual || tbl.OID == 0 {
					continue
				}
				for _, r := range tbl.Rules {
					if r.OID == 0 || r.OID != targetOID {
						continue
					}
					return NewStringDatum(buildRuleDefString(tbl, r)), nil
				}
			}
		}
		return NullDatum, nil

	case "pg_get_triggerdef":
		// pg_get_triggerdef(oid [, pretty bool]) → text — reconstructs the
		// CREATE TRIGGER statement. pg_dump's getTriggers selects
		// pg_get_triggerdef(t.oid, false) and emits the result verbatim with a
		// trailing semicolon. The trigger lives in its owning table's Triggers
		// slice (no central trigger registry), so scan all tables for the OID.
		// DU-002 slice 319.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for _, tbl := range im.AllTables() {
				if tbl.Virtual || tbl.OID == 0 {
					continue
				}
				for _, trig := range tbl.Triggers {
					if trig.OID == 0 || trig.OID != targetOID {
						continue
					}
					return NewStringDatum(buildTriggerDefString(tbl, trig)), nil
				}
			}
		}
		return NullDatum, nil

	case "pg_get_constraintdef":
		// pg_get_constraintdef(oid [, pretty bool]) → text
		// Reconstructs the constraint definition DDL. M0097-0023.
		// Handles UNIQUE/PRIMARY KEY/EXCLUDE constraints backed by indexes.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		// pretty: psql's \d+/\dD+ always call the two-arg form with pretty=true
		// (postgres/src/bin/psql/describe.c:2530); pg_dump uses the one-arg form
		// (implicit false) or the two-arg form with pretty=false explicitly
		// (pg_dump.c:7768, ruleutils.c:2152). Default false when the second arg
		// is absent, matching PG's 1-arg pg_get_constraintdef. DU-002-ah slice A.
		pretty := false
		if len(x.Args) >= 2 {
			prettyArg, err := evalExprSlot(x.Args[1], slot, ctx)
			if err == nil && !prettyArg.IsNull() {
				pretty = prettyArg.BoolValue()
			}
		}
		for _, idx := range ctx.Catalog.AllIndexes() {
			if idx.OID != targetOID || (!idx.IsConstraint && !idx.IsExclusion) {
				continue
			}
			return NewStringDatum(buildConstraintDefString(idx)), nil
		}
		// CHECK constraints are not index-backed; they live in the owning
		// table's NamedChecks. pg_dump's getTableConstraints query selects
		// `pg_get_constraintdef(c.oid)` for every contype='c' row, so without
		// this branch the CHECK def comes back NULL and the constraint is
		// silently dropped from the dumped CREATE TABLE. PG's deparser wraps the
		// predicate in an extra paren layer (CHECK ((expr))), and appends
		// NO INHERIT for a NO-INHERIT check; mirror that here. M0110-0001.
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for _, tbl := range im.AllTables() {
				for _, nc := range tbl.NamedChecks {
					if nc.OID == 0 || nc.OID != targetOID {
						continue
					}
					def := renderCheckPredicate(nc.Expr, pretty)
					if nc.NoInherit {
						def += " NO INHERIT"
					}
					// pg_get_constraintdef_worker's shared tail (ruleutils.c):
					// "Validated status is irrelevant when the constraint is
					// NOT ENFORCED" — checks conenforced FIRST and only falls
					// back to convalidated when the constraint IS enforced.
					// DU-002 slices 308, 430.
					if nc.NotEnforced {
						def += " NOT ENFORCED"
					} else if nc.NotValid {
						def += " NOT VALID"
					}
					return NewStringDatum(def), nil
				}
			}
			// FOREIGN KEY constraints (contype='f'). pg_dump's getConstraints
			// renders each FK via pg_get_constraintdef; with search_path='' the
			// deparser fully schema-qualifies the referenced relation
			// (`REFERENCES public.foo(id)`). DU-002 slice 51.
			for _, tbl := range im.AllTables() {
				for _, fk := range tbl.ForeignKeys {
					if fk.OID == 0 || fk.OID != targetOID {
						continue
					}
					return NewStringDatum(buildForeignKeyDefString(ctx, im, fk, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))), nil
				}
			}
			// Domain CHECK constraints (contype='c', keyed on contypid). pg_dump's
			// getDomainConstraints renders each via pg_get_constraintdef and
			// dumpDomain emits `CONSTRAINT <name> <def>`; the deparser fully
			// parenthesizes every sub-node, so a compound/function-call predicate
			// dumps `CHECK (((VALUE > 0) AND (VALUE < 100)))` /
			// `CHECK ((length(VALUE) > 0))` — reproduced by renderDomainCheckPredicate
			// (the domain twin of the table-CHECK renderer), which also upcases the
			// `VALUE` placeholder. A `CHECK (VALUE IN (...))` form is stored as a
			// pre-synthesized, byte-exact ScalarArrayOp deparse that defaultExprToSQL
			// cannot reproduce, so it keeps the legacy raw double-paren wrap.
			// DU-002 slice 96 (single comparison) / slice 363 (compound + function).
			for _, d := range im.AllDomains() {
				for _, ck := range d.Checks {
					if ck.OID == 0 || ck.OID != targetOID {
						continue
					}
					if len(ck.InValues) > 0 {
						return NewStringDatum("CHECK ((" + ck.Expr + "))"), nil
					}
					return NewStringDatum(renderDomainCheckPredicate(ck.Expr, pretty)), nil
				}
			}
		}
		return NullDatum, nil

	case "pg_get_partition_constraintdef":
		// pg_get_partition_constraintdef(relation oid) → text. M0134-0005ag.
		// ruleutils.c:2096 pg_get_partition_constraintdef → partcache.c:299
		// get_partition_qual_relid → partcache.c:337 generate_partition_qual →
		// partbounds.c:249 get_qual_from_partbound, dispatching by the PARENT's
		// strategy to get_qual_for_list / get_qual_for_range / get_qual_for_hash
		// (partbounds.c). Returns SQL NULL (never an error) when the relation
		// is not a partition, the OID is unknown, or the computed qual is
		// deliberately deferred (DEFAULT partition, multi-level partitioning,
		// expression-based partition keys, multi-column RANGE keys) — see
		// docs/design/0134-0005ag-partition-constraintdef.md. Design option 2
		// (local builder below) is used instead of touching the shared
		// defaultExprToSQL deparser, to avoid perturbing its CHECK-constraint
		// and index-predicate siblings.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		tbl, found := im.LookupTableByOID(targetOID)
		if !found || tbl.PartitionParentOID == 0 || len(tbl.PartitionBounds) == 0 {
			return NullDatum, nil
		}
		parent, found := im.LookupTableByOID(tbl.PartitionParentOID)
		if !found {
			return NullDatum, nil
		}
		// Multi-level partitioning: generate_partition_qual recurses and ANDs
		// every ancestor level's own qual (partcache.c:387-390). Not attempted
		// here — deferred (M0134-0005ag ledger row).
		if parent.PartitionParentOID != 0 {
			return NullDatum, nil
		}
		pb := tbl.PartitionBounds[0]
		if pb.IsDefault {
			// DEFAULT-partition constraint is the negation of every sibling
			// partition's own qual (get_qual_for_list :4099-4225 /
			// get_qual_for_range :4300-4368) — not derivable from this
			// partition's own bound alone. Deferred (M0134-0005ag ledger row).
			return NullDatum, nil
		}
		def := buildPartitionConstraintDef(parent, pb)
		if def == "" {
			return NullDatum, nil
		}
		return NewStringDatum(def), nil

	case "array_to_string":
		// array_to_string(anyarray, text [, text]) → text — joins array elements with separator.
		if len(x.Args) < 2 {
			return NullDatum, nil
		}
		arrDatum, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arrDatum.IsNull() {
			return NullDatum, nil
		}
		sepDatum, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return NullDatum, nil
		}
		sep := ""
		if !sepDatum.IsNull() {
			sep = sepDatum.StringValue()
		}
		nullStr := ""
		useNullStr := false
		if len(x.Args) >= 3 {
			nsDatum, err2 := evalExprSlot(x.Args[2], slot, ctx)
			if err2 == nil && !nsDatum.IsNull() {
				nullStr = nsDatum.StringValue()
				useNullStr = true
			}
		}
		elems := parseTextArray(arrDatum.StringValue())
		var parts []string
		for _, el := range elems {
			if el == "NULL" {
				if useNullStr {
					parts = append(parts, nullStr)
				}
			} else {
				parts = append(parts, el)
			}
		}
		return NewStringDatum(strings.Join(parts, sep)), nil

	case "pg_get_partkeydef":
		// pg_get_partkeydef(relation oid) → text — reconstructs PARTITION BY clause. M0097-0023.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		arg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || arg.IsNull() {
			return NullDatum, nil
		}
		var targetOID uint32
		if arg.Kind == KindInt {
			targetOID = uint32(arg.Int)
		} else {
			v, _ := strconv.ParseUint(strings.TrimSpace(arg.StringValue()), 10, 32)
			targetOID = uint32(v)
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		tbl, found := im.LookupTableByOID(targetOID)
		if !found || tbl.PartitionMethod == "" {
			return NullDatum, nil
		}
		numKeys := len(tbl.PartitionKey)
		if n := len(tbl.PartitionKeyExprs); n > numKeys {
			numKeys = n
		}
		var parts []string
		for i := 0; i < numKeys; i++ {
			var part string
			colName := ""
			if i < len(tbl.PartitionKey) {
				colName = tbl.PartitionKey[i]
			}
			var keyExpr parser.Expr
			if i < len(tbl.PartitionKeyExprs) {
				keyExpr = tbl.PartitionKeyExprs[i]
			}
			if keyExpr != nil {
				part = defaultExprToSQL(keyExpr)
				// pg_get_partkeydef_worker (ruleutils.c) wraps each EXPRESSION key
				// in `(%s)` unless it "looks like a function call"
				// (looks_like_function): a bare function call — and the
				// func_expr_common_subexpr family (COALESCE/NULLIF/GREATEST/LEAST,
				// SQL value functions, XML/JSON) — deparses without the extra parens,
				// while everything else (operators, casts, CASE, …) is wrapped. goopg
				// represents every one of those callable forms as *parser.FuncCall
				// (including the niladic value functions, which defaultExprToSQL emits
				// as bare uppercase keywords), so that single type check mirrors PG's
				// node-tag switch. Without this wrap a binary-op key
				// `((a + b) * c)` dumped as `RANGE (((a + b) * c))` — one paren short
				// of real pg_dump 18.3's `RANGE ((((a + b) * c)))` (verified
				// byte-identical). The opclass/collation suffixes below are appended
				// AFTER the wrap, matching PG's append order (DU-002 slice 300).
				if _, isFunc := keyExpr.(*parser.FuncCall); !isFunc {
					part = "(" + part + ")"
				}
			} else {
				part = colName
			}
			if i < len(tbl.PartitionKeyOpClasses) && tbl.PartitionKeyOpClasses[i] != "" {
				part += " " + tbl.PartitionKeyOpClasses[i]
			}
			if i < len(tbl.PartitionKeyCollations) {
				coll := tbl.PartitionKeyCollations[i]
				if coll != "" && strings.ToLower(coll) != "default" && strings.ToLower(coll) != "pg_default" {
					part += ` COLLATE "` + coll + `"`
				}
			}
			parts = append(parts, part)
		}
		return NewStringDatum(strings.ToUpper(tbl.PartitionMethod) + " (" + strings.Join(parts, ", ") + ")"), nil

	case "pg_get_expr":
		// pg_get_expr(tree pg_node_tree, relation oid [, pretty bool]) → text
		// Decompiles an internal expression tree. Goopg stores pre-formatted strings
		// in pg_node_tree columns (e.g. relpartbound), so pass them through directly.
		if len(x.Args) >= 1 {
			treeArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			// pg_get_expr(NULL, ...) → NULL (mirrors PG). A NULL node tree means
			// the object has no such expression (e.g. a domain or column with no
			// default); pg_dump distinguishes NULL from '' to decide whether to
			// emit a DEFAULT clause, so collapsing NULL to '' produced a spurious
			// empty `DEFAULT `. DU-002 slice 90.
			if treeArg.IsNull() {
				return NullDatum, nil
			}
			if treeArg.StringValue() != "" {
				return NewStringDatum(treeArg.StringValue()), nil
			}
		}
		return NewStringDatum(""), nil

	case "obj_description":
		// obj_description(object_oid [, catalog_name]) → text
		// Returns the description for a database object from pg_description.
		if len(x.Args) >= 1 {
			oidDatum, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			var objOID uint32
			switch oidDatum.Kind {
			case KindInt:
				objOID = uint32(oidDatum.Int)
			case KindString:
				n, _ := strconv.ParseUint(oidDatum.StringValue(), 10, 32)
				objOID = uint32(n)
			}
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok && objOID != 0 {
				// classoid 1259 = pg_class
				if desc, found := im.GetComment(1259, objOID, 0); found {
					return NewStringDatum(desc), nil
				}
			}
		}
		return NullDatum, nil

	case "col_description":
		// col_description(table_oid oid, column_number int4) → text
		// Returns the comment for a table column from pg_description, keyed by
		// (classoid=pg_class, objoid, objsubid=attnum). Mirrors PG's SQL body
		// verbatim (postgres/src/backend/catalog/system_functions.sql:322-327):
		// a bare SELECT with no matching pg_description row yields NULL, and the
		// function is declared STRICT (system_functions.sql:325) so any NULL arg
		// → NULL. psql's describe.c column-comments query calls
		// pg_catalog.col_description(a.attrelid, a.attnum)
		// (postgres/src/bin/psql/describe.c:1986); objsubid 0 returns the
		// table's own comment. M0134-0002 C15.
		if len(x.Args) >= 2 {
			oidDatum, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			var objOID uint32
			switch oidDatum.Kind {
			case KindInt:
				objOID = uint32(oidDatum.Int)
			case KindString:
				n, _ := strconv.ParseUint(oidDatum.StringValue(), 10, 32)
				objOID = uint32(n)
			}
			attnumDatum, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			// STRICT: a NULL table OID (objOID stays 0) or NULL/NOT-INT attnum
			// arg returns NULL without touching the catalog.
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok && objOID != 0 && attnumDatum.Kind == KindInt {
				// classoid 1259 = pg_class; objsubid = the column attnum.
				if desc, found := im.GetComment(1259, objOID, int32(attnumDatum.Int)); found {
					return NewStringDatum(desc), nil
				}
			}
		}
		return NullDatum, nil

	case "shobj_description":
		// shobj_description(object_oid, catalog_name) → text
		// Returns the description for a SHARED (cluster-wide) database object
		// from pg_shdescription, keyed by (classoid, objoid) rather than
		// obj_description's per-database pg_description (classoid, objoid,
		// objsubid). pg_dump's dumpDatabase (--create only) calls
		// `shobj_description(oid, 'pg_database')` to render `COMMENT ON
		// DATABASE`. goopg has no COMMENT ON DATABASE/ROLE/TABLESPACE writer
		// yet, so GetComment always misses and this returns NULL — matching a
		// freshly bootstrapped cluster with no shared comments recorded.
		// M0119-0004-ACLHEAP (datacl half).
		if len(x.Args) >= 2 {
			oidDatum, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			catArg, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			var objOID uint32
			switch oidDatum.Kind {
			case KindInt:
				objOID = uint32(oidDatum.Int)
			case KindString:
				n, _ := strconv.ParseUint(oidDatum.StringValue(), 10, 32)
				objOID = uint32(n)
			}
			var classoid uint32
			switch catArg.StringValue() {
			case "pg_database":
				classoid = catalog.PgDatabaseRelationOID // 1262
			case "pg_authid":
				classoid = 1260
			case "pg_tablespace":
				classoid = 1213
			}
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok && objOID != 0 && classoid != 0 {
				if desc, found := im.GetComment(classoid, objOID, 0); found {
					return NewStringDatum(desc), nil
				}
			}
		}
		return NullDatum, nil

	case "pg_relation_filenode":
		// pg_relation_filenode(relation regclass) → oid
		// Returns the filenode for a relation. For temporary tables returns NULL.
		if len(x.Args) >= 1 {
			relDatum, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			var relOID uint32
			switch relDatum.Kind {
			case KindInt:
				relOID = uint32(relDatum.Int)
			case KindString:
				n, _ := strconv.ParseUint(relDatum.StringValue(), 10, 32)
				relOID = uint32(n)
			}
			if relOID != 0 {
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if tbl, exists := im.LookupTableByOID(relOID); exists {
						if tbl.Temp {
							return NullDatum, nil
						}
						return Datum{Kind: KindInt, Int: int64(relOID)}, nil
					}
				}
			}
		}
		return NullDatum, nil

	case "pg_filenode_relation":
		// pg_filenode_relation(tablespace oid, filenode oid) → regclass
		// Returns the relation for a given filenode. Stub: returns NULL.
		return NullDatum, nil

	case "point":
		// point(x, y) → text "(x,y)" — minimal geometric point. M0097-0023.
		if len(x.Args) == 2 {
			av, aerr := evalExprSlot(x.Args[0], slot, ctx)
			bv, berr := evalExprSlot(x.Args[1], slot, ctx)
			if aerr != nil {
				return Datum{}, aerr
			}
			if berr != nil {
				return Datum{}, berr
			}
			ax, aok := datumAsFloat64(av)
			bx, bok := datumAsFloat64(bv)
			if aok && bok {
				return NewStringDatum(fmt.Sprintf("(%g,%g)", ax, bx)), nil
			}
		}
		return NullDatum, nil

	case "box":
		// box(point, point) → text "(max_x,max_y),(min_x,min_y)". M0097-0023.
		if len(x.Args) == 2 {
			av, aerr := evalExprSlot(x.Args[0], slot, ctx)
			bv, berr := evalExprSlot(x.Args[1], slot, ctx)
			if aerr != nil {
				return Datum{}, aerr
			}
			if berr != nil {
				return Datum{}, berr
			}
			as, aok := datumAsString(av)
			bs, bok := datumAsString(bv)
			if aok && bok {
				p1, ok1 := parsePointText(as)
				p2, ok2 := parsePointText(bs)
				if ok1 && ok2 {
					urx := math.Max(p1[0], p2[0])
					ury := math.Max(p1[1], p2[1])
					llx := math.Min(p1[0], p2[0])
					lly := math.Min(p1[1], p2[1])
					return NewStringDatum(fmt.Sprintf("(%g,%g),(%g,%g)", urx, ury, llx, lly)), nil
				}
			}
		}
		return NullDatum, nil

	case "center":
		// center(circle) → point "(x,y)" — circle_center in geo_ops.c.
		// M0134-0098.
		if len(x.Args) == 1 {
			av, aerr := evalExprSlot(x.Args[0], slot, ctx)
			if aerr != nil {
				return Datum{}, aerr
			}
			if av.IsNull() {
				return NullDatum, nil
			}
			as, aok := datumAsString(av)
			if aok {
				cx, cy, _, ok := parseCircleLiteral(as)
				if ok {
					return NewStringDatum(fmt.Sprintf("(%s,%s)", PGFloatOut(cx, 64), PGFloatOut(cy, 64))), nil
				}
			}
		}
		return NullDatum, nil

	case "radius":
		// radius(circle) → float8 — circle_radius in geo_ops.c. M0134-0098.
		if len(x.Args) == 1 {
			av, aerr := evalExprSlot(x.Args[0], slot, ctx)
			if aerr != nil {
				return Datum{}, aerr
			}
			if av.IsNull() {
				return NullDatum, nil
			}
			as, aok := datumAsString(av)
			if aok {
				_, _, r, ok := parseCircleLiteral(as)
				if ok {
					return floatTextDatum(PGFloatOut(r, 64)), nil
				}
			}
		}
		return NullDatum, nil

	case "diameter":
		// diameter(circle) → float8, 2*radius — circle_diameter in
		// geo_ops.c. M0134-0098.
		if len(x.Args) == 1 {
			av, aerr := evalExprSlot(x.Args[0], slot, ctx)
			if aerr != nil {
				return Datum{}, aerr
			}
			if av.IsNull() {
				return NullDatum, nil
			}
			as, aok := datumAsString(av)
			if aok {
				_, _, r, ok := parseCircleLiteral(as)
				if ok {
					return floatTextDatum(PGFloatOut(2*r, 64)), nil
				}
			}
		}
		return NullDatum, nil

	case "pg_sequence_parameters":
		// SRF returning sequence parameters — stub returns NULL.
		return NullDatum, nil

	// ── Partition metadata functions (M0097-0015) ─────────────────────────
	case "pg_partition_tree", "pg_partition_ancestors":
		// SRF — handled by the PgPartitionTree plan node; never reach this path
		// when used in FROM. As a scalar fallback (SELECT pg_partition_tree(...))
		// return NULL. M0097-0023.
		return NullDatum, nil
	case "pg_partition_root":
		// pg_partition_root(relid) → regclass: walk the PartitionParentOID chain
		// to find the root. Returns NULL for non-partitioned tables, views, matviews,
		// legacy-inheritance tables, and NULL input. M0097-0023.
		if len(x.Args) != 1 || ctx == nil || ctx.Catalog == nil {
			return NullDatum, nil
		}
		v, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || v.IsNull() {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		relName := partitionResolveRegclass(v, im)
		if relName == "" {
			return NullDatum, nil
		}
		// Try table first.
		if tbl, ok2 := im.LookupTable(parser.ObjectName{Name: relName}); ok2 {
			// Non-partitioned table (no PartitionKey) and not a partition child → NULL.
			if len(tbl.PartitionKey) == 0 && tbl.PartitionParentOID == 0 {
				return NullDatum, nil
			}
			// Walk up to root.
			cur := tbl
			visited := map[uint32]bool{}
			for cur.PartitionParentOID != 0 && !visited[cur.OID] {
				visited[cur.OID] = true
				parent, ok3 := im.LookupTableByOID(cur.PartitionParentOID)
				if !ok3 {
					break
				}
				cur = parent
			}
			return NewStringDatum(cur.Name), nil
		}
		// Try index.
		if idx, ok2 := im.LookupIndex(parser.ObjectName{Name: relName}); ok2 {
			if idx.PartitionParentOID == 0 && len(im.IndexPartitionChildren(idx.OID)) == 0 {
				return NullDatum, nil
			}
			cur := idx
			visited := map[uint32]bool{}
			for cur.PartitionParentOID != 0 && !visited[cur.OID] {
				visited[cur.OID] = true
				parent, ok3 := im.LookupIndexByOID(cur.PartitionParentOID)
				if !ok3 {
					break
				}
				cur = parent
			}
			return NewStringDatum(cur.Name), nil
		}
		return NullDatum, nil
	case "satisfies_hash_partition":
		// satisfies_hash_partition(tableoid, modulus, remainder, val...) → bool
		// Full implementation: Jenkins Bob hash + hash_combine64. M0097-0027.
		if len(x.Args) < 3 || ctx == nil || ctx.Catalog == nil {
			return NewBoolDatum(false), nil
		}
		modulusDatum, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		remainderDatum, err := evalExprSlot(x.Args[2], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		// NULL modulus or NULL remainder → false (PG behavior)
		if modulusDatum.IsNull() || remainderDatum.IsNull() {
			return NewBoolDatum(false), nil
		}
		modulus := int(modulusDatum.Int)
		remainder := int(remainderDatum.Int)
		if modulus <= 0 {
			return NullDatum, &ExecError{Code: "22023",
				Message: "modulus for hash partition must be an integer value greater than zero"}
		}
		if remainder < 0 {
			return NullDatum, &ExecError{Code: "22023",
				Message: "remainder for hash partition must be an integer value greater than or equal to zero"}
		}
		if remainder >= modulus {
			return NullDatum, &ExecError{Code: "22023",
				Message: "remainder for hash partition must be less than modulus"}
		}
		tableoidDatum, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		if tableoidDatum.IsNull() {
			return NewBoolDatum(false), nil
		}
		if tableoidDatum.Kind != KindInt {
			return NullDatum, &ExecError{Code: "XX000",
				Message: "could not open relation with OID 0"}
		}
		tableOID := uint32(tableoidDatum.Int)
		if tableOID == 0 {
			return NullDatum, &ExecError{Code: "XX000",
				Message: "could not open relation with OID 0"}
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NewBoolDatum(false), nil
		}
		tbl, found := im.LookupTableByOID(tableOID)
		if !found {
			return NullDatum, &ExecError{Code: "XX000",
				Message: fmt.Sprintf("could not open relation with OID %d", tableOID)}
		}
		if tbl.PartitionMethod != "HASH" || tbl.PartitionParentOID != 0 {
			return NullDatum, &ExecError{Code: "42809",
				Message: fmt.Sprintf("%q is not a hash partitioned table", tbl.Name)}
		}
		numKeys := len(tbl.PartitionKey)
		numArgs := len(x.Args) - 3
		if numArgs != numKeys {
			return NullDatum, &ExecError{Code: "22023",
				Message: fmt.Sprintf("number of partitioning columns (%d) does not match number of partition keys provided (%d)",
					numKeys, numArgs)}
		}
		// Type-check args against partition key column types (PG behavior: check even for NULLs).
		// Non-variadic: no quotes around type names; variadic: quoted type names.
		for i := 0; i < numKeys; i++ {
			colType := ""
			for _, col := range tbl.Columns {
				if strings.EqualFold(col.Name, tbl.PartitionKey[i]) {
					colType = strings.ToLower(col.Type.Name)
					break
				}
			}
			argTypeName := hashPartTypeName(x.Args[3+i])
			if argTypeName != "" {
				colPGName := pgFormatTypeName(colType)
				if !hashPartTypesCompatible(colType, argTypeName) {
					if x.Variadic {
						return NullDatum, &ExecError{Code: "22023",
							Message: fmt.Sprintf("column %d of the partition key has type %q, but supplied value is of type %q",
								i+1, colPGName, argTypeName)}
					}
					return NullDatum, &ExecError{Code: "22023",
						Message: fmt.Sprintf("column %d of the partition key has type %s, but supplied value is of type %s",
							i+1, colPGName, argTypeName)}
				}
			}
		}
		// Compute hash: for each non-NULL key value, call the operator class hash
		// function (or the built-in type default) and fold with hash_combine64.
		// Shared with INSERT-time HASH partition routing — M0134-0053.
		rowHash, herr := computeHashPartitionRowHash(tbl, im, ctx, x.Pos(), func(i int) (Datum, error) {
			return evalExprSlot(x.Args[3+i], slot, ctx)
		})
		if herr != nil {
			return NullDatum, herr
		}
		return NewBoolDatum(uint64(rowHash)%uint64(modulus) == uint64(remainder)), nil
	case "merge_action":
		// merge_action() → text — returns 'INSERT', 'UPDATE', or 'DELETE' within MERGE RETURNING.
		// Stub: return NULL (MERGE RETURNING is not yet executed). M0097-0016.
		return NullDatum, nil

	// ── Function introspection stubs (M0097-0012) ─────────────────────────
	case "pg_get_functiondef":
		// pg_get_functiondef(func_oid) → text — returns function DDL.
		if len(x.Args) == 1 {
			oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || oidArg.IsNull() {
				return NullDatum, nil
			}
			if ctx == nil || ctx.Catalog == nil {
				return NullDatum, nil
			}
			rs := ctx.Catalog.Routines()
			if rs == nil {
				return NullDatum, nil
			}
			var r *catalog.Routine
			if oidArg.Kind == KindInt {
				r = rs.LookupByOID(uint32(oidArg.Int))
			} else {
				// Fall back to name lookup for string arguments. The parser
				// unquotes a quoted name (the lookup is case-insensitive), so
				// `pg_get_functiondef('"MyFunc"')` resolves. A syntax error
				// yields an empty best-effort lookup (this path returns NULL on a
				// miss anyway). M0119-0006 (72nd slice).
				schema, nm, _ := splitRegQualifiedName(strings.TrimSpace(oidArg.StringValue()))
				candidates := rs.LookupByName(parser.ObjectName{Schema: schema, Name: nm})
				if len(candidates) > 0 {
					r = candidates[0]
				}
			}
			if r == nil {
				return NullDatum, nil
			}
			return NewStringDatum(buildFunctionDef(r)), nil
		}
	case "pg_get_function_arguments":
		// pg_get_function_arguments(oid) → text: comma-separated arg list
		if len(x.Args) == 1 && ctx != nil && ctx.Catalog != nil {
			oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && !oidArg.IsNull() && oidArg.Kind == KindInt {
				if r := routineOrAggregateArgs(ctx.Catalog, uint32(oidArg.Int)); r != nil {
					return NewStringDatum(buildFunctionArguments(r, true)), nil
				}
			}
		}
		return NewStringDatum(""), nil
	case "pg_get_function_identity_arguments":
		// pg_get_function_identity_arguments(oid) → text: the argument list
		// needed to identify the function for ALTER/DROP FUNCTION. Upstream
		// (ruleutils.c print_function_arguments) differs from
		// pg_get_function_arguments only by print_defaults=false — it omits
		// DEFAULT clauses. buildFunctionArguments takes printDefaults=false here,
		// so the identity form drops any ` DEFAULT <expr>` carried by the full form.
		if len(x.Args) == 1 && ctx != nil && ctx.Catalog != nil {
			oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && !oidArg.IsNull() && oidArg.Kind == KindInt {
				if r := routineOrAggregateArgs(ctx.Catalog, uint32(oidArg.Int)); r != nil {
					return NewStringDatum(buildFunctionArguments(r, false)), nil
				}
			}
		}
		return NewStringDatum(""), nil
	case "pg_get_function_sqlbody":
		// pg_get_function_sqlbody(oid) → text: the deparsed SQL-standard body
		// of a `LANGUAGE sql ... BEGIN ATOMIC` function (PG14+). Returns NULL
		// for any routine that is not a SQL-language function with an inlined
		// standard body (e.g. C, internal, plpgsql, or quoted-string SQL
		// bodies). goopg has no BEGIN ATOMIC support — no routine carries a
		// parsed prosqlbody — so this is NULL for every routine, which also
		// matches what pg_dump's dumpFunc expects for such functions.
		return NullDatum, nil
	case "pg_get_function_result":
		// pg_get_function_result(oid) → text: return type
		if len(x.Args) == 1 && ctx != nil && ctx.Catalog != nil {
			oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && !oidArg.IsNull() && oidArg.Kind == KindInt {
				if rs := ctx.Catalog.Routines(); rs != nil {
					if r := rs.LookupByOID(uint32(oidArg.Int)); r != nil && !r.IsProcedure {
						// RETURNS TABLE: PG's pg_get_function_result renders the
						// table columns (stored here as trailing OUT args) as
						// `TABLE(name type, ...)` rather than the equivalent
						// `SETOF record`. pg_dump uses this verbatim, so emitting
						// the TABLE form keeps the dump identical to upstream.
						if r.ReturnsTable {
							return NewStringDatum(buildTableResult(r)), nil
						}
						// Set-returning functions carry a SETOF prefix on their
						// result type, matching PG's pg_get_function_result
						// (ruleutils.c). pg_dump uses this verbatim for the
						// RETURNS clause, so dropping SETOF would silently
						// downgrade an SRF to a scalar function on dump.
						ret := canonicalTypeName(r.ReturnType.Name, r.ReturnTypeOID)
						if r.ReturnsSet {
							ret = "SETOF " + ret
						}
						return NewStringDatum(ret), nil
					}
				}
			}
		}
		return NewStringDatum(""), nil
	// pg_function_is_visible(oid) → bool: always true (no schema visibility model in v0).
	case "pg_function_is_visible", "pg_table_is_visible", "pg_type_is_visible",
		"pg_operator_is_visible", "pg_opfamily_is_visible", "pg_opclass_is_visible",
		"pg_conversion_is_visible", "pg_aggregate_is_visible", "pg_ts_parser_is_visible",
		"pg_ts_dict_is_visible", "pg_ts_template_is_visible", "pg_ts_config_is_visible",
		"pg_statistics_obj_is_visible":
		return NewBoolDatum(true), nil
	case "pg_proc":
		return NullDatum, nil
	case "regproc", "regprocedure", "regclass", "regtype", "regnamespace":
		// Type cast functions. For regclass specifically, resolve a
		// text relation name to the table's numeric OID via the
		// catalog (matches PG semantics post M0103-0008 rung 16,
		// after pg_class.oid was flipped from text-name to numeric).
		// Numeric inputs pass through. Other reg* casts remain
		// stubs returning the argument as-is.
		if len(x.Args) != 1 {
			return NullDatum, nil
		}
		v, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || v.IsNull() {
			return v, err
		}
		if name == "regclass" && v.Kind == KindString && ctx != nil && ctx.Catalog != nil {
			s := v.StringValue()
			// Shared SplitIdentifierString port (sibling of the CastExpr arm and
			// regIdentifierInput): a quoted relation name unquotes before the
			// lookup, a syntax error raises regclassin's 42602. M0119-0006
			// (72nd slice).
			schema, rel, nameOK := splitRegQualifiedName(s)
			if !nameOK {
				return NullDatum, &ExecError{Code: "42602", Pos: x.Pos(), Message: "invalid name syntax"}
			}
			// Scoped to the connection's own dbOid — see the CastExpr
			// regclass arm's identical fix (M0122-0007 4e follow-up 33).
			tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: schema, Name: rel}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
			if ok && tbl != nil {
				return NewIntDatum(int64(tbl.OID)), nil
			}
		}
		return v, nil

	// ── String functions (M0097-0005) ─────────────────────────────────────
	case "repeat":
		// repeat(text, int) → text
		if len(x.Args) == 2 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			n, err := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil || n.IsNull() {
				return NullDatum, nil
			}
			// PG text_repeat (varlena.c): count <= 0 returns empty string,
			// not an error.
			if n.Int < 0 {
				n.Int = 0
			}
			return NewStringDatum(strings.Repeat(s.StringValue(), int(n.Int))), nil
		}
	case "char_length", "character_length":
		// char_length(text) → int
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len([]rune(s.StringValue())))}, nil
		}
	case "length":
		// length(text) → int  (byte length for bytea, char length for text).
		// Only valid for text/varchar/char/bytea — integer/numeric/etc. must error
		// because PostgreSQL does not define length(integer). M0097-0063.
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			if s.Kind != KindString && s.Kind != KindBytes {
				typName := "unknown"
				switch s.Kind {
				case KindInt:
					typName = "integer"
				case KindNumeric:
					typName = "numeric"
				case KindBool:
					typName = "boolean"
				case KindTime:
					typName = "timestamp"
				}
				return Datum{}, &ExecError{Code: "42883",
					Message: fmt.Sprintf("function length(%s) does not exist", typName),
					Hint:    "No function matches the given name and argument types. You might need to add explicit type casts.",
					Pos:     x.Pos()}
			}
			if s.Kind == KindBytes {
				// byteaoctetlen: bytes, not characters. Rune-counting a bytea
				// happened to agree only because each invalid UTF-8 byte
				// decodes to one RuneError. M0125-0021.
				return Datum{Kind: KindInt, Int: int64(len(s.BytesValue()))}, nil
			}
			return Datum{Kind: KindInt, Int: int64(len([]rune(s.StringValue())))}, nil
		}
	case "octet_length":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			if s.Kind == KindBytes {
				return Datum{Kind: KindInt, Int: int64(len(s.BytesValue()))}, nil
			}
			if s.Kind != KindString {
				return Datum{}, &ExecError{Code: "42883",
					Message: fmt.Sprintf("function octet_length(%s) does not exist", stringFuncArgTypeName(s.Kind)),
					Hint:    "No function matches the given name and argument types. You might need to add explicit type casts.",
					Pos:     x.Pos()}
			}
			// bpchar: PG's bpcharoctetlen returns the raw datum size, and PG
			// stores bpchar blank-padded — octet_length('ab'::char(10)) is 10,
			// not 2. goopg keeps bpchar trimmed in the datum (M0103-0007), so
			// pad to the declared width first. M0119-0006 (65th slice).
			if tm := declaredBpcharTypmod(x.Args[0]); tm > 0 {
				t := catalog.Type{Name: "char", Args: []int64{tm}}
				return Datum{Kind: KindInt, Int: int64(len(catalog.PadBpchar(t, s.StringValue())))}, nil
			}
			return Datum{Kind: KindInt, Int: int64(len(s.StringValue()))}, nil
		}
	case "bit_length":
		// PG 18 defines bit_length as a SQL function, octet_length($1) * 8, for
		// bytea (oid 1810) and text (oid 1811). There is NO bit_length(bpchar):
		// it resolves through the implicit bpchar→text cast, which trims
		// trailing spaces, so bit_length('ab'::char(10)) is 16 (2 bytes × 8),
		// not 80. goopg's bpchar datum is already trimmed (M0103-0007), so the
		// plain byte length below is the trimmed length. M0119-0006 (65th slice).
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			if s.Kind == KindBytes {
				return Datum{Kind: KindInt, Int: int64(8 * len(s.BytesValue()))}, nil
			}
			if s.Kind != KindString {
				return Datum{}, &ExecError{Code: "42883",
					Message: fmt.Sprintf("function bit_length(%s) does not exist", stringFuncArgTypeName(s.Kind)),
					Hint:    "No function matches the given name and argument types. You might need to add explicit type casts.",
					Pos:     x.Pos()}
			}
			return Datum{Kind: KindInt, Int: int64(8 * len(s.StringValue()))}, nil
		}
	case "upper":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ToUpper(s.StringValue())), nil
		}
	case "lower":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ToLower(s.StringValue())), nil
		}
	case "initcap":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(initCap(s.StringValue())), nil
		}
	case "btrim":
		// btrim(text [, chars]) — trim chars from both ends
		if len(x.Args) >= 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			// PG: postgres/src/backend/utils/adt/oracle_compat.c:638-703
			// (dobyteatrim / byteatrim) — bytea input trims by byte-set
			// membership, not rune semantics, and must stay tagged KindBytes.
			if s.Kind == KindBytes {
				cutset := []byte(" ")
				if len(x.Args) >= 2 {
					c, err := evalExprSlot(x.Args[1], slot, ctx)
					if err == nil && !c.IsNull() {
						cutset = c.BytesValue()
					}
				}
				return NewBytesDatum(bytes.Trim(s.BytesValue(), string(cutset))), nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExprSlot(x.Args[1], slot, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.Trim(s.StringValue(), cutset)), nil
		}
	case "ltrim":
		if len(x.Args) >= 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			// PG: postgres/src/backend/utils/adt/oracle_compat.c:638-703
			// (dobyteatrim / bytealtrim) — same byte-set semantics as btrim.
			if s.Kind == KindBytes {
				cutset := []byte(" ")
				if len(x.Args) >= 2 {
					c, err := evalExprSlot(x.Args[1], slot, ctx)
					if err == nil && !c.IsNull() {
						cutset = c.BytesValue()
					}
				}
				return NewBytesDatum(bytes.TrimLeft(s.BytesValue(), string(cutset))), nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExprSlot(x.Args[1], slot, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.TrimLeft(s.StringValue(), cutset)), nil
		}
	case "rtrim":
		if len(x.Args) >= 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			// PG: postgres/src/backend/utils/adt/oracle_compat.c:638-703
			// (dobyteatrim / byteartrim) — same byte-set semantics as btrim.
			if s.Kind == KindBytes {
				cutset := []byte(" ")
				if len(x.Args) >= 2 {
					c, err := evalExprSlot(x.Args[1], slot, ctx)
					if err == nil && !c.IsNull() {
						cutset = c.BytesValue()
					}
				}
				return NewBytesDatum(bytes.TrimRight(s.BytesValue(), string(cutset))), nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExprSlot(x.Args[1], slot, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.TrimRight(s.StringValue(), cutset)), nil
		}
	case "lpad":
		// lpad(text, int [, fill_text])
		if len(x.Args) >= 2 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			n, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil || err2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			fill := " "
			if len(x.Args) >= 3 {
				f, ferr := evalExprSlot(x.Args[2], slot, ctx)
				if ferr == nil && !f.IsNull() {
					fill = f.StringValue()
				}
			}
			return NewStringDatum(padLeft(s.StringValue(), int(n.Int), fill)), nil
		}
	case "rpad":
		if len(x.Args) >= 2 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			n, err2 := evalExprSlot(x.Args[1], slot, ctx)
			if err != nil || err2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			fill := " "
			if len(x.Args) >= 3 {
				f, ferr := evalExprSlot(x.Args[2], slot, ctx)
				if ferr == nil && !f.IsNull() {
					fill = f.StringValue()
				}
			}
			return NewStringDatum(padRight(s.StringValue(), int(n.Int), fill)), nil
		}
	case "replace":
		// replace(text, from, to)
		if len(x.Args) == 3 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			f, e2 := evalExprSlot(x.Args[1], slot, ctx)
			t, e3 := evalExprSlot(x.Args[2], slot, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ReplaceAll(s.StringValue(), f.StringValue(), t.StringValue())), nil
		}
	case "translate":
		// translate(text, from_chars, to_chars)
		if len(x.Args) == 3 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			f, e2 := evalExprSlot(x.Args[1], slot, ctx)
			t, e3 := evalExprSlot(x.Args[2], slot, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(translateStr(s.StringValue(), f.StringValue(), t.StringValue())), nil
		}
	case "strpos", "position":
		// strpos(string, substring) → int; position(substring in string) via FuncCall rewrite
		if len(x.Args) == 2 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			sub, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || sub.IsNull() {
				return NullDatum, nil
			}
			idx := strings.Index(s.StringValue(), sub.StringValue())
			if idx < 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			// Convert byte offset to rune position + 1 (PostgreSQL is 1-indexed)
			runes := []rune(s.StringValue()[:idx])
			return Datum{Kind: KindInt, Int: int64(len(runes) + 1)}, nil
		}
	case "split_part":
		// split_part(text, delimiter, field) — mirrors PG's split_part()
		// (postgres/src/backend/utils/adt/varlena.c:4621-4750).
		if len(x.Args) == 3 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			d, e2 := evalExprSlot(x.Args[1], slot, ctx)
			n, e3 := evalExprSlot(x.Args[2], slot, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() || d.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			fldnum := int(n.Int)
			// field number is 1 based
			if fldnum == 0 {
				return Datum{}, &ExecError{Code: "22023",
					Message: "field position must not be zero"}
			}
			str := s.StringValue()
			sep := d.StringValue()
			// return empty string for empty input string
			if len(str) < 1 {
				return NewStringDatum(""), nil
			}
			// handle empty field separator: if first or last field, return
			// input string, else empty string.
			if len(sep) < 1 {
				if fldnum == 1 || fldnum == -1 {
					return NewStringDatum(str), nil
				}
				return NewStringDatum(""), nil
			}
			parts := strings.Split(str, sep)
			if fldnum < 0 {
				// convert negative field number to positive by counting from
				// the end (total field count).
				fldnum += len(parts) + 1
				if fldnum <= 0 {
					return NewStringDatum(""), nil
				}
			}
			if fldnum > len(parts) {
				return NewStringDatum(""), nil
			}
			return NewStringDatum(parts[fldnum-1]), nil
		}
	case "concat":
		// concat(any, ...) → text — NULL inputs are treated as empty string.
		// concat(VARIADIC NULL::anyarray) → NULL (not empty string). M0097-0063.
		// Expand VARIADIC array arguments into individual string values.
		if x.Variadic && len(x.Args) == 1 {
			// concat(VARIADIC arr) — single array arg with VARIADIC flag.
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			elems := parseTextArray(v.StringValue())
			var buf strings.Builder
			for _, e := range elems {
				buf.WriteString(e)
			}
			return NewStringDatum(buf.String()), nil
		}
		var buf strings.Builder
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			buf.WriteString(v.Format())
		}
		return NewStringDatum(buf.String()), nil
	case "concat_ws":
		// concat_ws(sep, any, ...) → text.
		// concat_ws(sep, VARIADIC arr) — expand array elements. M0097-0063.
		if len(x.Args) >= 1 {
			sepArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || sepArg.IsNull() {
				return NullDatum, nil
			}
			sep := sepArg.StringValue()

			// Check for VARIADIC last argument.
			if x.Variadic && len(x.Args) == 2 {
				arrVal, verr := evalExprSlot(x.Args[1], slot, ctx)
				if verr != nil || arrVal.IsNull() {
					return NullDatum, nil
				}
				// Must be an array (string starting with '{').
				sv := arrVal.StringValue()
				if len(sv) == 0 || sv[0] != '{' {
					return Datum{}, &ExecError{Code: "42809",
						Message: "VARIADIC argument must be an array",
						Pos:     x.Pos()}
				}
				elems := parseTextArray(sv)
				var parts []string
				for _, e := range elems {
					parts = append(parts, e)
				}
				return NewStringDatum(strings.Join(parts, sep)), nil
			}

			var parts []string
			for _, arg := range x.Args[1:] {
				v, verr := evalExprSlot(arg, slot, ctx)
				if verr != nil || v.IsNull() {
					continue
				}
				parts = append(parts, v.Format())
			}
			return NewStringDatum(strings.Join(parts, sep)), nil
		}
	case "left":
		// left(text, n) → text
		if len(x.Args) == 2 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			n, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			cnt := int(n.Int)
			if cnt < 0 {
				cnt = max(0, len(runes)+cnt)
			} else if cnt > len(runes) {
				cnt = len(runes)
			}
			return NewStringDatum(string(runes[:cnt])), nil
		}
	case "right":
		if len(x.Args) == 2 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			n, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			cnt := int(n.Int)
			if cnt < 0 {
				start := -cnt
				if start >= len(runes) {
					return NewStringDatum(""), nil
				}
				return NewStringDatum(string(runes[start:])), nil
			}
			start := len(runes) - cnt
			if start < 0 {
				start = 0
			}
			return NewStringDatum(string(runes[start:])), nil
		}
	case "starts_with":
		// starts_with(text, prefix) → bool (varlena.c text_starts_with,
		// pg_proc oid 3696). Registered in the catalog but had no evaluator
		// — every call fell through to the "function does not exist" 42883
		// default below. M0134-0111.
		if len(x.Args) == 2 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			p, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || p.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(strings.HasPrefix(s.StringValue(), p.StringValue())), nil
		}
	case "reverse":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			if s.Kind == KindBytes {
				// bytea_reverse (postgres/src/backend/utils/adt/varlena.c:3458-3474)
				// is a plain byte-for-byte reversal, no codepoint awareness.
				src := s.BytesValue()
				out := make([]byte, len(src))
				for i, b := range src {
					out[len(src)-1-i] = b
				}
				return NewBytesDatum(out), nil
			}
			runes := []rune(s.StringValue())
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return NewStringDatum(string(runes)), nil
		}
	case "ascii":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			if len(runes) == 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			return Datum{Kind: KindInt, Int: int64(runes[0])}, nil
		}
	case "chr":
		if len(x.Args) == 1 {
			n, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || n.IsNull() {
				return NullDatum, nil
			}
			// PG: postgres/src/backend/utils/adt/oracle_compat.c:1030-1047 (chr)
			// — reject non-positive/zero codepoints with PG's exact SQLSTATEs.
			if n.Int < 0 {
				return Datum{}, &ExecError{Code: "22023", Message: "character number must be positive"}
			}
			if n.Int == 0 {
				return Datum{}, &ExecError{Code: "54000", Message: "null character not permitted"}
			}
			return NewStringDatum(string(rune(n.Int))), nil
		}
	case "quote_literal":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NewStringDatum("NULL"), nil
			}
			return NewStringDatum(pgQuoteLiteral(s.StringValue())), nil
		}
	case "quote_ident":
		if len(x.Args) == 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			// PG's quote_ident only adds double quotes when the identifier
			// would not survive a re-parse unquoted (uppercase, special chars,
			// leading digit, empty); a plain lowercase identifier is returned
			// bare. pgQuoteIdent applies that rule — unconditional quoting here
			// over-quoted e.g. role names in pg_dump's getPolicies TO-clause
			// resolution. DU-002 slice 330.
			return NewStringDatum(pgQuoteIdent(s.StringValue())), nil
		}
	case "regexp_replace":
		// regexp_replace(text, pattern, replacement [, flags | start [, N [, flags]]]).
		// M0097-0011 (3/4-arg forms) + M0134-0070 Round F (5/6-arg extended
		// forms, pg_proc.dat:3755-3768 oids 6251/6252/6253:
		//   6251 (6-arg): string, pattern, replacement, start, N, flags
		//   6252 (5-arg): string, pattern, replacement, start, N       (no flags)
		//   6253 (4-arg): string, pattern, replacement, start          (no N, no flags)
		// oid 6253 collides in arity with the pre-existing 4-arg flags form
		// (oid 2285: string, pattern, replacement, flags) — goopg's untyped
		// AST can't disambiguate by arity, so branch on the arg-3 Datum kind
		// (string Datum -> flags form; int Datum -> start form), which is
		// exactly what upstream's own overload resolution keys off for an
		// unquoted numeric literal vs a quoted string literal (see
		// strings.sql "erroneous invocation of non-extended form" case:
		// regexp_replace(..., 'X', '1') stays on the 4-arg flags form and
		// errors "invalid regular expression option").
		// See postgres/src/backend/utils/adt/regexp.c:700-741
		// (textregexreplace_extended) for the start/N validation rules and
		// postgres/src/backend/utils/adt/varlena.c:4457-4618
		// (replace_text_regexp) for the match-and-replace loop mirrored below.
		if len(x.Args) >= 3 {
			s, e1 := evalExprSlot(x.Args[0], slot, ctx)
			pat, e2 := evalExprSlot(x.Args[1], slot, ctx)
			repl, e3 := evalExprSlot(x.Args[2], slot, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() || pat.IsNull() {
				return NullDatum, nil
			}
			flagsStr := ""
			start := int64(1)
			n := int64(1)
			haveStartN := false
			switch len(x.Args) {
			case 4:
				v, e4 := evalExprSlot(x.Args[3], slot, ctx)
				if e4 != nil {
					return NullDatum, e4
				}
				if !v.IsNull() {
					if v.Kind == KindInt {
						// oid 6253: (string, pattern, replacement, start) — no N, no flags.
						haveStartN = true
						start = v.Int
					} else {
						flagsStr = v.StringValue()
						// M0134-0070: textregexreplace (4-arg text overload, oid
						// 2285) adds a HINT when the 4th arg is non-empty and its
						// first byte is '0'..'9' — the user probably meant the
						// start-parameter form (regexp.c:673-684). Print the WHOLE
						// opt via pg_mblen_range, not the shared helper's %q-of-
						// first-rune, so "1z" doesn't truncate to "1".
						if len(flagsStr) > 0 && flagsStr[0] >= '0' && flagsStr[0] <= '9' {
							return NullDatum, &ExecError{Code: "22023",
								Message: "invalid regular expression option: \"" + flagsStr + "\"",
								Hint:    "If you meant to use regexp_replace() with a start parameter, cast the fourth argument to integer explicitly."}
						}
					}
				}
			case 5:
				// oid 6252: (string, pattern, replacement, start, N) — no flags.
				v4, e4 := evalExprSlot(x.Args[3], slot, ctx)
				if e4 != nil {
					return NullDatum, e4
				}
				v5, e5 := evalExprSlot(x.Args[4], slot, ctx)
				if e5 != nil {
					return NullDatum, e5
				}
				if v4.IsNull() || v5.IsNull() {
					return NullDatum, nil
				}
				haveStartN = true
				start = v4.Int
				n = v5.Int
			case 6:
				// oid 6251: (string, pattern, replacement, start, N, flags).
				v4, e4 := evalExprSlot(x.Args[3], slot, ctx)
				if e4 != nil {
					return NullDatum, e4
				}
				v5, e5 := evalExprSlot(x.Args[4], slot, ctx)
				if e5 != nil {
					return NullDatum, e5
				}
				v6, e6 := evalExprSlot(x.Args[5], slot, ctx)
				if e6 != nil {
					return NullDatum, e6
				}
				if v4.IsNull() || v5.IsNull() {
					return NullDatum, nil
				}
				haveStartN = true
				start = v4.Int
				n = v5.Int
				if !v6.IsNull() {
					flagsStr = v6.StringValue()
				}
			}
			if haveStartN {
				if start <= 0 {
					return NullDatum, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "start", start)}
				}
				if n < 0 {
					return NullDatum, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "n", n)}
				}
			}
			goFlags := ""
			replaceAll := false
			if flagsStr != "" {
				var ferr error
				goFlags, replaceAll, ferr = pgRegexFlagsToGoModifiers(flagsStr)
				if ferr != nil {
					return NullDatum, ferr
				}
			}
			re, err := regexpCompilePattern(pat.StringValue(), flagsStr, goFlags)
			if err != nil {
				return NewStringDatum(s.StringValue()), nil // invalid pattern: return input
			}
			replacement := repl.StringValue()
			// Convert PostgreSQL's replacement-string escape grammar
			// (\1-\9, \&, \\, other-\c passthrough) to a Go regexp expansion
			// template. M0134-0070 Round G.
			replacement = pgRegexpReplacementTemplate(replacement)

			if !haveStartN {
				var result string
				if replaceAll {
					result = re.ReplaceAllString(s.StringValue(), replacement)
				} else {
					// Replace only first occurrence.
					found := false
					result = re.ReplaceAllStringFunc(s.StringValue(), func(m string) string {
						if found {
							return m
						}
						found = true
						return re.ReplaceAllString(m, replacement)
					})
				}
				return NewStringDatum(result), nil
			}

			// start/N-scoped replace: mirror varlena.c:replace_text_regexp's
			// successive-match loop. search_start is the 0-based char offset
			// (start-1); N==0 means replace every match at/after search_start
			// (like the 'g' flag, scoped to start); N>0 replaces only the
			// Nth match found and leaves all others (before and after)
			// unchanged.
			runes := []rune(s.StringValue())
			searchStart := int(start) - 1
			if searchStart > len(runes) {
				searchStart = len(runes)
			}
			prefix := string(runes[:searchStart])
			window := string(runes[searchStart:])
			matches := re.FindAllStringSubmatchIndex(window, -1)
			var b strings.Builder
			b.WriteString(prefix)
			last := 0
			nmatches := int64(0)
			for _, loc := range matches {
				nmatches++
				if n > 0 && nmatches != n {
					continue // not the target match: leave it unchanged
				}
				mstart, mend := loc[0], loc[1]
				b.WriteString(window[last:mstart])
				b.Write(re.ExpandString(nil, replacement, window, loc))
				last = mend
				if n > 0 {
					break // only one target match; stop after replacing it
				}
			}
			b.WriteString(window[last:])
			return NewStringDatum(b.String()), nil
		}
	case "format":
		// format(fmt, args...) — PostgreSQL format() with positional args, width, flags.
		// %[position][flags][width]type where type = s | I | L | %. M0097-0003 / M0097-0063.
		// format(fmt, VARIADIC arr) expands the array into individual arguments. M0097-0063.
		if len(x.Args) >= 1 {
			f, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			if f.IsNull() {
				return NullDatum, nil
			}
			fmtStr := f.StringValue()

			// Evaluate remaining args, expanding VARIADIC array if present.
			// x.Variadic is true when any argument was marked with VARIADIC keyword.
			var args []Datum
			nonFmtArgs := x.Args[1:]
			if x.Variadic && len(nonFmtArgs) == 1 {
				// format(fmt, VARIADIC arr) — single variadic array.
				v, e := evalExprSlot(nonFmtArgs[0], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if !v.IsNull() {
					sv := v.StringValue()
					if len(sv) == 0 || sv[0] != '{' {
						return Datum{}, &ExecError{Code: "42809",
							Message: "VARIADIC argument must be an array",
							Pos:     x.Pos()}
					}
					elems := parseTextArray(sv)
					for _, e := range elems {
						args = append(args, NewStringDatum(e))
					}
				}
				// If v is NULL, args stays empty → format string must not use any args.
			} else {
				for _, a := range nonFmtArgs {
					v, e := evalExprSlot(a, slot, ctx)
					if e != nil {
						return Datum{}, e
					}
					args = append(args, v)
				}
			}

			result, ferr := applyPgFormatFull(fmtStr, args)
			if ferr != nil {
				return Datum{}, ferr
			}
			return NewStringDatum(result), nil
		}

	case "json_extract_path", "json_extract_path_text", "jsonb_extract_path", "jsonb_extract_path_text":
		// json{b}_extract_path[_text](from_json, VARIADIC path_elems text[]).
		// Oracle: postgres/src/backend/utils/adt/jsonfuncs.c get_path_all /
		// get_jsonb_path_all — both walk the shared per-step navigation
		// jsonPathStep also used by evalJSONArrow (-> / ->>), just driven by a
		// text[] path array instead of a single operator RHS. goopg carries
		// json and jsonb identically as KindString text, so the json/jsonb
		// pairs share one implementation (M0134-0037).
		if len(x.Args) >= 1 {
			asText := name == "json_extract_path_text" || name == "jsonb_extract_path_text"
			jv, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			if jv.IsNull() {
				return NullDatum, nil
			}
			ls, ok := datumAsString(jv)
			if !ok {
				return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(),
					Message: fmt.Sprintf("function %s requires a json argument", x.Name)}
			}

			// Evaluate the path elements, expanding a VARIADIC text[] argument
			// if present — mirrors the format()/concat() VARIADIC handling
			// above. A NULL path element (or a NULL path array) yields NULL,
			// matching get_path_all's array_contains_nulls check.
			var path []string
			pathArgs := x.Args[1:]
			if x.Variadic && len(pathArgs) == 1 {
				v, e := evalExprSlot(pathArgs[0], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if v.IsNull() {
					return NullDatum, nil
				}
				sv := v.StringValue()
				if len(sv) == 0 || sv[0] != '{' {
					return Datum{}, &ExecError{Code: "42809",
						Message: "VARIADIC argument must be an array", Pos: x.Pos()}
				}
				path = parseTextArray(sv)
			} else {
				for _, a := range pathArgs {
					v, e := evalExprSlot(a, slot, ctx)
					if e != nil {
						return Datum{}, e
					}
					if v.IsNull() {
						return NullDatum, nil
					}
					path = append(path, v.StringValue())
				}
			}

			dec := json.NewDecoder(strings.NewReader(ls))
			dec.UseNumber()
			var doc any
			if err := dec.Decode(&doc); err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
					Message: "invalid input syntax for type json"}
			}

			elem, found := doc, true
			for _, key := range path {
				elem, found = jsonPathStep(elem, key)
				if !found {
					return NullDatum, nil
				}
			}
			if asText {
				return jsonElemAsTextDatum(elem), nil
			}
			return jsonElemAsJSONDatum(elem), nil
		}

	// ── Mathematical functions (M0097-0005) ───────────────────────────────
	case "abs":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				n := v.Int
				if n == math.MinInt64 {
					// abs(MinInt64) overflows: MinInt64 = -2^63, abs = 2^63 which can't fit int64.
					return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
				}
				if n < 0 {
					n = -n
				}
				return Datum{Kind: KindInt, Int: n}, nil
			}
			// Numeric abs
			if v.Kind == KindNumeric || v.Kind == KindString {
				sv := v.Format()
				if strings.HasPrefix(sv, "-") {
					sv = sv[1:]
				}
				m, sc, perr := parseNumeric(sv)
				if perr == nil {
					return newNumeric(m, int(sc)), nil
				}
			}
			return v, nil
		}
	case "ceil", "ceiling":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(math.Ceil(f))}, nil
		}
	case "floor":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(math.Floor(f))}, nil
		}
	case "round":
		if len(x.Args) >= 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			// PG: round(±Infinity) = ±Infinity for both float8 and numeric
			// (dround / numeric_round). The julian/epoch infinity results of
			// EXTRACT/date_part are carried as the strings "Infinity"/"-Infinity"
			// (no numeric-Infinity wire type in goopg) — pass them through instead
			// of ParseFloat/FormatFloat, which would mangle them to "+Inf"/"-Inf".
			// The regress extract/date_part blocks round() the julian column
			// (timestamptz.sql) and expect ±Infinity on the infinity rows
			// (timestamptz.out:1341-1342, 1187-1188). M0134-0076.
			if s := v.Format(); s == "Infinity" || s == "-Infinity" {
				return v, nil
			}
			scale := int64(0)
			if len(x.Args) >= 2 {
				sc, serr := evalExprSlot(x.Args[1], slot, ctx)
				if serr == nil && !sc.IsNull() {
					scale = sc.Int
				}
			}
			if v.Kind == KindInt && scale == 0 {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			factor := math.Pow(10, float64(scale))
			rounded := math.Round(f*factor) / factor
			sv := strconv.FormatFloat(rounded, 'f', int(scale), 64)
			m, sc2, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc2)), nil
		}
	case "trunc":
		if len(x.Args) >= 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			scale := int64(0)
			if len(x.Args) >= 2 {
				sc, serr := evalExprSlot(x.Args[1], slot, ctx)
				if serr == nil && !sc.IsNull() {
					scale = sc.Int
				}
			}
			factor := math.Pow(10, float64(scale))
			truncated := math.Trunc(f*factor) / factor
			return Datum{Kind: KindInt, Int: int64(truncated)}, nil
		}
	case "sign":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				if v.Int > 0 {
					return Datum{Kind: KindInt, Int: 1}, nil
				} else if v.Int < 0 {
					return Datum{Kind: KindInt, Int: -1}, nil
				}
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			if f > 0 {
				return Datum{Kind: KindInt, Int: 1}, nil
			} else if f < 0 {
				return Datum{Kind: KindInt, Int: -1}, nil
			}
			return Datum{Kind: KindInt, Int: 0}, nil
		}
	case "trim_scale", "min_scale":
		// trim_scale(numeric) reduces the value's display scale to the
		// minimum needed to represent it exactly (drop trailing zero
		// decimal digits); min_scale(numeric) reports that minimum scale
		// as an int without altering the value. Mirrors PG's
		// numeric_trim_scale / numeric_min_scale, both built on
		// get_min_scale (postgres/src/backend/utils/adt/numeric.c:4323,
		// :4253): peel trailing zero digits off the mantissa one decimal
		// place at a time while scale > 0.
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			m := numericMant(v)
			scale := int(v.Scale)
			ten := big.NewInt(10)
			for scale > 0 {
				q, r := new(big.Int).QuoRem(m, ten, new(big.Int))
				if r.Sign() != 0 {
					break
				}
				m = q
				scale--
			}
			if name == "min_scale" {
				return Datum{Kind: KindInt, Int: int64(scale)}, nil
			}
			return newNumeric(m, scale), nil
		}
	case "sqrt":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			result := math.Sqrt(f)
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "power", "pow":
		if len(x.Args) == 2 {
			base, e1 := evalExprSlot(x.Args[0], slot, ctx)
			exp, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || base.IsNull() || exp.IsNull() {
				return NullDatum, nil
			}
			b, _ := strconv.ParseFloat(base.Format(), 64)
			e, _ := strconv.ParseFloat(exp.Format(), 64)
			result := math.Pow(b, e)
			if result == math.Trunc(result) {
				return Datum{Kind: KindInt, Int: int64(result)}, nil
			}
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "exp":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, _ := strconv.ParseFloat(v.Format(), 64)
			result := math.Exp(f)
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "ln", "log":
		if len(x.Args) >= 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, _ := strconv.ParseFloat(v.Format(), 64)
			var result float64
			if name == "ln" || len(x.Args) == 1 {
				result = math.Log(f)
			} else {
				base, _ := evalExprSlot(x.Args[1], slot, ctx)
				b, _ := strconv.ParseFloat(base.Format(), 64)
				result = math.Log(f) / math.Log(b)
			}
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "gcd":
		// gcd(a, b) — greatest common divisor, non-negative. M0097-0003.
		// Uses uint64 absolute values so MinInt64 doesn't overflow on negation.
		if len(x.Args) == 2 {
			av, e1 := evalExprSlot(x.Args[0], slot, ctx)
			bv, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || av.IsNull() || bv.IsNull() {
				return NullDatum, nil
			}
			absA := int64ToAbsUint64(av.Int)
			absB := int64ToAbsUint64(bv.Int)
			ua, ub := absA, absB
			for ub != 0 {
				ua, ub = ub, ua%ub
			}
			const maxInt64 = uint64(math.MaxInt64)
			isInt4Range := av.Int >= -2147483648 && av.Int <= 2147483647 &&
				bv.Int >= -2147483648 && bv.Int <= 2147483647
			if isInt4Range {
				if ua > 2147483647 {
					return Datum{}, &ExecError{Code: "22003", Message: "integer out of range"}
				}
			} else if ua > maxInt64 {
				return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
			}
			return Datum{Kind: KindInt, Int: int64(ua)}, nil
		}
	case "lcm":
		// lcm(a, b) — least common multiple, non-negative. M0097-0003.
		// Uses uint64 to handle MinInt64 without overflow.
		if len(x.Args) == 2 {
			av, e1 := evalExprSlot(x.Args[0], slot, ctx)
			bv, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || av.IsNull() || bv.IsNull() {
				return NullDatum, nil
			}
			if av.Int == 0 || bv.Int == 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			absA := int64ToAbsUint64(av.Int)
			absB := int64ToAbsUint64(bv.Int)
			ga, gb := absA, absB
			for gb != 0 {
				ga, gb = gb, ga%gb
			}
			// lcm = |a|/gcd * |b| — divide first to reduce overflow risk.
			result := (absA / ga) * absB
			const maxInt64 = uint64(math.MaxInt64)
			isInt4Range := av.Int >= -2147483648 && av.Int <= 2147483647 &&
				bv.Int >= -2147483648 && bv.Int <= 2147483647
			if isInt4Range {
				if result > 2147483647 {
					return Datum{}, &ExecError{Code: "22003", Message: "integer out of range"}
				}
			} else if result > maxInt64 {
				return Datum{}, &ExecError{Code: "22003", Message: "bigint out of range"}
			}
			return Datum{Kind: KindInt, Int: int64(result)}, nil
		}
	case "mod":
		// mod(a, b) → a % b
		if len(x.Args) == 2 {
			a, e1 := evalExprSlot(x.Args[0], slot, ctx)
			b, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			if b.Int == 0 {
				return Datum{}, &ExecError{Code: "22012", Message: "division by zero"}
			}
			return Datum{Kind: KindInt, Int: a.Int % b.Int}, nil
		}
	case "pi":
		return newNumeric(parseNumericOrZero("3.14159265358979"), 14), nil
	case "random":
		// random() → float8 in [0, 1).
		// random(min, max) → uniform integer/numeric in [min, max]. M0097-0071.
		if len(x.Args) >= 2 {
			loD, loErr := evalExprSlot(x.Args[0], slot, ctx)
			hiD, hiErr := evalExprSlot(x.Args[1], slot, ctx)
			if loErr != nil || hiErr != nil || loD.IsNull() || hiD.IsNull() {
				return NullDatum, nil
			}
			// Both args are integer-kind → integer range.
			if loD.Kind == KindInt && hiD.Kind == KindInt {
				lo64, hi64 := loD.Int, hiD.Int
				if lo64 > hi64 {
					return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "lower bound must be less than or equal to upper bound"}
				}
				if lo64 == hi64 {
					return NewIntDatum(lo64), nil
				}
				// Use uint64 arithmetic to avoid int64 overflow for full-range spans.
				rangeU := uint64(hi64) - uint64(lo64) // always correct (two's complement)
				sessionPRNGMu.Lock()
				var rndOffset uint64
				if rangeU == ^uint64(0) { // MaxUint64: full int64 range
					rndOffset = sessionPRNG.Uint64()
				} else {
					rndOffset = sessionPRNG.Uint64() % (rangeU + 1)
				}
				sessionPRNGMu.Unlock()
				v := int64(uint64(lo64) + rndOffset) // two's complement safe
				return NewIntDatum(v), nil
			}
			// Numeric / string args — validate NaN/Inf then compare.
			loS := loD.Format()
			hiS := hiD.Format()
			loM, loSc, loPerr := parseNumeric(loS)
			hiM, hiSc, hiPerr := parseNumeric(hiS)
			if loPerr != nil {
				// Check for NaN/Inf in the raw string.
				loF, _ := datumToFloat64(loD)
				if math.IsNaN(loF) {
					return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "lower bound cannot be NaN"}
				}
				if math.IsInf(loF, 0) {
					return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "lower bound cannot be infinity"}
				}
				return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "invalid arguments for random(min, max)"}
			}
			if hiPerr != nil {
				hiF, _ := datumToFloat64(hiD)
				if math.IsNaN(hiF) {
					return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "upper bound cannot be NaN"}
				}
				if math.IsInf(hiF, 0) {
					return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "upper bound cannot be infinity"}
				}
				return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "invalid arguments for random(min, max)"}
			}
			// Validate lo <= hi.
			cmpA := newNumeric(loM, int(loSc))
			cmpB := newNumeric(hiM, int(hiSc))
			cmp, _ := numericCmp(cmpA, cmpB)
			if cmp > 0 {
				return NullDatum, &ExecError{Code: "22003", Pos: x.Pos(), Message: "lower bound must be less than or equal to upper bound"}
			}
			// For integer-like numerics (no decimal scale), return bigint.
			if loSc == 0 && hiSc == 0 && loM.IsInt64() && hiM.IsInt64() {
				lo64 := loM.Int64()
				hi64 := hiM.Int64()
				if lo64 == hi64 {
					return NewIntDatum(lo64), nil
				}
				// Use uint64 arithmetic to avoid int64 overflow for full-range spans.
				rangeU := uint64(hi64) - uint64(lo64)
				sessionPRNGMu.Lock()
				var rndOffset uint64
				if rangeU == ^uint64(0) {
					rndOffset = sessionPRNG.Uint64()
				} else {
					rndOffset = sessionPRNG.Uint64() % (rangeU + 1)
				}
				sessionPRNGMu.Unlock()
				v := int64(uint64(lo64) + rndOffset)
				return NewIntDatum(v), nil
			}
			// Numeric range: return a numeric in [lo, hi].
			// Apply scale: mantissa is stored as integer * 10^scale.
			sessionPRNGMu.Lock()
			frac := sessionPRNG.Float64()
			sessionPRNGMu.Unlock()
			loFRaw, _ := loM.Float64()
			hiFRaw, _ := hiM.Float64()
			loF := loFRaw / math.Pow10(int(loSc))
			hiF := hiFRaw / math.Pow10(int(hiSc))
			v := loF + frac*(hiF-loF)
			return NewStringDatum(strconv.FormatFloat(v, 'f', -1, 64)), nil
		}
		// Zero-arg: uniform float8 in [0, 1).
		sessionPRNGMu.Lock()
		v := sessionPRNG.Float64()
		sessionPRNGMu.Unlock()
		return NewStringDatum(strconv.FormatFloat(v, 'f', 15, 64)), nil

	case "setseed":
		// setseed(double precision) — seed ∈ [-1, 1]. M0097-0071.
		if len(x.Args) < 1 {
			return NullDatum, nil
		}
		seedD, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || seedD.IsNull() {
			return NullDatum, nil
		}
		seedF, ok := datumToFloat64(seedD)
		if !ok {
			return NullDatum, nil
		}
		// Map [-1, 1] → int64 seed, matching PG convention.
		seedI := int64(seedF * float64(1<<31))
		sessionPRNGMu.Lock()
		sessionPRNG = mathrand.New(mathrand.NewSource(seedI))
		sessionPRNGMu.Unlock()
		return NullDatum, nil // returns void

	case "random_normal":
		// random_normal() → float8 from N(0,1)
		// random_normal(mean, stddev) → N(mean, stddev). M0097-0071.
		mean, stddev := 0.0, 1.0
		if len(x.Args) >= 2 {
			mD, mErr := evalExprSlot(x.Args[0], slot, ctx)
			sD, sErr := evalExprSlot(x.Args[1], slot, ctx)
			if mErr != nil || sErr != nil || mD.IsNull() || sD.IsNull() {
				return NullDatum, nil
			}
			if f, ok := datumToFloat64(mD); ok {
				mean = f
			}
			if f, ok := datumToFloat64(sD); ok {
				stddev = f
			}
		}
		// Box-Muller transform: Z = sqrt(-2*ln(U1)) * cos(2π*U2) ~ N(0,1)
		sessionPRNGMu.Lock()
		u1 := sessionPRNG.Float64()
		u2 := sessionPRNG.Float64()
		sessionPRNGMu.Unlock()
		if u1 == 0 {
			u1 = 1e-15 // avoid log(0)
		}
		z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
		result := mean + stddev*z
		return NewStringDatum(strconv.FormatFloat(result, 'f', 15, 64)), nil

	// ── float8 aggregate state functions ─────────────────────────────────
	// These back stddev, variance, regression aggregates in PostgreSQL.
	// State arrays are represented as PostgreSQL array literals: {n1,n2,...}

	case "float8_accum":
		// float8_accum(float8[], float8) -> float8[]
		// Accumulates one value into a 3-element Youngs-Cramer state {N, Sx, Sxx}.
		if len(x.Args) == 2 {
			stateD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			valD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || valD.IsNull() {
				return NullDatum, nil
			}
			var state [3]float64
			if !stateD.IsNull() {
				elems := parseTextArray(stateD.StringValue())
				if len(elems) == 3 {
					for i := range state {
						state[i], _ = strconv.ParseFloat(elems[i], 64)
					}
				}
			}
			newval, _ := strconv.ParseFloat(valD.Format(), 64)
			nOld := state[0]
			sxOld := state[1]
			sxxOld := state[2]
			n := nOld + 1
			sx := sxOld + newval
			var sxx float64
			if nOld > 0 {
				tmp := newval*n - sx
				sxx = sxxOld + tmp*tmp/(n*nOld)
			} else {
				if math.IsInf(newval, 0) || math.IsNaN(newval) {
					sxx = math.NaN()
				} else {
					sxx = 0
				}
			}
			parts := []string{
				strconv.FormatFloat(n, 'g', -1, 64),
				strconv.FormatFloat(sx, 'g', -1, 64),
				strconv.FormatFloat(sxx, 'g', -1, 64),
			}
			return NewStringDatum("{" + strings.Join(parts, ",") + "}"), nil
		}

	case "float8_regr_accum":
		// float8_regr_accum(float8[], float8, float8) -> float8[]
		// Accumulates one (Y, X) pair into a 6-element regression state
		// {N, Sx, Sxx, Sy, Syy, Sxy}.
		if len(x.Args) == 3 {
			stateD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			yD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			xD, e3 := evalExprSlot(x.Args[2], slot, ctx)
			if e1 != nil || e2 != nil || e3 != nil || yD.IsNull() || xD.IsNull() {
				return NullDatum, nil
			}
			var state [6]float64
			if !stateD.IsNull() {
				elems := parseTextArray(stateD.StringValue())
				if len(elems) == 6 {
					for i := range state {
						state[i], _ = strconv.ParseFloat(elems[i], 64)
					}
				}
			}
			yVal, _ := strconv.ParseFloat(yD.Format(), 64)
			xVal, _ := strconv.ParseFloat(xD.Format(), 64)
			nOld := state[0]
			sxOld, sxxOld := state[1], state[2]
			syOld, syyOld, sxyOld := state[3], state[4], state[5]
			n := nOld + 1
			sx := sxOld + xVal
			sy := syOld + yVal
			var sxx, syy, sxy float64
			if nOld > 0 {
				tmpX := xVal*n - sx
				tmpY := yVal*n - sy
				scale := 1.0 / (n * nOld)
				sxx = sxxOld + tmpX*tmpX*scale
				syy = syyOld + tmpY*tmpY*scale
				sxy = sxyOld + tmpX*tmpY*scale
			}
			parts := []string{
				strconv.FormatFloat(n, 'g', -1, 64),
				strconv.FormatFloat(sx, 'g', -1, 64),
				strconv.FormatFloat(sxx, 'g', -1, 64),
				strconv.FormatFloat(sy, 'g', -1, 64),
				strconv.FormatFloat(syy, 'g', -1, 64),
				strconv.FormatFloat(sxy, 'g', -1, 64),
			}
			return NewStringDatum("{" + strings.Join(parts, ",") + "}"), nil
		}

	case "float8_combine":
		// float8_combine(float8[], float8[]) -> float8[]
		// Merges two 3-element Youngs-Cramer states {N, Sx, Sxx}.
		if len(x.Args) == 2 {
			s1D, e1 := evalExprSlot(x.Args[0], slot, ctx)
			s2D, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			parse3 := func(d Datum) ([3]float64, bool) {
				var s [3]float64
				if d.IsNull() {
					return s, false
				}
				elems := parseTextArray(d.StringValue())
				if len(elems) != 3 {
					return s, false
				}
				for i := range s {
					s[i], _ = strconv.ParseFloat(elems[i], 64)
				}
				return s, true
			}
			st1, ok1 := parse3(s1D)
			st2, ok2 := parse3(s2D)
			if !ok1 || st1[0] == 0 {
				if ok2 {
					return s2D, nil
				}
				return NullDatum, nil
			}
			if !ok2 || st2[0] == 0 {
				return s1D, nil
			}
			n1, sx1, sxx1 := st1[0], st1[1], st1[2]
			n2, sx2, sxx2 := st2[0], st2[1], st2[2]
			n := n1 + n2
			sx := sx1 + sx2
			tmp := sx1/n1 - sx2/n2
			sxx := sxx1 + sxx2 + n1*n2*tmp*tmp/n
			parts := []string{
				strconv.FormatFloat(n, 'g', -1, 64),
				strconv.FormatFloat(sx, 'g', -1, 64),
				strconv.FormatFloat(sxx, 'g', -1, 64),
			}
			return NewStringDatum("{" + strings.Join(parts, ",") + "}"), nil
		}

	case "float8_regr_combine":
		// float8_regr_combine(float8[], float8[]) -> float8[]
		// Merges two 6-element regression states {N, Sx, Sxx, Sy, Syy, Sxy}.
		if len(x.Args) == 2 {
			s1D, e1 := evalExprSlot(x.Args[0], slot, ctx)
			s2D, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			parse6 := func(d Datum) ([6]float64, bool) {
				var s [6]float64
				if d.IsNull() {
					return s, false
				}
				elems := parseTextArray(d.StringValue())
				if len(elems) != 6 {
					return s, false
				}
				for i := range s {
					s[i], _ = strconv.ParseFloat(elems[i], 64)
				}
				return s, true
			}
			st1, ok1 := parse6(s1D)
			st2, ok2 := parse6(s2D)
			if !ok1 || st1[0] == 0 {
				if ok2 {
					return s2D, nil
				}
				return NullDatum, nil
			}
			if !ok2 || st2[0] == 0 {
				return s1D, nil
			}
			n1, sx1, sxx1 := st1[0], st1[1], st1[2]
			sy1, syy1, sxy1 := st1[3], st1[4], st1[5]
			n2, sx2, sxx2 := st2[0], st2[1], st2[2]
			sy2, syy2, sxy2 := st2[3], st2[4], st2[5]
			n := n1 + n2
			sx := sx1 + sx2
			sy := sy1 + sy2
			tmpX := sx1/n1 - sx2/n2
			tmpY := sy1/n1 - sy2/n2
			sxx := sxx1 + sxx2 + n1*n2*tmpX*tmpX/n
			syy := syy1 + syy2 + n1*n2*tmpY*tmpY/n
			sxy := sxy1 + sxy2 + n1*n2*tmpX*tmpY/n
			parts := []string{
				strconv.FormatFloat(n, 'g', -1, 64),
				strconv.FormatFloat(sx, 'g', -1, 64),
				strconv.FormatFloat(sxx, 'g', -1, 64),
				strconv.FormatFloat(sy, 'g', -1, 64),
				strconv.FormatFloat(syy, 'g', -1, 64),
				strconv.FormatFloat(sxy, 'g', -1, 64),
			}
			return NewStringDatum("{" + strings.Join(parts, ",") + "}"), nil
		}

	// ── Array functions ──────────────────────────────────────────────────

	case "array_append":
		// array_append(anyarray, anyelement) → anyarray
		// Appends element to the end of an array. M0097-0035.
		if len(x.Args) == 2 {
			arrD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			elemD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			var elems []string
			if !arrD.IsNull() {
				elems = parseTextArray(arrD.StringValue())
			}
			var elemStr string
			if elemD.IsNull() {
				elemStr = "NULL"
			} else {
				elemStr = elemD.Format()
			}
			elems = append(elems, elemStr)
			return NewStringDatum(formatTextArray(elems)), nil
		}

	case "array_prepend":
		// array_prepend(anyelement, anyarray) → anyarray
		if len(x.Args) == 2 {
			elemD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			arrD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			var elems []string
			if !arrD.IsNull() {
				elems = parseTextArray(arrD.StringValue())
			}
			var elemStr string
			if elemD.IsNull() {
				elemStr = "NULL"
			} else {
				elemStr = elemD.Format()
			}
			elems = append([]string{elemStr}, elems...)
			return NewStringDatum(formatTextArray(elems)), nil
		}

	case "array_cat":
		// array_cat(anyarray, anyarray) → anyarray
		if len(x.Args) == 2 {
			a1, e1 := evalExprSlot(x.Args[0], slot, ctx)
			a2, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			var elems []string
			if !a1.IsNull() {
				elems = append(elems, parseTextArray(a1.StringValue())...)
			}
			if !a2.IsNull() {
				elems = append(elems, parseTextArray(a2.StringValue())...)
			}
			return NewStringDatum(formatTextArray(elems)), nil
		}

	case "array_remove":
		// array_remove(anyarray, anyelement) → anyarray — removes every element
		// equal to the second argument from a 1-D array. pg_dump's getTables strips
		// the view check_option markers from reloptions with a nested
		// array_remove(array_remove(c.reloptions,'check_option=local'),
		// 'check_option=cascaded'). M0110-0001 / DU-002 slice 5.
		//
		// PG's array_remove is NotStrict on the element (a NULL element removes the
		// array's NULL entries) but returns NULL for a NULL array. Element matching
		// uses the type's default btree equality; goopg's text-array representation
		// compares the formatted element text, with a NULL element matching the
		// "NULL" placeholder produced by the array_append/_cat siblings.
		if len(x.Args) == 2 {
			arrD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			elemD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			if arrD.IsNull() {
				return NullDatum, nil
			}
			target := "NULL"
			if !elemD.IsNull() {
				target = elemD.Format()
			}
			src := parseTextArray(arrD.StringValue())
			out := make([]string, 0, len(src))
			for _, e := range src {
				if e == target {
					continue
				}
				out = append(out, e)
			}
			return NewStringDatum(formatTextArray(out)), nil
		}

	case "array_dims":
		// array_dims(anyarray) → text — returns '[1:N]' for a 1-D array of N elements.
		if len(x.Args) == 1 {
			arrD, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || arrD.IsNull() {
				return NullDatum, nil
			}
			elems := parseTextArray(arrD.StringValue())
			return NewStringDatum(fmt.Sprintf("[1:%d]", len(elems))), nil
		}

	case "array_ndims":
		// array_ndims(anyarray) → int — returns 1 for a 1-D array.
		if len(x.Args) == 1 {
			arrD, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || arrD.IsNull() {
				return NullDatum, nil
			}
			_ = parseTextArray(arrD.StringValue())
			return NewIntDatum(1), nil
		}

	case "regexp_split_to_array":
		// regexp_split_to_array(string, pattern [, flags]) → text[]
		// Splits string by regexp and returns the parts as an array. M0097-0035.
		if len(x.Args) >= 2 {
			strD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			patD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil || strD.IsNull() || patD.IsNull() {
				return NullDatum, nil
			}
			flags := ""
			if len(x.Args) >= 3 {
				flagD, fe := evalExprSlot(x.Args[2], slot, ctx)
				if fe == nil && !flagD.IsNull() {
					flags = flagD.StringValue()
				}
			}
			goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
			if ferr != nil {
				return NullDatum, ferr
			}
			if global {
				// regexp_split_to_array() rejects 'g' (regexp.c:1818-1826
				// regexp_split_to_array — "User mustn't specify 'g'").
				return NullDatum, &ExecError{Code: "22023",
					Message: `regexp_split_to_array() does not support the "global" option`}
			}
			pat := patD.StringValue()
			// Build RE2 pattern with flags.
			reStr := goFlags + pat
			re, rerr := regexp.Compile(reStr)
			if rerr != nil {
				// No Pos: pure runtime evaluation (RE_compile_and_cache,
				// postgres/src/backend/utils/adt/regexp.c, has no
				// errposition call). M0134-0070.
				return NullDatum, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", rerr)}
			}
			parts := re.Split(strD.StringValue(), -1)
			return NewStringDatum(formatTextArray(parts)), nil
		}

	case "regexp_count":
		// regexp_count(string, pattern [, start [, flags]]) → int4
		// postgres/src/backend/utils/adt/regexp.c:1138 regexp_count.
		if len(x.Args) >= 2 && len(x.Args) <= 4 {
			strD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			patD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil {
				return Datum{}, e1
			}
			if e2 != nil {
				return Datum{}, e2
			}
			if strD.IsNull() || patD.IsNull() {
				return NullDatum, nil
			}
			start := int64(1)
			if len(x.Args) >= 3 {
				d, e := evalExprSlot(x.Args[2], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				start = d.Int
				if start <= 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "start", start)}
				}
			}
			flags := ""
			if len(x.Args) >= 4 {
				d, e := evalExprSlot(x.Args[3], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				flags = d.StringValue()
			}
			goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
			if ferr != nil {
				return Datum{}, ferr
			}
			if global {
				// regexp.c:1160-1166 — "User mustn't specify 'g'".
				return Datum{}, &ExecError{Code: "22023",
					Message: `regexp_count() does not support the "global" option`}
			}
			re, rerr := regexpCompilePattern(patD.StringValue(), flags, goFlags)
			if rerr != nil {
				return Datum{}, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", rerr)}
			}
			runes := []rune(strD.StringValue())
			winStart := int(start) - 1
			if winStart > len(runes) {
				winStart = len(runes)
			}
			window := string(runes[winStart:])
			matches := re.FindAllStringIndex(window, -1)
			return Datum{Kind: KindInt, Int: int64(len(matches))}, nil
		}

	case "regexp_like":
		// regexp_like(string, pattern [, flags]) → bool.
		// postgres/src/backend/utils/adt/regexp.c:1329 regexp_like — direct
		// compile+execute, no start/N, always searches from position 0.
		if len(x.Args) >= 2 && len(x.Args) <= 3 {
			strD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			patD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil {
				return Datum{}, e1
			}
			if e2 != nil {
				return Datum{}, e2
			}
			if strD.IsNull() || patD.IsNull() {
				return NullDatum, nil
			}
			flags := ""
			if len(x.Args) >= 3 {
				d, e := evalExprSlot(x.Args[2], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				flags = d.StringValue()
			}
			goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
			if ferr != nil {
				return Datum{}, ferr
			}
			if global {
				return Datum{}, &ExecError{Code: "22023",
					Message: `regexp_like() does not support the "global" option`}
			}
			re, rerr := regexpCompilePattern(patD.StringValue(), flags, goFlags)
			if rerr != nil {
				return Datum{}, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", rerr)}
			}
			return NewBoolDatum(re.MatchString(strD.StringValue())), nil
		}

	case "regexp_instr":
		// regexp_instr(string, pattern [, start [, N [, endoption [, flags [, subexpr]]]]]) → int4
		// postgres/src/backend/utils/adt/regexp.c:1198 regexp_instr.
		if len(x.Args) >= 2 && len(x.Args) <= 7 {
			strD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			patD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil {
				return Datum{}, e1
			}
			if e2 != nil {
				return Datum{}, e2
			}
			if strD.IsNull() || patD.IsNull() {
				return NullDatum, nil
			}
			start := int64(1)
			if len(x.Args) >= 3 {
				d, e := evalExprSlot(x.Args[2], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				start = d.Int
				if start <= 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "start", start)}
				}
			}
			n := int64(1)
			if len(x.Args) >= 4 {
				d, e := evalExprSlot(x.Args[3], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				n = d.Int
				if n <= 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "n", n)}
				}
			}
			endoption := int64(0)
			if len(x.Args) >= 5 {
				d, e := evalExprSlot(x.Args[4], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				endoption = d.Int
				if endoption != 0 && endoption != 1 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "endoption", endoption)}
				}
			}
			flags := ""
			if len(x.Args) >= 6 {
				d, e := evalExprSlot(x.Args[5], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				flags = d.StringValue()
			}
			subexpr := int64(0)
			if len(x.Args) >= 7 {
				d, e := evalExprSlot(x.Args[6], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				subexpr = d.Int
				if subexpr < 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "subexpr", subexpr)}
				}
			}
			goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
			if ferr != nil {
				return Datum{}, ferr
			}
			if global {
				return Datum{}, &ExecError{Code: "22023",
					Message: `regexp_instr() does not support the "global" option`}
			}
			window, so, eo, ok, merr := regexpInstrSubstrLocate(strD.StringValue(), patD.StringValue(), flags, goFlags, int(start), int(n), int(subexpr))
			if merr != nil {
				return Datum{}, merr
			}
			if !ok {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			byteOff := so
			if endoption == 1 {
				byteOff = eo
			}
			return Datum{Kind: KindInt, Int: int64(regexpWindowCharPos(window, int(start), byteOff))}, nil
		}

	case "regexp_substr":
		// regexp_substr(string, pattern [, start [, N [, flags [, subexpr]]]]) → text
		// postgres/src/backend/utils/adt/regexp.c:1904 regexp_substr. Same
		// start/N/subexpr validation as regexp_instr above, but flags/subexpr
		// are ONE SLOT EARLIER (no endoption arg) — shares
		// regexpInstrSubstrLocate so the two "pos" computations can't drift.
		if len(x.Args) >= 2 && len(x.Args) <= 6 {
			strD, e1 := evalExprSlot(x.Args[0], slot, ctx)
			patD, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil {
				return Datum{}, e1
			}
			if e2 != nil {
				return Datum{}, e2
			}
			if strD.IsNull() || patD.IsNull() {
				return NullDatum, nil
			}
			start := int64(1)
			if len(x.Args) >= 3 {
				d, e := evalExprSlot(x.Args[2], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				start = d.Int
				if start <= 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "start", start)}
				}
			}
			n := int64(1)
			if len(x.Args) >= 4 {
				d, e := evalExprSlot(x.Args[3], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				n = d.Int
				if n <= 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "n", n)}
				}
			}
			flags := ""
			if len(x.Args) >= 5 {
				d, e := evalExprSlot(x.Args[4], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				flags = d.StringValue()
			}
			subexpr := int64(0)
			if len(x.Args) >= 6 {
				d, e := evalExprSlot(x.Args[5], slot, ctx)
				if e != nil {
					return Datum{}, e
				}
				if d.IsNull() {
					return NullDatum, nil
				}
				subexpr = d.Int
				if subexpr < 0 {
					return Datum{}, &ExecError{Code: "22023",
						Message: fmt.Sprintf("invalid value for parameter %q: %d", "subexpr", subexpr)}
				}
			}
			goFlags, global, ferr := pgRegexFlagsToGoModifiers(flags)
			if ferr != nil {
				return Datum{}, ferr
			}
			if global {
				return Datum{}, &ExecError{Code: "22023",
					Message: `regexp_substr() does not support the "global" option`}
			}
			window, so, eo, ok, merr := regexpInstrSubstrLocate(strD.StringValue(), patD.StringValue(), flags, goFlags, int(start), int(n), int(subexpr))
			if merr != nil {
				return Datum{}, merr
			}
			if !ok {
				return NullDatum, nil
			}
			return NewStringDatum(window[so:eo]), nil
		}

	// ── Type conversion functions (M0097-0005) ────────────────────────────
	case "to_number":
		// to_number(text, fmt) → numeric — simplified: parse as numeric
		if len(x.Args) >= 1 {
			s, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			cleaned := strings.TrimSpace(strings.ReplaceAll(s.StringValue(), ",", ""))
			m, sc, perr := parseNumeric(cleaned)
			if perr != nil {
				return NewStringDatum(cleaned), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "to_hex":
		// to_hex32/to_hex64 zero-extend the argument's two's-complement bit
		// pattern rather than sign-formatting it (PG oracle:
		// postgres/src/backend/utils/adt/varlena.c:5254-5267). ArgWidth is
		// stamped at plan time (see resolveExpr's to_hex intercept in
		// internal/optimizer/planner.go); default/empty ArgWidth uses the
		// uint32 (int4 overload) path, which is also correct for all
		// positive-value cases since %x does not zero-pad.
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if x.ArgWidth == "int8" {
				return NewStringDatum(fmt.Sprintf("%x", uint64(v.Int))), nil
			}
			return NewStringDatum(fmt.Sprintf("%x", uint32(v.Int))), nil
		}
	case "to_bin":
		// to_bin32/to_bin64 zero-extend the argument's two's-complement bit
		// pattern rather than sign-formatting it (PG oracle:
		// postgres/src/backend/utils/adt/varlena.c:5190-5248, 5254-5267).
		// ArgWidth is stamped at plan time (see resolveExpr's to_hex/to_bin/
		// to_oct intercept in internal/optimizer/planner.go); default/empty
		// ArgWidth uses the uint32 (int4 overload) path, which is also
		// correct for all positive-value cases since strconv.FormatUint does
		// not zero-pad.
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if x.ArgWidth == "int8" {
				return NewStringDatum(strconv.FormatUint(uint64(v.Int), 2)), nil
			}
			return NewStringDatum(strconv.FormatUint(uint64(uint32(v.Int)), 2)), nil
		}
	case "to_oct":
		// to_oct32/to_oct64 zero-extend the argument's two's-complement bit
		// pattern rather than sign-formatting it (PG oracle:
		// postgres/src/backend/utils/adt/varlena.c:5190-5248, 5254-5267).
		// ArgWidth is stamped at plan time (see resolveExpr's to_hex/to_bin/
		// to_oct intercept in internal/optimizer/planner.go); default/empty
		// ArgWidth uses the uint32 (int4 overload) path, which is also
		// correct for all positive-value cases since strconv.FormatUint does
		// not zero-pad.
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if x.ArgWidth == "int8" {
				return NewStringDatum(strconv.FormatUint(uint64(v.Int), 8)), nil
			}
			return NewStringDatum(strconv.FormatUint(uint64(uint32(v.Int)), 8)), nil
		}
	case "get_byte":
		// get_byte(bytea, int4) -> int4 (postgres/src/backend/utils/adt/
		// varlena.c:3310-3329 byteaGetByte). M0134-0070.
		if len(x.Args) == 2 {
			bv, berr := evalExprSlot(x.Args[0], slot, ctx)
			if berr != nil {
				return NullDatum, berr
			}
			if bv.IsNull() {
				return NullDatum, nil
			}
			nv, nerr := evalExprSlot(x.Args[1], slot, ctx)
			if nerr != nil {
				return NullDatum, nerr
			}
			if nv.IsNull() {
				return NullDatum, nil
			}
			b := bv.BytesValue()
			n := nv.Int
			if n < 0 || n >= int64(len(b)) {
				// No Pos: byteaGetByte (varlena.c:3310-3329) has no
				// errposition call. M0134-0070.
				return Datum{}, &ExecError{Code: "2202E",
					Message: fmt.Sprintf("index %d out of valid range, 0..%d", n, len(b)-1)}
			}
			return Datum{Kind: KindInt, Int: int64(b[n])}, nil
		}
	case "set_byte":
		// set_byte(bytea, int4, int4) -> bytea (postgres/src/backend/utils/
		// adt/varlena.c:3369-3399 byteaSetByte). M0134-0070.
		if len(x.Args) == 3 {
			bv, berr := evalExprSlot(x.Args[0], slot, ctx)
			if berr != nil {
				return NullDatum, berr
			}
			if bv.IsNull() {
				return NullDatum, nil
			}
			nv, nerr := evalExprSlot(x.Args[1], slot, ctx)
			if nerr != nil {
				return NullDatum, nerr
			}
			if nv.IsNull() {
				return NullDatum, nil
			}
			newv, verr := evalExprSlot(x.Args[2], slot, ctx)
			if verr != nil {
				return NullDatum, verr
			}
			if newv.IsNull() {
				return NullDatum, nil
			}
			src := bv.BytesValue()
			n := nv.Int
			if n < 0 || n >= int64(len(src)) {
				// No Pos: byteaSetByte (varlena.c:3369-3399) has no
				// errposition call. M0134-0070.
				return Datum{}, &ExecError{Code: "2202E",
					Message: fmt.Sprintf("index %d out of valid range, 0..%d", n, len(src)-1)}
			}
			out := make([]byte, len(src))
			copy(out, src)
			out[n] = byte(newv.Int)
			return NewBytesDatum(out), nil
		}
	case "get_bit":
		// get_bit(bytea, int8) -> int4 (postgres/src/backend/utils/adt/
		// varlena.c:3330-3364 byteaGetBit). Bit numbering is LSB-first within
		// each byte: bitNo = n%8, tested via `byte & (1 << bitNo)`
		// (varlena.c:3361), so bitNo 0 is the byte's least-significant bit.
		// M0134-0070.
		if len(x.Args) == 2 {
			bv, berr := evalExprSlot(x.Args[0], slot, ctx)
			if berr != nil {
				return NullDatum, berr
			}
			if bv.IsNull() {
				return NullDatum, nil
			}
			nv, nerr := evalExprSlot(x.Args[1], slot, ctx)
			if nerr != nil {
				return NullDatum, nerr
			}
			if nv.IsNull() {
				return NullDatum, nil
			}
			b := bv.BytesValue()
			n := nv.Int
			bitLen := int64(len(b)) * 8
			if n < 0 || n >= bitLen {
				// No Pos: byteaGetBit (varlena.c:3330-3364) has no
				// errposition call. M0134-0070.
				return Datum{}, &ExecError{Code: "2202E",
					Message: fmt.Sprintf("index %d out of valid range, 0..%d", n, bitLen-1)}
			}
			byteNo := n / 8
			bitNo := uint(n % 8)
			if b[byteNo]&(1<<bitNo) != 0 {
				return Datum{Kind: KindInt, Int: 1}, nil
			}
			return Datum{Kind: KindInt, Int: 0}, nil
		}
	case "set_bit":
		// set_bit(bytea, int8, int4) -> bytea (postgres/src/backend/utils/
		// adt/varlena.c:3400-3448 byteaSetBit). Same LSB-first bit numbering
		// as get_bit; also validates the new-bit-value arg is 0 or 1
		// (varlena.c:3437-3439, ERRCODE_INVALID_PARAMETER_VALUE=22023).
		// M0134-0070.
		if len(x.Args) == 3 {
			bv, berr := evalExprSlot(x.Args[0], slot, ctx)
			if berr != nil {
				return NullDatum, berr
			}
			if bv.IsNull() {
				return NullDatum, nil
			}
			nv, nerr := evalExprSlot(x.Args[1], slot, ctx)
			if nerr != nil {
				return NullDatum, nerr
			}
			if nv.IsNull() {
				return NullDatum, nil
			}
			newv, verr := evalExprSlot(x.Args[2], slot, ctx)
			if verr != nil {
				return NullDatum, verr
			}
			if newv.IsNull() {
				return NullDatum, nil
			}
			src := bv.BytesValue()
			n := nv.Int
			bitLen := int64(len(src)) * 8
			if n < 0 || n >= bitLen {
				// No Pos: byteaSetBit (varlena.c:3400-3448) has no
				// errposition call. M0134-0070.
				return Datum{}, &ExecError{Code: "2202E",
					Message: fmt.Sprintf("index %d out of valid range, 0..%d", n, bitLen-1)}
			}
			newBit := newv.Int
			if newBit != 0 && newBit != 1 {
				return Datum{}, &ExecError{Code: "22023",
					Message: "new bit must be 0 or 1"}
			}
			byteNo := n / 8
			bitNo := uint(n % 8)
			out := make([]byte, len(src))
			copy(out, src)
			if newBit == 0 {
				out[byteNo] &^= 1 << bitNo
			} else {
				out[byteNo] |= 1 << bitNo
			}
			return NewBytesDatum(out), nil
		}
	case "encode":
		// encode(bytea, format) -> text. Formats: base64, escape, hex
		// (binary_encode, postgres/src/backend/utils/adt/encode.c). This was a
		// stub returning "" for every input until M0125-0021 — a hex dump that
		// silently produced the empty string rather than erroring.
		if len(x.Args) != 2 {
			return NullDatum, nil
		}
		src, serr := evalExprSlot(x.Args[0], slot, ctx)
		if serr != nil {
			return NullDatum, serr
		}
		if src.IsNull() {
			return NullDatum, nil
		}
		encFmt, ferr := evalExprSlot(x.Args[1], slot, ctx)
		if ferr != nil {
			return NullDatum, ferr
		}
		if encFmt.IsNull() {
			return NullDatum, nil
		}
		// The argument is bytea; a KindString here is an unknown-type literal
		// that PG would have coerced through byteain first.
		raw := src.BytesValue()
		if src.Kind != KindBytes {
			b, berr := byteaIn(src.StringValue(), x.Pos())
			if berr != nil {
				return NullDatum, berr
			}
			raw = b
		}
		switch strings.ToLower(strings.TrimSpace(encFmt.Format())) {
		case "hex":
			return NewStringDatum(hexEncodePG(raw)), nil
		case "base64":
			return NewStringDatum(b64EncodePG(raw)), nil
		case "escape":
			return NewStringDatum(escEncodePG(raw)), nil
		default:
			return NullDatum, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unrecognized encoding: %q", encFmt.Format())}
		}
	case "decode":
		// decode(text, format) -> bytea. Formats: hex, escape, base64.
		if len(x.Args) != 2 {
			return NullDatum, nil
		}
		src, serr := evalExprSlot(x.Args[0], slot, ctx)
		if serr != nil || src.IsNull() {
			return NullDatum, nil
		}
		fmtArg, ferr := evalExprSlot(x.Args[1], slot, ctx)
		if ferr != nil || fmtArg.IsNull() {
			return NullDatum, nil
		}
		format := strings.ToLower(strings.TrimSpace(fmtArg.Format()))
		// M0125-0021: all three decoders now come from bytea.go, shared with
		// the `::bytea` cast, so decode() and byteain cannot drift. Three
		// deviations were removed here: the `\x` prefix was stripped before
		// hex-decoding (PG errors — `decode('\xaabb','hex')` is
		// `invalid hexadecimal digit: "\"`), an invalid backslash sequence in
		// escape format was passed through instead of raising 22P02, and
		// base64 rejected the newlines PG's own encode() emits.
		switch format {
		case "hex":
			b, err := hexDecodePG(src.Format(), x.Pos())
			if err != nil {
				return NullDatum, err
			}
			return NewBytesDatum(b), nil
		case "escape":
			b, err := escDecodePG(src.Format(), x.Pos())
			if err != nil {
				return NullDatum, err
			}
			return NewBytesDatum(b), nil
		case "base64":
			b, err := b64DecodePG(src.Format(), x.Pos())
			if err != nil {
				return NullDatum, err
			}
			return NewBytesDatum(b), nil
		default:
			return NullDatum, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unrecognized encoding: %q", fmtArg.Format())}
		}
	case "convert_from":
		// convert_from(bytea, src_encoding name) -> text: decode src_encoding
		// bytes into the server encoding (always UTF8 here). Port of
		// pg_convert_from (postgres/src/backend/utils/adt/mbutils.c).
		// Resolution goes through mb.BuiltinLookup, the same bootstrap-only
		// conversion set maybeConvertCellsForClientEncoding uses for
		// client_encoding — a CREATE CONVERSION-registered proc is not yet
		// consulted (M0122-0008 scope note, carried forward). M0134-0121.
		if len(x.Args) != 2 {
			return NullDatum, nil
		}
		src, serr := evalExprSlot(x.Args[0], slot, ctx)
		if serr != nil || src.IsNull() {
			return NullDatum, nil
		}
		encArg, eerr := evalExprSlot(x.Args[1], slot, ctx)
		if eerr != nil || encArg.IsNull() {
			return NullDatum, nil
		}
		raw := src.BytesValue()
		if src.Kind != KindBytes {
			b, berr := byteaIn(src.StringValue(), x.Pos())
			if berr != nil {
				return NullDatum, berr
			}
			raw = b
		}
		srcEnc := catalog.EncodingNameToID(encArg.StringValue())
		if srcEnc < 0 {
			return NullDatum, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid encoding name %q", encArg.StringValue())}
		}
		converted, cerr := mb.DoEncodingConversion(raw, srcEnc, mb.PG_UTF8, mb.BuiltinLookup)
		if cerr != nil {
			return NullDatum, &ExecError{Code: "22021", Pos: x.Pos(), Message: cerr.Error()}
		}
		return NewStringDatum(string(converted)), nil
	case "convert_to":
		// convert_to(text, dest_encoding name) -> bytea: encode server-
		// encoding (UTF8) text into dest_encoding bytes. Port of
		// pg_convert_to (postgres/src/backend/utils/adt/mbutils.c). Same
		// mb.BuiltinLookup scope note as convert_from. M0134-0121.
		if len(x.Args) != 2 {
			return NullDatum, nil
		}
		src, serr := evalExprSlot(x.Args[0], slot, ctx)
		if serr != nil || src.IsNull() {
			return NullDatum, nil
		}
		encArg, eerr := evalExprSlot(x.Args[1], slot, ctx)
		if eerr != nil || encArg.IsNull() {
			return NullDatum, nil
		}
		destEnc := catalog.EncodingNameToID(encArg.StringValue())
		if destEnc < 0 {
			return NullDatum, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid encoding name %q", encArg.StringValue())}
		}
		converted, cerr := mb.DoEncodingConversion([]byte(src.StringValue()), mb.PG_UTF8, destEnc, mb.BuiltinLookup)
		if cerr != nil {
			return NullDatum, &ExecError{Code: "22021", Pos: x.Pos(), Message: cerr.Error()}
		}
		return NewBytesDatum(converted), nil
	case "crc32":
		// crc32(bytea) -> int8. Standard CRC-32 (IEEE/zlib polynomial).
		// PG oracle: pg_crc.c:106-116 (crc32_bytea) —
		// INIT_TRADITIONAL_CRC32/COMP_TRADITIONAL_CRC32/FIN_TRADITIONAL_CRC32,
		// which is exactly the zlib CRC-32 that crc32.ChecksumIEEE computes.
		// M0134-0070.
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			return Datum{Kind: KindInt, Int: int64(crc32.ChecksumIEEE(raw))}, nil
		}
	case "crc32c":
		// crc32c(bytea) -> int8. CRC-32C (Castagnoli polynomial).
		// PG oracle: pg_crc.c:119-128 (crc32c_bytea). M0134-0070.
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			checksum := crc32.Checksum(raw, crc32.MakeTable(crc32.Castagnoli))
			// A CRC-32C result is unsigned 32-bit; widen to int64 without sign
			// extension so values above 2^31 stay positive (PG_RETURN_INT64
			// widens the unsigned pg_crc32c the same way).
			return Datum{Kind: KindInt, Int: int64(checksum)}, nil
		}
	case "bit_count":
		// bit_count(bytea) -> int8. Population count over the raw bytes.
		// PG oracle: varlena.c bytea_bit_count — pg_popcount(VARDATA_ANY(t1),
		// VARSIZE_ANY_EXHDR(t1)). Only the bytea overload (OID 6162) is
		// implemented; the bit(n) overload (OID 6163) is out of scope.
		// M0134-0070.
		if len(x.Args) == 1 {
			src, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if src.IsNull() {
				return NullDatum, nil
			}
			raw := src.BytesValue()
			if src.Kind != KindBytes {
				b, berr := byteaIn(src.StringValue(), x.Pos())
				if berr != nil {
					return NullDatum, berr
				}
				raw = b
			}
			var count int64
			i := 0
			for ; i+8 <= len(raw); i += 8 {
				count += int64(bits.OnesCount64(uint64(raw[i]) | uint64(raw[i+1])<<8 |
					uint64(raw[i+2])<<16 | uint64(raw[i+3])<<24 | uint64(raw[i+4])<<32 |
					uint64(raw[i+5])<<40 | uint64(raw[i+6])<<48 | uint64(raw[i+7])<<56))
			}
			for ; i < len(raw); i++ {
				count += int64(bits.OnesCount8(raw[i]))
			}
			return Datum{Kind: KindInt, Int: count}, nil
		}
	case "unistr":
		// unistr(text) -> text. PG oracle: varlena.c:6762-6925 (unistr).
		// M0134-0070; see internal/executor/unistr.go for the escape scanner.
		if len(x.Args) == 1 {
			s, serr := evalExprSlot(x.Args[0], slot, ctx)
			if serr != nil {
				return NullDatum, serr
			}
			if s.IsNull() {
				return NullDatum, nil
			}
			out, uerr := unistrDecode(s.StringValue(), x.Pos())
			if uerr != nil {
				return NullDatum, uerr
			}
			return NewStringDatum(out), nil
		}

	// ── Misc functions (M0097-0005) ────────────────────────────────────────
	case "coalesce":
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err == nil && !v.IsNull() {
				return v, nil
			}
		}
		return NullDatum, nil
	case "nullif":
		if len(x.Args) == 2 {
			a, e1 := evalExprSlot(x.Args[0], slot, ctx)
			b, e2 := evalExprSlot(x.Args[1], slot, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			if !a.IsNull() && !b.IsNull() && a.Format() == b.Format() {
				return NullDatum, nil
			}
			return a, nil
		}
	case "greatest":
		best := NullDatum
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			if best.IsNull() {
				best = v
				continue
			}
			cmp, cerr := compareDatum(v, best, x.Pos())
			if cerr != nil || cmp > 0 {
				best = v
			}
		}
		return best, nil
	case "least":
		best := NullDatum
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			if best.IsNull() {
				best = v
				continue
			}
			cmp, cerr := compareDatum(v, best, x.Pos())
			if cerr != nil || cmp < 0 {
				best = v
			}
		}
		return best, nil
	case "num_nonnulls":
		cnt := 0
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err == nil && !v.IsNull() {
				cnt++
			}
		}
		return Datum{Kind: KindInt, Int: int64(cnt)}, nil
	case "num_nulls":
		cnt := 0
		for _, arg := range x.Args {
			v, err := evalExprSlot(arg, slot, ctx)
			if err != nil || v.IsNull() {
				cnt++
			}
		}
		return Datum{Kind: KindInt, Int: int64(cnt)}, nil
	case "pg_typeof":
		if len(x.Args) == 1 {
			var cat catalog.Catalog
			if ctx != nil {
				cat = ctx.Catalog
			}
			// pg_typeof's declared SQL return type is regtype, whose wire/
			// binary representation IS the type's OID (mirrors regclass/
			// regproc) — resolve the display name to its OID here rather than
			// returning display text, so a further `::oid` cast is a plain
			// identity reinterpretation instead of misparsing display text
			// through oidin(). The wire/display layer (planner.exprType's
			// FuncCall case + dispatch.go's typeOIDFor/appendTypedCellText)
			// renders the OID back to the display name for a plain
			// `SELECT pg_typeof(...)`. M0122-0005 pg_typeof()::oid follow-up.
			//
			// Fast path: planner folded pg_typeof(expr) to a StringConst
			// holding the pre-computed type name — resolve it without
			// evaluating. M0097-0035.
			if sc, ok := x.Args[0].(*optimizer.StringConst); ok {
				return NewIntDatum(int64(pgTypeofOIDForName(cat, sc.Value))), nil
			}
			// Runtime path: evaluate arg and map Datum kind to PG type name.
			// KindString must map to "text" here (NOT the string value).
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NewIntDatum(int64(pgTypeofOIDForName(cat, "unknown"))), nil
			}
			var typName string
			switch v.Kind {
			case KindString:
				typName = "text"
			case KindInt:
				typName = "integer"
			case KindBool:
				typName = "boolean"
			case KindNumeric:
				typName = "numeric"
			case KindTime:
				typName = "timestamp without time zone"
			default:
				typName = "text"
			}
			return NewIntDatum(int64(pgTypeofOIDForName(cat, typName))), nil
		}
	case "format_type":
		// format_type(oid, typemod) — returns the SQL name of a data type given
		// its type OID and optional type modifier. Used by psql \d+ meta-commands.
		// NULL OID → NULL result. typemod=-1 or NULL means no modifier.
		// M0097-0023.
		if len(x.Args) >= 1 {
			oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || oidArg.IsNull() {
				return NullDatum, nil
			}
			typeOID := oidArg.Int
			typmod := int64(-1)
			if len(x.Args) >= 2 {
				modArg, merr := evalExprSlot(x.Args[1], slot, ctx)
				if merr == nil && !modArg.IsNull() {
					typmod = modArg.Int
				}
			}
			name := formatTypeOID(typeOID, typmod)
			if name == "???" && ctx != nil && ctx.Catalog != nil {
				// A user-defined type's pg_type OID is dynamically allocated,
				// so formatTypeOID (built-ins only) returns the unknown
				// sentinel; resolve it via the shared enum/domain/composite/
				// range/multirange lookup (also used by the ::regtype cast).
				// format_type only schema-qualifies when the type's ACTUAL
				// schema (from its NamespaceOID, slice A) is not visible on
				// the effective search_path — a per-schema predicate — and
				// renders via regOutQualified (quote_qualified_identifier),
				// matching format_type_extended (format_type.c:303-326).
				// e.g. pg_dump's search_path='' (ALWAYS_SECURE_SEARCH_PATH_SQL)
				// makes every schema non-visible, forcing the qualified form.
				// DU-002 slices 88-90/249-251/(M0110-0001); M0119-0006 slice B.
				if uname, ok := userTypeNameForOID(ctx.Catalog, uint32(typeOID), func(s string) bool { return !RegObjectSchemaVisible(ctx, s) }); ok {
					name = uname
				}
			}
			return NewStringDatum(name), nil
		}
	case "pg_encoding_to_char":
		// pg_encoding_to_char(int4) → name: the canonical encoding name for a
		// pg_enc integer ID, or "" for an out-of-range ID (mirrors
		// pg_encoding_to_char in encnames.c). pg_dump's dumpConversion calls it on
		// pg_conversion.conforencoding / contoencoding to render the FOR/TO
		// encoding-name literals. NULL input → NULL. DU-002 slice 399.
		if len(x.Args) >= 1 {
			encArg, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || encArg.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(catalog.EncodingIDToName(int32(encArg.Int))), nil
		}
case "pg_char_to_encoding":
	// pg_char_to_encoding(name) → int4: resolves an encoding name
	// (any case, with arbitrary punctuation that clean_encoding_name
	// strips) to its pg_enc integer ID, or -1 if unknown. Mirrors
	// pg_char_to_encoding in encnames.c. NULL input → NULL. M0122-0008.
	if len(x.Args) >= 1 {
		encArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || encArg.IsNull() {
			return NullDatum, nil
		}
		return Datum{Kind: KindInt, Int: int64(catalog.EncodingNameToID(encArg.StringValue()))}, nil
	}
	case "pg_column_size":
		if len(x.Args) == 1 {
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len(v.Format()))}, nil
		}
	case "session_user":
		// session_user() is the authenticated login role, unaffected by SET
		// ROLE — only SET/RESET SESSION AUTHORIZATION changes it. M0134-0009,
		// PG oracle: miscinit.c GetSessionUserId.
		return NewStringDatum(ctx.SessionUserName()), nil
	case "current_user", "current_role", "user":
		// current_user()/current_role/user are the effective role: the SET
		// ROLE target when active, else the session user. M0134-0009, PG
		// oracle: miscinit.c GetCurrentRoleId.
		return NewStringDatum(ctx.EffectiveUserName()), nil
	case "version":
		return NewStringDatum("PostgreSQL 18.3 goopg compatible"), nil
	case "pg_current_xact_id", "txid_current":
		if ctx.Tx.XID != 0 {
			return Datum{Kind: KindInt, Int: int64(ctx.Tx.XID)}, nil
		}
		return Datum{Kind: KindInt, Int: 0}, nil
	// txid_current_if_assigned() → xid8: same as pg_current_xact_id() but
	// returns NULL instead of assigning a new xid. M0134-0080 (pg_proc OID
	// 3348, handler pg_current_xact_id_if_assigned, xid8funcs.c).
	case "txid_current_if_assigned":
		if ctx.Tx.XID != 0 {
			return Datum{Kind: KindInt, Int: int64(ctx.Tx.XID)}, nil
		}
		return NullDatum, nil
	// txid_current_snapshot() → pg_snapshot: the statement's active
	// snapshot rendered in xmin:xmax:xip,... form. Mirrors pg_current_snapshot
	// (xid8funcs.c), reading ctx.Snap directly rather than the procarray (v0
	// has no separate GetActiveSnapshot() call). M0134-0080 (pg_proc OID 2944).
	case "txid_current_snapshot":
		xmin := uint64(ctx.Snap.Xmin)
		xmax := uint64(ctx.Snap.Xmax)
		var xips []uint64
		for _, ip := range ctx.Snap.InProgress {
			v := uint64(ip)
			if v >= xmin && v < xmax {
				xips = append(xips, v)
			}
		}
		sort.Slice(xips, func(i, j int) bool { return xips[i] < xips[j] })
		xips = dedupSortedUint64(xips)
		return NewStringDatum(formatPgSnapshot(xmin, xmax, xips)), nil
	// txid_snapshot_xmin(pg_snapshot) / txid_snapshot_xmax(pg_snapshot) → xid8.
	// Mirrors pg_snapshot_xmin/pg_snapshot_xmax (xid8funcs.c). M0134-0080
	// (pg_proc OID 2945/2946).
	case "txid_snapshot_xmin", "txid_snapshot_xmax":
		if len(x.Args) != 1 {
			return NullDatum, nil
		}
		argD, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || argD.IsNull() {
			return NullDatum, err
		}
		xmin, xmax, _, ok := parsePgSnapshotParts(argD.StringValue())
		if !ok {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type pg_snapshot: %q", argD.StringValue())}
		}
		if name == "txid_snapshot_xmin" {
			return Datum{Kind: KindInt, Int: int64(xmin)}, nil
		}
		return Datum{Kind: KindInt, Int: int64(xmax)}, nil
	// txid_visible_in_snapshot(xid8, pg_snapshot) → bool. Mirrors
	// pg_visible_in_snapshot/is_visible_fxid (xid8funcs.c). M0134-0080
	// (pg_proc OID 2948).
	case "txid_visible_in_snapshot":
		if len(x.Args) != 2 {
			return NullDatum, nil
		}
		valD, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || valD.IsNull() {
			return NullDatum, err
		}
		snapD, err := evalExprSlot(x.Args[1], slot, ctx)
		if err != nil || snapD.IsNull() {
			return NullDatum, err
		}
		xmin, xmax, xips, ok := parsePgSnapshotParts(snapD.StringValue())
		if !ok {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type pg_snapshot: %q", snapD.StringValue())}
		}
		value := uint64(valD.Int)
		var visible bool
		switch {
		case value < xmin:
			visible = true
		case value >= xmax:
			visible = false
		default:
			visible = true
			for _, ip := range xips {
				if ip == value {
					visible = false
					break
				}
			}
		}
		return NewBoolDatum(visible), nil
	// txid_status(xid8) → text: 'in progress' / 'committed' / 'aborted', or
	// NULL for a too-old xid whose CLOG entry has been truncated (unmodelled
	// in goopg — see the M0134-0080 deferral ledger row), or ERROR for an xid
	// that has not been assigned yet. Mirrors pg_xact_status/
	// TransactionIdInRecentPast (xid8funcs.c). M0134-0080 (pg_proc OID 3360).
	case "txid_status":
		if len(x.Args) != 1 {
			return NullDatum, nil
		}
		argD, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil || argD.IsNull() {
			return NullDatum, err
		}
		xid := storage.TransactionID(argD.Int)
		if xid == storage.InvalidTransactionID {
			return NullDatum, nil
		}
		if xid >= transam.FirstNormalTransactionID && ctx.TxnMgr != nil && xid >= ctx.TxnMgr.NextXID() {
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("transaction ID %d is in the future", int64(xid))}
		}
		if ctx.TxnMgr == nil {
			return NullDatum, nil
		}
		switch ctx.TxnMgr.ClassifyXID(xid) {
		case transam.XidVisInProgress:
			return NewStringDatum("in progress"), nil
		case transam.XidVisCommitted:
			return NewStringDatum("committed"), nil
		case transam.XidVisAborted:
			return NewStringDatum("aborted"), nil
		default:
			return NullDatum, nil
		}
	case "clock_timestamp":
		// prorettype 1184 (timestamptz), like the now() family. M0119-0006.
		return NewTimestampTZDatum(ctx.Now), nil
	case "timeofday":
		return NewStringDatum(ctx.Now.Format("Mon Jan 02 15:04:05.000000 2006 UTC")), nil
	case "localtime":
		// Returns time-of-day anchored at epoch (same storage convention as current_time).
		// Accepts optional precision arg: localtime(N) rounds the fractional
		// seconds via roundTimeDatumToPrecision, matching the `::time(N)`
		// CastExpr path (AdjustTimeForTypmod ROUNDS, it does not truncate —
		// see the current_time case above, M0134-0084: the previous
		// floor-via-integer-division here is what made
		// `now()::time(N)::text = localtime(N)::text` (expressions.sql) flaky).
		t := ctx.Now.UTC()
		d := NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC))
		if len(x.Args) > 0 {
			prec, err := evalExprSlot(x.Args[0], slot, ctx)
			if err == nil && prec.Kind == KindInt {
				d = roundTimeDatumToPrecision(d, prec.Int)
			}
		}
		return d, nil
	case "localtimestamp":
		// M0134-0084: localtimestamp is the plain-`timestamp` sibling of now()
		// (prorettype 1083 vs now()'s 1184) and must equal `now()::timestamp`
		// — PG derives both from the same transaction-start instant converted
		// into the session TimeZone (timestamptz_timestamp, date.c). The
		// `::timestamp` CastExpr path already applies that conversion
		// (misc.TimestampTZToTimestamp, M0119-0006 40th slice); this bare
		// FuncCall previously skipped it and returned the raw UTC instant
		// relabelled as local wall clock, silently off by the zone's UTC
		// offset whenever TimeZone != UTC.
		return NewTimeDatum(misc.TimestampTZToTimestamp(ctx.Now, timeZoneFromCtx(ctx))), nil
	case "pg_is_in_recovery":
		return NewBoolDatum(ctx.IsStandby), nil
	case "pg_promote":
		// pg_promote(wait boolean DEFAULT true, wait_seconds integer DEFAULT 60)
		// Returns true if this server is a standby and promotion was triggered.
		// Returns false without error when not a standby (mirrors upstream).
		if ctx.Promote == nil {
			return NewBoolDatum(false), nil
		}
		if err := ctx.Promote(); err != nil {
			return Datum{}, &ExecError{
				Code:    "XX000",
				Pos:     x.Pos(),
				Message: "pg_promote: " + err.Error(),
			}
		}
		return NewBoolDatum(true), nil
	// SQL-callable replication-slot management (slotfuncs.c). Both were
	// already in pg_proc but had no executor arm, so the M0130-S10 E2E
	// harness got 42883 from `SELECT pg_create_physical_replication_slot(…)`
	// on a goopg primary. See expr_replslot.go.
	// M-NIGHTLY AI-20260810-011258-003.
	case "pg_create_physical_replication_slot":
		return evalPgCreatePhysicalReplicationSlot(x, slot, ctx)
	case "pg_drop_replication_slot":
		return evalPgDropReplicationSlot(x, slot, ctx)
	// currtid2(relname text, tid tid) → tid: returns the latest visible TID
	// for a row in the named relation. M0097-0038.
	case "currtid2":
		return evalCurrtid2(x, slot, ctx)
	// acldefault("char", oid) → aclitem[]: the hard-wired default access
	// privileges for a newly-created object of the given type. pg_dump's
	// getNamespaces/getTypes/getTables/... call it as the baseline to diff
	// against the stored *acl column. M0110-0001 / DU-002 slice 2.
	case "acldefault":
		return evalAclDefault(x, slot, ctx)
	// pg_get_userbyid(oid) → name: resolves a role OID to its name, or PG's
	// literal fallback "unknown (OID=n)" when no such role exists
	// (pg_get_userbyid, ruleutils.c). pg_dumpall's dumpRoleGUCPrivs calls it
	// as `pg_get_userbyid(10)` (BOOTSTRAP_SUPERUSERID) to resolve
	// pg_parameter_acl's implicit owner. M0119-0004-ACLHEAP (parameter ACL
	// half).
	case "pg_get_userbyid":
		if len(x.Args) != 1 {
			return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(),
				Message: "pg_get_userbyid(oid) requires exactly 1 argument"}
		}
		oidArg, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		if oidArg.IsNull() {
			return NullDatum, nil
		}
		if ctx == nil || ctx.Catalog == nil {
			return NullDatum, nil
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NullDatum, nil
		}
		return NewStringDatum(im.RoleNameForOIDOrUnknown(uint32(oidArg.Int))), nil
	// pg_stat_force_next_flush() → void: forces the next cumulative-statistics
	// flush in the current backend to proceed unconditionally (upstream skips a
	// flush if the rate-limit interval has not elapsed). goopg accumulates
	// function-call stats in per-session pending counters; this flushes them
	// into the shared, cluster-global store so the pg_stat_get_function_* getters
	// (and other sessions) observe them. The `stats` isolation spec calls it
	// between mutating and observing steps. Design 0118-0124 (M0118-0009 rung 2).
	case "pg_stat_force_next_flush":
		funcStats.flush(sessionStatsID(ctx))
		relStats.flush(sessionStatsID(ctx))
		return NewStringDatum(""), nil
	// pg_stat_get_function_calls(oid) → bigint: number of times the function has
	// been called (flushed), or NULL when no stats exist for it. Design 0118-0124.
	case "pg_stat_get_function_calls":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		if c, found := fetchFuncStat(ctx, oid); found {
			return NewIntDatum(c.calls), nil
		}
		return NullDatum, nil
	// pg_stat_get_xact_function_calls(oid) → bigint: number of times the
	// function has been called so far in the CURRENT open transaction (the
	// backend-local pending counters, not yet flushed), or NULL when the
	// session has not called it in this transaction. Unlike the shared-tier
	// getter above, this reads the pending tier directly (not fetchFuncStat's
	// stats_fetch_consistency snapshot) — PG's find_funcstat_entry always
	// reads the backend's own live pending state, which has no cross-session
	// consistency concern. PG: pgstatfuncs.c:1804. M0134-0020.
	case "pg_stat_get_xact_function_calls":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		if c, found := funcStats.peekPending(sessionStatsID(ctx), oid); found {
			return NewIntDatum(c.calls), nil
		}
		return NullDatum, nil
	// pg_stat_get_function_total_time(oid) → double precision: total wall time
	// spent in the function (and the functions it called), in milliseconds, or
	// NULL when no stats exist. Design 0118-0124.
	case "pg_stat_get_function_total_time":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		if c, found := fetchFuncStat(ctx, oid); found {
			return newNumericFromFloat(float64(c.totalTime.Nanoseconds()) / 1e6), nil
		}
		return NullDatum, nil
	// pg_stat_get_function_self_time(oid) → double precision: wall time spent in
	// the function itself excluding nested calls, in milliseconds, or NULL when
	// no stats exist. goopg does not separate nested time, so self == total
	// (the spec only checks > 0). Design 0118-0124.
	case "pg_stat_get_function_self_time":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		if c, found := fetchFuncStat(ctx, oid); found {
			return newNumericFromFloat(float64(c.selfTime.Nanoseconds()) / 1e6), nil
		}
		return NullDatum, nil
	// pg_stat_reset_single_function_counters(oid) → void: drop the cumulative
	// counters for one function. A non-existent OID is a silent no-op (matching
	// PG, which only resets a present shared entry). Design 0118-0124.
	case "pg_stat_reset_single_function_counters":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if ok {
			funcStats.resetSingle(oid)
		}
		return NewStringDatum(""), nil
	// pg_stat_reset() → void: reset all cumulative statistics for the current
	// database. goopg currently tracks only function stats, so this clears the
	// shared function-stats store. Design 0118-0124.
	case "pg_stat_reset":
		funcStats.resetAll()
		relStats.resetAll()
		return NewStringDatum(""), nil
	// pg_stat_clear_snapshot() → void: discards the current transaction's cached
	// statistics snapshot (used with stats_fetch_consistency = 'snapshot'/'cache')
	// so subsequent reads consult live shared values again. M0118-0009.
	case "pg_stat_clear_snapshot":
		if sess, ok := ctx.Session.(*BasicSession); ok && sess != nil {
			sess.ClearStatsSnapshot()
		}
		return NewStringDatum(""), nil
	// Relation (table) cumulative-stats getters. Unlike the function-stats
	// getters, these return 0 (not SQL NULL) for an OID with no flushed stats —
	// matching PG, where pg_stat_get_numscans of a dropped/never-touched relation
	// reads 0. Design 0118-0128 (M0118-0009 rung 6).
	case "pg_stat_get_numscans",
		"pg_stat_get_tuples_returned",
		"pg_stat_get_tuples_fetched",
		"pg_stat_get_tuples_inserted",
		"pg_stat_get_tuples_updated",
		"pg_stat_get_tuples_deleted",
		"pg_stat_get_live_tuples",
		"pg_stat_get_dead_tuples",
		"pg_stat_get_vacuum_count":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		c, _ := relStats.get(oid)
		var v int64
		switch name {
		case "pg_stat_get_numscans":
			v = c.numScans
		case "pg_stat_get_tuples_returned":
			v = c.tuplesReturned
		case "pg_stat_get_tuples_fetched":
			// Index-scan fetched tuples are not yet tracked (no index-scan
			// counting rung); report 0 as PG does before any index scan.
			v = 0
		case "pg_stat_get_tuples_inserted":
			v = c.tuplesInserted
		case "pg_stat_get_tuples_updated":
			v = c.tuplesUpdated
		case "pg_stat_get_tuples_deleted":
			v = c.tuplesDeleted
		case "pg_stat_get_live_tuples":
			v = c.deltaLive
			if v < 0 {
				v = 0 // PG clamps live-tuple estimate to non-negative
			}
		case "pg_stat_get_dead_tuples":
			v = c.deltaDead
			if v < 0 {
				v = 0
			}
		case "pg_stat_get_vacuum_count":
			// No VACUUM-driven relation stats yet; PG reads 0 until first vacuum.
			v = 0
		}
		return NewIntDatum(v), nil
	// pg_stat_get_xact_tuples_inserted(oid) → bigint: rows inserted into the
	// relation so far in the CURRENT open transaction (the per-transaction
	// staging tier, not yet folded into pending at commit/abort) — visible
	// BEFORE COMMIT, unlike the shared-tier getter above. An OID the session
	// has not written in this transaction reads 0, never NULL — the found-bool
	// is deliberately discarded, matching PG's PG_STAT_GET_XACT_RELENTRY_INT64
	// macro (find_tabstat_entry == NULL → result = 0). PG: pgstatfuncs.c:1758,
	// instantiated :1796. M0134-0020.
	case "pg_stat_get_xact_tuples_inserted":
		oid, ok, err := statFuncOIDArg(x, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !ok {
			return NullDatum, nil
		}
		c, _ := relStats.peekStaging(sessionStatsID(ctx), oid)
		return NewIntDatum(c.tuplesInserted), nil
	}

	// Function-style type casts: int4(x), float8(x), text(x), etc.
	// PostgreSQL allows type names as function names for casting. M0097-0003.
	if len(x.Args) == 1 {
		typeName := name
		switch typeName {
		case "int2", "smallint",
			"int4", "integer", "int",
			"int8", "bigint",
			"float4", "real",
			"float8", "double precision",
			"numeric", "decimal",
			"text", "varchar", "bpchar", "char",
			"bool", "boolean",
			"oid", "date", "timestamp", "timestamptz",
			"time", "timetz", "interval":
			v, err := evalExprSlot(x.Args[0], slot, ctx)
			if err != nil {
				return Datum{}, err
			}
			if v.IsNull() {
				return NullDatum, nil
			}
			return evalCast(v, typeName, x.Pos(), ctx)
		}
	}

	return evalStoredRoutineFuncCall(x, slot, ctx)
}

// ACL privilege bits, mirroring src/include/nodes/parsenodes.h. Used by
// evalAclDefault to compute hard-wired default privileges.
const (
	aclInsert      = 1 << 0  // 'a'
	aclSelect      = 1 << 1  // 'r'
	aclUpdate      = 1 << 2  // 'w'
	aclDelete      = 1 << 3  // 'd'
	aclTruncate    = 1 << 4  // 'D'
	aclReferences  = 1 << 5  // 'x'
	aclTrigger     = 1 << 6  // 't'
	aclExecute     = 1 << 7  // 'X'
	aclUsage       = 1 << 8  // 'U'
	aclCreate      = 1 << 9  // 'C'
	aclCreateTemp  = 1 << 10 // 'T'
	aclConnect     = 1 << 11 // 'c'
	aclSet         = 1 << 12 // 's'
	aclAlterSystem = 1 << 13 // 'A'
	aclMaintain    = 1 << 14 // 'm'
)

// aclPrivString renders a privilege bitmask to its canonical letter string,
// emitting letters in PostgreSQL's fixed ACL_ALL_RIGHTS_STR order
// ("arwdDxtXUCTcsAm", src/include/utils/acl.h). e.g. USAGE|CREATE → "UC".
func aclPrivString(mask int) string {
	const order = "arwdDxtXUCTcsAm"
	bits := [...]int{
		aclInsert, aclSelect, aclUpdate, aclDelete, aclTruncate,
		aclReferences, aclTrigger, aclExecute, aclUsage, aclCreate,
		aclCreateTemp, aclConnect, aclSet, aclAlterSystem, aclMaintain,
	}
	var b strings.Builder
	for i, bit := range bits {
		if mask&bit != 0 {
			b.WriteByte(order[i])
		}
	}
	return b.String()
}

// aclRoleNameForOID resolves a role OID to the name aclitemout would print.
// goopg has the single bootstrap superuser "postgres" (OID 10,
// BOOTSTRAP_SUPERUSERID); any other OID falls back to its numeric form, which
// is what PostgreSQL emits for a since-dropped role.
func aclRoleNameForOID(oid int64) string {
	if oid == 10 {
		return "postgres"
	}
	return strconv.FormatInt(oid, 10)
}

// evalAclDefault implements acldefault("char" objtype, oid ownerId) → aclitem[].
// It mirrors acldefault()/acldefault_sql() in src/backend/utils/adt/acl.c: it
// computes the hard-wired default access privileges for a newly-created object
// of the given type and renders them as the text form of an aclitem[] array,
// e.g. acldefault('n', 10) → "{postgres=UC/postgres}". The world (PUBLIC) entry
// is emitted first, then the owner entry, each only when non-empty. NULL input
// yields NULL. M0110-0001 / DU-002 slice 2.
func evalAclDefault(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: "acldefault(\"char\", oid) requires exactly 2 arguments"}
	}
	typeArg, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	ownerArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	if typeArg.IsNull() || ownerArg.IsNull() {
		return NullDatum, nil
	}
	objtype := typeArg.Format()
	if objtype == "" {
		return NullDatum, &ExecError{Code: "22P02", Pos: x.Pos(),
			Message: "invalid input syntax for type \"char\""}
	}

	var worldDefault, ownerDefault int
	switch objtype[0] {
	case 'c': // OBJECT_COLUMN — no extra privileges
	case 'r': // OBJECT_TABLE
		ownerDefault = aclInsert | aclSelect | aclUpdate | aclDelete |
			aclTruncate | aclReferences | aclTrigger | aclMaintain
	case 's': // OBJECT_SEQUENCE
		ownerDefault = aclUsage | aclSelect | aclUpdate
	case 'd': // OBJECT_DATABASE
		worldDefault = aclCreateTemp | aclConnect
		ownerDefault = aclCreate | aclCreateTemp | aclConnect
	case 'f': // OBJECT_FUNCTION
		worldDefault = aclExecute
		ownerDefault = aclExecute
	case 'l': // OBJECT_LANGUAGE
		worldDefault = aclUsage
		ownerDefault = aclUsage
	case 'L': // OBJECT_LARGEOBJECT
		ownerDefault = aclSelect | aclUpdate
	case 'n': // OBJECT_SCHEMA
		ownerDefault = aclUsage | aclCreate
	case 't': // OBJECT_TABLESPACE
		ownerDefault = aclCreate
	case 'F': // OBJECT_FDW
		ownerDefault = aclUsage
	case 'S': // OBJECT_FOREIGN_SERVER
		ownerDefault = aclUsage
	case 'T': // OBJECT_TYPE / OBJECT_DOMAIN
		worldDefault = aclUsage
		ownerDefault = aclUsage
	case 'p': // OBJECT_PARAMETER_ACL
		ownerDefault = aclSet | aclAlterSystem
	default:
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: fmt.Sprintf("unrecognized object type abbreviation: %c", objtype[0])}
	}

	ownerName := aclRoleNameForOID(ownerArg.Int)
	var items []string
	if worldDefault != 0 {
		items = append(items, "="+aclPrivString(worldDefault)+"/"+ownerName)
	}
	if ownerDefault != 0 {
		items = append(items, ownerName+"="+aclPrivString(ownerDefault)+"/"+ownerName)
	}
	return NewStringDatum("{" + strings.Join(items, ",") + "}"), nil
}

// evalCurrtid2 implements currtid2(relname text, tid tid) → tid.
// Returns the latest visible TID for the named relation, or an error for
// unsupported relation kinds (indexes, partitioned tables, views without ctid).
// M0097-0038.
func evalCurrtid2(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: fmt.Sprintf("function currtid2(unknown, unknown) does not exist")}
	}
	nameD, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	if nameD.IsNull() {
		return NullDatum, nil
	}
	relname := strings.TrimSpace(nameD.StringValue())

	tidD, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	if tidD.IsNull() {
		return NullDatum, nil
	}
	tidStr := tidD.StringValue()
	block, offset, ok := parseTidInput(tidStr)
	if !ok {
		return NullDatum, &ExecError{Code: "22P02", Pos: x.Pos(),
			Message: fmt.Sprintf("invalid input syntax for type tid: %q", tidStr)}
	}

	// Sequence: in-memory only; treat TID as always valid. M0097-0038.
	if LookupSequence(relname, ctxSeqDBOid(ctx)) != nil {
		return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
	}

	if ctx.Catalog == nil {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: "currtid2 requires a catalog"}
	}

	// Index: not supported.
	if _, isIdx := ctx.Catalog.LookupIndex(parser.ObjectName{Name: relname}); isIdx {
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: fmt.Sprintf("cannot open relation %q", relname),
			Detail:  "This operation is not supported for indexes."}
	}

	tbl, found := ctx.Catalog.LookupTable(parser.ObjectName{Name: relname})
	if !found {
		return NullDatum, &ExecError{Code: "42P01", Pos: x.Pos(),
			Message: fmt.Sprintf("relation %q does not exist", relname)}
	}

	// Partitioned table: no storage.
	if len(tbl.PartitionKey) > 0 {
		schema := tbl.Schema
		if schema == "" {
			schema = "public"
		}
		qualName := schema + "." + tbl.Name
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: fmt.Sprintf("cannot look at latest visible tid for relation %q", qualName)}
	}

	// View (non-matview): inspect for ctid column, resolve to base table.
	if tbl.View != nil && !tbl.IsMatView {
		return currtid2ViewCheck(tbl, block, offset, x.Pos(), ctx)
	}

	// Heap table or matview: check TID validity in storage.
	return currtid2TIDCheck(tbl.Name, tbl, block, offset, x.Pos(), ctx)
}

// currtid2ViewCheck handles currtid2 for a SQL view. Checks that the view
// has a ctid column of type tid, then delegates TID validity to the base table.
func currtid2ViewCheck(viewTbl *catalog.Table, block uint32, offset uint16, pos int, ctx *Context) (Datum, error) {
	var ctidTypeName string
	for _, c := range viewTbl.Columns {
		if strings.EqualFold(c.Name, "ctid") {
			ctidTypeName = strings.ToLower(c.Type.Name)
			break
		}
	}
	if ctidTypeName == "" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	if ctidTypeName != "tid" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "ctid isn't of type TID"}
	}
	// Resolve base table from view's FROM clause.
	if viewTbl.View == nil || len(viewTbl.View.From) == 0 {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	baseTableName := viewTbl.View.From[0].Name
	if baseTableName == "" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	baseTbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: baseTableName})
	if !ok {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	return currtid2TIDCheck(baseTbl.Name, baseTbl, block, offset, pos, ctx)
}

// currtid2TIDCheck verifies that (block, offset) is a valid address in tbl's
// heap storage and returns the tid on success.
func currtid2TIDCheck(relname string, tbl *catalog.Table, block uint32, offset uint16, pos int, ctx *Context) (Datum, error) {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil || uint32(nBlocks) <= block {
		return NullDatum, &ExecError{Code: "22000", Pos: pos,
			Message: fmt.Sprintf("tid (%d, %d) is not valid for relation %q", block, offset, relname)}
	}
	return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
}

// initCap returns s with the first letter of each word capitalized. M0097-0005.
func initCap(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			for j := 1; j < len(runes); j++ {
				runes[j] = unicode.ToLower(runes[j])
			}
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

// padLeft left-pads s to length n using the fill string. M0097-0005.
// PG clamps a negative target length to 0 before any buffer sizing
// (postgres/src/backend/utils/adt/varlena.c text_lpad), returning ''.
// M0134-0070.
func padLeft(s string, n int, fill string) string {
	if n < 0 {
		n = 0
	}
	runes := []rune(s)
	if len(runes) >= n {
		return string(runes[:n])
	}
	if fill == "" {
		return s
	}
	fillRunes := []rune(fill)
	var buf []rune
	for len(buf)+len(runes) < n {
		for _, r := range fillRunes {
			if len(buf)+len(runes) >= n {
				break
			}
			buf = append(buf, r)
		}
	}
	return string(buf) + string(runes)
}

// padRight right-pads s to length n using the fill string. M0097-0005.
// PG clamps a negative target length to 0 before any buffer sizing
// (postgres/src/backend/utils/adt/varlena.c text_rpad), returning ''.
// M0134-0070.
func padRight(s string, n int, fill string) string {
	if n < 0 {
		n = 0
	}
	runes := []rune(s)
	if len(runes) >= n {
		return string(runes[:n])
	}
	if fill == "" {
		return s
	}
	fillRunes := []rune(fill)
	result := make([]rune, len(runes), n)
	copy(result, runes)
	for len(result) < n {
		for _, r := range fillRunes {
			if len(result) >= n {
				break
			}
			result = append(result, r)
		}
	}
	return string(result)
}

// translateStr replaces each character in s that appears in from with the
// corresponding character in to. Characters in from without a corresponding
// to-character are deleted. M0097-0005.
func translateStr(s, from, to string) string {
	fromRunes := []rune(from)
	toRunes := []rune(to)
	var buf strings.Builder
	for _, r := range s {
		replaced := false
		for i, fr := range fromRunes {
			if r == fr {
				if i < len(toRunes) {
					buf.WriteRune(toRunes[i])
				}
				// else: delete the character
				replaced = true
				break
			}
		}
		if !replaced {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// parseNumericOrZero wraps parseNumeric with a zero fallback. M0097-0005.
func parseNumericOrZero(s string) *big.Int {
	m, _, err := parseNumeric(s)
	if err != nil {
		return new(big.Int)
	}
	return m
}

// evalAdvisoryLock implements the blocking and non-blocking advisory-lock
// acquisition variants.
//
//   - tryOnly=true : non-blocking (pg_try_advisory_*); returns true/false.
//   - tryOnly=false: blocking (pg_advisory_lock, pg_advisory_xact_lock);
//     blocks until the lock is acquired or ctx is cancelled.
//   - shared=true  : ShareLock mode (pg_advisory_*_shared variants).
//   - shared=false : ExclusiveLock mode.
//
// Argument forms:
//
//	(bigint)        → key = bigint, twoArg=false
//	(int4, int4)    → key = (classid, objid), twoArg=true
func evalAdvisoryLock(x *optimizer.FuncCall, slot SlotView, ctx *Context, tryOnly bool, xactScoped bool, shared bool) (Datum, error) {
	sess := advisorySessionIDFromContext(ctx)

	var key advisoryKey
	var twoArg bool
	switch len(x.Args) {
	case 1:
		v, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		n, ok := datumInt64(v)
		if !ok {
			return NullDatum, nil
		}
		key = bigintToKey(n)
		twoArg = false
	case 2:
		v0, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		v1, err2 := evalExprSlot(x.Args[1], slot, ctx)
		if err2 != nil {
			return NullDatum, err2
		}
		n0, _ := datumInt64(v0)
		n1, _ := datumInt64(v1)
		key = int4ToKey(int32(n0), int32(n1))
		twoArg = true
	default:
		return NullDatum, nil
	}

	if tryOnly {
		ok := globalAdvisoryMgr.tryAcquire(key, sess, xactScoped, shared, twoArg)
		return NewBoolDatum(ok), nil
	}

	// Blocking acquire — respects ctx cancellation.
	qctx := ctx.Ctx
	if qctx == nil {
		qctx = context.Background()
	}
	if err := globalAdvisoryMgr.acquire(qctx, key, sess, xactScoped, shared, twoArg); err != nil {
		// Context cancelled (step timed out or runner aborted).
		return NullDatum, nil
	}
	// Return a non-NULL void-like empty string (PostgreSQL advisory lock functions
	// return void; non-NULL so `IS NOT NULL` in WHERE clauses is true).
	return NewStringDatum(""), nil
}

// evalAdvisoryUnlock implements pg_advisory_unlock(bigint), pg_advisory_unlock(int4,int4),
// pg_advisory_unlock_shared(bigint), and pg_advisory_unlock_shared(int4,int4).
// Returns true if the lock was held by this session and has been released, false otherwise.
// Emits WARNING "you don't own a lock of type <mode>" when returning false. M0097-0021.
func evalAdvisoryUnlock(x *optimizer.FuncCall, slot SlotView, ctx *Context, shared bool) (Datum, error) {
	sess := advisorySessionIDFromContext(ctx)

	var key advisoryKey
	switch len(x.Args) {
	case 1:
		v, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		n, _ := datumInt64(v)
		key = bigintToKey(n)
	case 2:
		v0, err := evalExprSlot(x.Args[0], slot, ctx)
		if err != nil {
			return NullDatum, err
		}
		v1, err2 := evalExprSlot(x.Args[1], slot, ctx)
		if err2 != nil {
			return NullDatum, err2
		}
		n0, _ := datumInt64(v0)
		n1, _ := datumInt64(v1)
		key = int4ToKey(int32(n0), int32(n1))
	default:
		return NewBoolDatum(false), nil
	}

	ok := globalAdvisoryMgr.release(key, sess)
	if !ok {
		lockType := "ExclusiveLock"
		if shared {
			lockType = "ShareLock"
		}
		if ctx != nil {
			ctx.AddWarning("you don't own a lock of type " + lockType)
		}
	}
	return NewBoolDatum(ok), nil
}

// evalAdvisoryUnlockAll implements pg_advisory_unlock_all(). Releases every
// session-scoped advisory lock held by this session and returns NULL (void-like).
func evalAdvisoryUnlockAll(ctx *Context) (Datum, error) {
	if ctx == nil {
		return NullDatum, nil
	}
	globalAdvisoryMgr.releaseAllSession(advisorySessionIDFromContext(ctx))
	return NullDatum, nil
}

// datumInt64 extracts an integer value from a Datum. Returns (0, false) if the
// Datum is not an integer-compatible type.
func datumInt64(d Datum) (int64, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, true
	case KindString:
		// Some callers pass string representations of integers.
		n, err := strconv.ParseInt(strings.TrimSpace(d.StringValue()), 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// evalToDate implements PostgreSQL's `to_date(text, text)` for the
// format codes HammerDB TPC-H Q15 uses (`YYYY-MM-DD`). It reuses
// `pgFormatToGoLayout` from to_timestamp and truncates the result
// to midnight UTC so the value behaves like a DATE rather than a
// timestamp. Real upstream parity (timezone, era handling, locale
// month names) waits on the type system; this is scoped to "make
// Q15 plan and run without rejecting the conversion".
func evalToDate(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_date(text, text) requires exactly 2 arguments"}
	}
	src, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	fmtArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fmtArg.IsNull() {
		return NullDatum, nil
	}
	if (src.Kind != KindString) || (fmtArg.Kind != KindString) {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_date arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.StringValue())
	t, perr := time.Parse(goLayout, src.StringValue())
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_date: %v (format=%q value=%q)", perr, fmtArg.StringValue(), src.StringValue())}
	}
	year, month, day := t.UTC().Date()
	out := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	d := NewTimeDatum(out)
	d.TimeSub = TimeSubDate // mark as DATE type for Postgres MDY display. M0097-0063.
	return d, nil
}

// evalSubstr implements PostgreSQL's `substr(string, from [, count])`
// (alias `substring`) using 1-based byte indexing — matches upstream's
// `text_substr` semantics for ASCII/single-byte text. HammerDB TPC-H
// Q22 uses `substr(c_phone, 1, 2)` to extract the country code prefix.
// NULL inputs propagate to NULL output.
//
// The 2-argument form returns the substring from `from` to the end of
// evalPgSleep implements pg_sleep(seconds). Sleeps for the given
// duration while honouring query cancellation via ctx.Ctx.
func evalPgSleep(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep(double precision) requires exactly 1 argument"}
	}
	secs, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if secs.IsNull() {
		return NullDatum, nil
	}
	var d time.Duration
	switch secs.Kind {
	case KindInt:
		d = time.Duration(secs.Int) * time.Second
	case KindNumeric:
		f, err := strconv.ParseFloat(secs.Format(), 64)
		if err != nil {
			return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep: invalid numeric value"}
		}
		d = time.Duration(f * float64(time.Second))
	default:
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep argument must be numeric"}
	}
	if d < 0 {
		d = 0
	}
	if ctx.Ctx != nil {
		select {
		case <-time.After(d):
		case <-ctx.Ctx.Done():
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	} else {
		time.Sleep(d)
	}
	return NullDatum, nil
}

// the string. Negative `from` values are clamped per upstream:
// `substr('abcdef', -2, 4)` returns `'a'` (start at position 1, length
// becomes 1 after subtracting the negative offset). For a v0 simple
// implementation we follow the spec exactly.
func evalSubstr(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 && len(x.Args) != 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr requires 2 or 3 arguments"}
	}
	src, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	fromArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fromArg.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindString && src.Kind != KindBytes {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr first argument must be text"}
	}
	if fromArg.Kind != KindInt {
		// SQL-standard regex form: SUBSTRING(str FROM pattern) with a TEXT
		// pattern rather than an integer start position. PG resolves this via
		// overload resolution on the static argument type (int4 vs text); goopg's
		// eval-time Kind check plays the same role. Only the 2-arg FROM-only form
		// is valid here — a 3-arg call with a text pattern isn't a real PG form
		// (PG's grammar has no FROM-pattern-FOR-count overload), so it falls
		// through to the same error as today. M0134-0061.
		if len(x.Args) == 2 && fromArg.Kind == KindString {
			if src.Kind != KindString {
				return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr first argument must be text"}
			}
			return evalSubstrRegex(src.StringValue(), fromArg.StringValue(), x.Pos())
		}
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr second argument must be integer"}
	}
	// bytea_substr (varlena.c) slices BYTES and returns bytea. The window
	// arithmetic below is already byte-indexed, so the only difference is the
	// Kind of the result — without it `substring(<bytea> from 2 for 1)` would
	// hand back a text datum that the wire renderer hex-dumps as garbage.
	// M0125-0021.
	mkResult := NewStringDatum
	if src.Kind == KindBytes {
		mkResult = func(v string) Datum { return NewBytesDatum([]byte(v)) }
	}
	s := src.StringValue()
	from := fromArg.Int
	// Upstream's text_substring: 1-based start, treat values <=0 as
	// shifting the window left of the string. With no length, the
	// window is open-ended on the right, so a non-positive `from`
	// just clamps to position 1.
	if len(x.Args) == 2 {
		if from < 1 {
			from = 1
		}
		idx := int(from) - 1
		if idx >= len(s) {
			return mkResult(""), nil
		}
		return mkResult(s[idx:]), nil
	}
	cntArg, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if cntArg.IsNull() {
		return NullDatum, nil
	}
	if cntArg.Kind != KindInt {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr third argument must be integer"}
	}
	count := cntArg.Int
	if count < 0 {
		// No Pos: pure runtime evaluation (text_substring,
		// postgres/src/backend/utils/adt/varlena.c, has no errposition
		// call). M0134-0070.
		return Datum{}, &ExecError{Code: "22011", Message: "negative substring length not allowed"}
	}
	end := from + count
	if from < 1 {
		from = 1
	}
	startIdx := int(from) - 1
	endIdx := int(end) - 1
	if endIdx < startIdx {
		endIdx = startIdx
	}
	if startIdx >= len(s) {
		return mkResult(""), nil
	}
	if endIdx > len(s) {
		endIdx = len(s)
	}
	return mkResult(s[startIdx:endIdx]), nil
}


// evalOverlay implements the SQL-standard
// OVERLAY(str PLACING replacement FROM start [FOR count]) function,
// desugared by the parser to overlay(str, replacement, start[, count]).
// Mirrors evalSubstr's byte-indexed simplification (no multibyte
// correctness) and text/bytea Kind-branch structure. M0134-0070.
// PG oracle: postgres/src/backend/utils/adt/varlena.c text_overlay
// (~line 1167) and bytea_overlay (~line 3221) — same algorithm/errors.
func evalOverlay(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 3 && len(x.Args) != 4 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "overlay requires 3 or 4 arguments"}
	}
	src, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	repl, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	startArg, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || repl.IsNull() || startArg.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindString && src.Kind != KindBytes {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "overlay first argument must be text"}
	}
	if repl.Kind != src.Kind {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "overlay second argument must match first argument type"}
	}
	if startArg.Kind != KindInt {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "overlay third argument must be integer"}
	}

	// bytea_overlay (varlena.c) slices BYTES and returns bytea — same split
	// as evalSubstr's text/bytea Kind branch above. M0134-0070.
	mkResult := NewStringDatum
	if src.Kind == KindBytes {
		mkResult = func(v string) Datum { return NewBytesDatum([]byte(v)) }
	}

	s := src.StringValue()
	r := repl.StringValue()
	sp := startArg.Int
	if sp <= 0 {
		// text_overlay/bytea_overlay: sp<=0 raises this exact wording under
		// ERRCODE_SUBSTRING_ERROR (22011) — same constant/message evalSubstr
		// already uses for its negative-length check above. No Pos: pure
		// runtime evaluation (text_overlay has no errposition call).
		return Datum{}, &ExecError{Code: "22011", Message: "negative substring length not allowed"}
	}

	sl := int64(len(r))
	if len(x.Args) == 4 {
		cntArg, err := evalExprSlot(x.Args[3], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if cntArg.IsNull() {
			return NullDatum, nil
		}
		if cntArg.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "overlay fourth argument must be integer"}
		}
		sl = cntArg.Int
	}

	// result = substring(s, 1, sp-1) + r + substring(s, sp+sl, <rest>).
	// Mirrors text_overlay's two text_substring calls (varlena.c).
	part1End := int(sp) - 1
	if part1End > len(s) {
		part1End = len(s)
	}
	part1 := s[:part1End]

	tailStart := sp + sl
	if tailStart < 1 {
		tailStart = 1
	}
	tailIdx := int(tailStart) - 1
	var part2 string
	if tailIdx < len(s) {
		part2 = s[tailIdx:]
	}

	return mkResult(part1 + r + part2), nil
}

// evalSubstrRegex implements the SQL-standard regex form of SUBSTRING —
// `SUBSTRING(str FROM pattern)` where pattern is POSIX-regex text, not an
// integer start position. Mirrors upstream's `textregexsubstr`
// (postgres/src/backend/utils/adt/regexp.c:583-627): compile the pattern
// (REG_ADVANCED upstream; here via the shared pgPatternToGoRE2 + regexp.Compile
// path already used by evalPOSIXRegex and friends), execute with nmatch=2 (Go:
// FindStringSubmatchIndex). If the pattern has a parenthesized subexpression
// (re_nsub > 0), the result is ALWAYS taken from that subexpression's match —
// even when it didn't participate in an otherwise-successful overall match
// (upstream's own comment: `'foo(bar)?'` matches `'foo'` overall but the
// subexpression doesn't participate, so `so < 0 || eo < 0` and the function
// returns NULL, not the whole match). Only when the pattern has NO
// parenthesized subexpression at all does the whole match get returned.
// M0134-0061.
func evalSubstrRegex(src, pattern string, pos int) (Datum, error) {
	goPattern := pgPatternToGoRE2(pattern)
	re, err := regexp.Compile(goPattern)
	if err != nil {
		// No Pos: pure runtime evaluation (textregexsubstr,
		// postgres/src/backend/utils/adt/regexp.c, has no errposition
		// call). M0134-0070.
		return Datum{}, &ExecError{Code: "2201B", Message: fmt.Sprintf("invalid regular expression: %v", err)}
	}
	loc := re.FindStringSubmatchIndex(src)
	if loc == nil {
		// Overall match failed: upstream's RE_execute returns no match → NULL.
		return NullDatum, nil
	}
	// loc[0],loc[1] = whole match; loc[2],loc[3] = first subexpression (if any).
	var so, eo int
	if len(loc) >= 4 {
		so, eo = loc[2], loc[3]
	} else {
		so, eo = loc[0], loc[1]
	}
	if so < 0 || eo < 0 {
		return NullDatum, nil
	}
	return NewStringDatum(src[so:eo]), nil
}

// evalToTimestamp implements PostgreSQL's `to_timestamp(text,
// text)` for the format specifiers HammerDB's TPC-H loader uses
// (`YYYY`, `Mon`, `MM`, `DD`, plus optional time-of-day pieces).
// Real upstream parity (timezone handling, locale-aware month
// names, fractional seconds) waits on the type system; this is
// deliberately scoped to "make the loader work without rejecting
// rows".
func evalToTimestamp(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_timestamp(text, text) requires exactly 2 arguments"}
	}
	src, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	fmtArg, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fmtArg.IsNull() {
		return NullDatum, nil
	}
	if (src.Kind != KindString) || (fmtArg.Kind != KindString) {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_timestamp arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.StringValue())
	t, perr := time.Parse(goLayout, src.StringValue())
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_timestamp: %v (format=%q value=%q)", perr, fmtArg.StringValue(), src.StringValue())}
	}
	return NewTimeDatum(t.UTC()), nil
}

// pgFormatToGoLayout translates a v0 subset of upstream PG's
// to_timestamp() format codes into a Go time-package layout.
// Codes are matched longest-first inside a left-to-right scan;
// any character that isn't a recognised code passes through as a
// literal separator. Unknown codes are kept verbatim — Go's
// time.Parse will error if they don't match the input.
func pgFormatToGoLayout(s string) string {
	codes := []struct {
		from, to string
	}{
		{"YYYY", "2006"},
		{"YY", "06"},
		{"MON", "Jan"},
		{"Mon", "Jan"},
		{"MM", "01"},
		{"DD", "02"},
		{"HH24", "15"},
		{"HH", "03"},
		{"MI", "04"},
		{"SS", "05"},
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		matched := false
		for _, c := range codes {
			if i+len(c.from) <= len(s) && s[i:i+len(c.from)] == c.from {
				b.WriteString(c.to)
				i += len(c.from)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// subqueryCacheKey builds a deterministic string key from an
// outer row so correlated subquery results can be cached per
// distinct correlation value.
func subqueryCacheKey(row Row) string {
	parts := make([]string, len(row))
	for i, d := range row {
		parts[i] = datumKey(d)
	}
	return strings.Join(parts, "|")
}

// nonCorrelatedCacheKey returns a constant cache key for a given
// non-correlated subquery node. The pointer-derived suffix keeps
// keys for distinct subquery sites distinct within a single query
// (so two unrelated non-correlated SubPlans don't share a cached
// result), while collapsing all outer rows for the same SubPlan
// onto a single entry. (M0058-0001.)
func nonCorrelatedCacheKey(x interface{}) string {
	return fmt.Sprintf("\x00nc:%p", x)
}

// isValidBoolInput reports whether v is a valid PostgreSQL boolean literal.
// Used by pg_input_is_valid('...', 'bool'). Mirrors evalTypedStringLit bool case.
// pgInputIsValidTypedLen checks validity for varchar(N)/char(N)/character varying(N)
// type strings as used in pg_input_is_valid. Returns (valid, handled). M0097-0003.
func pgInputIsValidTypedLen(v, typStr string) (bool, bool) {
	// Match "varchar(N)", "character varying(N)", "char(N)" etc.
	var base string
	var n int
	for _, pfx := range []string{"character varying(", "varchar(", "character(", "char(", "bpchar("} {
		if strings.HasPrefix(typStr, pfx) && strings.HasSuffix(typStr, ")") {
			mid := typStr[len(pfx) : len(typStr)-1]
			if parsed, err := strconv.Atoi(mid); err == nil && parsed > 0 {
				base = pfx[:len(pfx)-1]
				n = parsed
				break
			}
		}
	}
	if n == 0 {
		return false, false
	}
	// PostgreSQL's input functions check raw length (NO trailing space stripping).
	if strings.Contains(base, "char") && !strings.Contains(base, "varying") {
		// char(N): fixed-width; check stripped length
		stripped := strings.TrimRight(v, " ")
		return len(stripped) <= n, true
	}
	// varchar(N): raw length check (varcharin does not strip trailing spaces).
	return len(v) <= n, true
}

// parseIdentString parses a qualified SQL identifier string (like "schema.table")
// into its components. Returns (components, errMsg, detail). If errMsg != "", an error occurred.
// Matches PostgreSQL's parse_ident() behavior. M0097-0003.
func parseIdentString(input string, strict bool) ([]string, string, string) {
	orig := input
	i := 0
	n := len(input)
	var components []string
	// PostgreSQL uses "string" (double-quoted, raw bytes) not Go %q (escape codes). M0097-0003.
	errMsg := func(detail string) ([]string, string, string) {
		return nil, `string is not a valid identifier: "` + orig + `"`, detail
	}

	for {
		// Skip leading whitespace.
		for i < n && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
			i++
		}
		if i >= n {
			if len(components) == 0 {
				return errMsg("")
			}
			// After the last dot, empty → error. M0097-0003.
			if len(components) > 0 && strict {
				return errMsg(`No valid identifier after ".".`)
			}
			break
		}
		if input[i] == '"' {
			// Quoted identifier: find matching unescaped '"'.
			i++ // skip opening quote
			var sb strings.Builder
			for i < n {
				if input[i] == '"' {
					if i+1 < n && input[i+1] == '"' {
						sb.WriteByte('"')
						i += 2
					} else {
						i++ // skip closing quote
						break
					}
				} else {
					sb.WriteByte(input[i])
					i++
				}
			}
			components = append(components, sb.String())
		} else {
			// Unquoted identifier: must start with letter or underscore.
			if !isIdentStartByte(input[i]) {
				if !strict {
					break
				}
				// Distinguish "before dot" vs "after dot" vs no-dot. M0097-0003.
				if input[i] == '.' {
					// Dot at start of component → nothing valid before this dot.
					return errMsg(`No valid identifier before ".".`)
				}
				if len(components) > 0 {
					// We consumed a dot and the next component is invalid.
					return errMsg(`No valid identifier after ".".`)
				}
				// No dot involved; just an invalid starting character.
				return errMsg("")
			}
			start := i
			for i < n && isIdentContByte(input[i]) {
				i++
			}
			ident := strings.ToLower(input[start:i])
			components = append(components, ident)
		}
		// Skip trailing whitespace.
		for i < n && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
			i++
		}
		if i >= n {
			break // end of string, done
		}
		if input[i] == '.' {
			i++ // consume dot, continue to next component
		} else {
			// Trailing garbage.
			if strict {
				return errMsg("")
			}
			break
		}
	}
	if len(components) == 0 {
		return errMsg("")
	}
	return components, "", ""
}

func isIdentStartByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 128
}

func isIdentContByte(c byte) bool {
	return isIdentStartByte(c) || (c >= '0' && c <= '9') || c == '$'
}

// formatTextArray formats a string slice as a PostgreSQL text array literal:
// {elem1,"elem with spaces",...}. Elements needing quoting get double-quoted. M0097-0003.
func formatTextArray(elems []string) string {
	if len(elems) == 0 {
		return "{}"
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			sb.WriteByte(',')
		}
		// Quote the element if it contains special chars, spaces, commas, braces, or backslashes.
		needsQuote := len(e) == 0
		if !needsQuote {
			for _, c := range e {
				if c == '"' || c == ',' || c == '{' || c == '}' || c == '\\' || c == ' ' || c == '\t' {
					needsQuote = true
					break
				}
			}
		}
		if needsQuote {
			sb.WriteByte('"')
			for _, c := range e {
				if c == '"' || c == '\\' {
					sb.WriteByte('\\')
				}
				sb.WriteRune(c)
			}
			sb.WriteByte('"')
		} else {
			sb.WriteString(e)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

// formatTextArrayWithNulls renders a PostgreSQL text-array literal where
// some elements may be NULL. NULL elements are rendered as the unquoted
// token NULL (PostgreSQL array literal syntax: {1,NULL,3}).
func formatTextArrayWithNulls(elems []string, nulls []bool) string {
	if len(elems) == 0 {
		return "{}"
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			sb.WriteByte(',')
		}
		if i < len(nulls) && nulls[i] {
			sb.WriteString("NULL")
			continue
		}
		// Quote the element if it contains special chars, spaces, commas, braces, or backslashes.
		needsQuote := len(e) == 0
		if !needsQuote {
			for _, c := range e {
				if c == '"' || c == ',' || c == '{' || c == '}' || c == '\\' || c == ' ' || c == '\t' {
					needsQuote = true
					break
				}
			}
		}
		if needsQuote {
			sb.WriteByte('"')
			for _, c := range e {
				if c == '"' || c == '\\' {
					sb.WriteByte('\\')
				}
				sb.WriteRune(c)
			}
			sb.WriteByte('"')
		} else {
			sb.WriteString(e)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

// applyViewColumnAliases rewrites the top-level select-list of a view's raw
// definition so each output column carries the explicit name from a
// `CREATE VIEW v (c1, c2, …)` column list, mirroring how PostgreSQL's
// pg_get_viewdef bakes the view column names into the SELECT as `expr AS cN`.
// goopg captures the view body verbatim (no deparser), so the aliases are
// spliced into the raw text.
//
// The rewrite is applied ONLY when it can be done unambiguously: the body must
// begin with the SELECT keyword, the top-level select list must split into
// exactly len(aliases) items, and no item may be a `*`/`x.*` star or already
// carry a top-level `AS` alias. Otherwise the raw text is returned unchanged
// (the renamed column names are then lost — a documented fidelity gap for these
// uncommon shapes). Quoting/paren/bracket nesting and string/identifier
// literals are respected when locating the FROM boundary and item commas.
func applyViewColumnAliases(rawDef string, aliases []string) string {
	if len(aliases) == 0 {
		return rawDef
	}
	trimmed := strings.TrimLeft(rawDef, " \t\n\r")
	const kw = "select"
	if len(trimmed) < len(kw) || !strings.EqualFold(trimmed[:len(kw)], kw) {
		return rawDef
	}
	rest := trimmed[len(kw):]
	// Guard against matching e.g. "selected" — the SELECT keyword must be
	// followed by a non-identifier byte (whitespace/paren).
	if rest == "" || isViewIdentByte(rest[0]) {
		return rawDef
	}
	// The select list ends at the first top-level FROM, or end-of-string when
	// the view body has no FROM clause (e.g. `SELECT 1 AS x`).
	selectList := rest
	tail := ""
	if from := findTopLevelFromKeyword(rest); from >= 0 {
		selectList = rest[:from]
		tail = rest[from:]
	}
	items := splitTopLevelCommas(selectList)
	if len(items) != len(aliases) {
		return rawDef
	}
	var sb strings.Builder
	sb.WriteString(trimmed[:len(kw)]) // preserve the original SELECT casing
	for i, item := range items {
		expr := strings.TrimSpace(item)
		if expr == "" || expr == "*" || strings.HasSuffix(expr, ".*") || hasTopLevelAsAlias(expr) {
			return rawDef
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte(' ')
		sb.WriteString(expr)
		sb.WriteString(" AS ")
		sb.WriteString(quoteViewIdent(aliases[i]))
	}
	if tail != "" {
		sb.WriteByte(' ')
		sb.WriteString(tail)
	}
	return sb.String()
}

// isViewIdentByte reports whether b can appear in an unquoted SQL identifier.
func isViewIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// findTopLevelFromKeyword returns the byte index of the first `FROM` keyword in
// s that appears at paren/bracket depth 0 and outside string/identifier
// literals, with identifier word boundaries on both sides; -1 if none.
func findTopLevelFromKeyword(s string) int {
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == 'f' || c == 'F'):
			if i+4 <= len(s) && strings.EqualFold(s[i:i+4], "from") {
				prevOK := i == 0 || !isViewIdentByte(s[i-1])
				nextOK := i+4 == len(s) || !isViewIdentByte(s[i+4])
				if prevOK && nextOK {
					return i
				}
			}
		}
	}
	return -1
}

// hasTopLevelAsAlias reports whether expr contains an `AS` keyword at
// paren/bracket depth 0 and outside literals — i.e. it already names its output
// column. Such items are left untouched (the whole rewrite bails) to avoid
// generating `expr AS old AS new`. A bare trailing alias without AS is not
// detected (atypical alongside an explicit view column list).
func hasTopLevelAsAlias(expr string) bool {
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(expr) && expr[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '"' {
				if i+1 < len(expr) && expr[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == 'a' || c == 'A'):
			if i+2 <= len(expr) && strings.EqualFold(expr[i:i+2], "as") {
				prevOK := i == 0 || !isViewIdentByte(expr[i-1])
				nextOK := i+2 == len(expr) || !isViewIdentByte(expr[i+2])
				if prevOK && nextOK {
					return true
				}
			}
		}
	}
	return false
}

// quoteViewIdent renders an alias as a SQL identifier, double-quoting it only
// when it is not a simple lowercase identifier (mirrors PG's quote_identifier).
func quoteViewIdent(s string) string {
	simple := len(s) > 0
	if simple {
		c := s[0]
		if !(c == '_' || (c >= 'a' && c <= 'z')) {
			simple = false
		}
	}
	if simple {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				simple = false
				break
			}
		}
	}
	// Mirror PostgreSQL quote_identifier(): a char-class-safe, all-lowercase
	// identifier must still be quoted when it is a non-UNRESERVED keyword, so a
	// view column alias named e.g. "select" round-trips as "select".
	if simple && sqlkeywords.IsReservedForQuoting(s) {
		simple = false
	}
	if simple {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// applyPgFormat implements PostgreSQL's format() function for common specifiers:
// %s (value as text), %I (quote_ident), %L (quote_literal), %% (literal %). M0097-0003.
// applyPgFormat is kept as a simple no-error wrapper for callers that don't
// need error propagation. New callers should use applyPgFormatFull.
func applyPgFormat(fmtStr string, args []Datum) string {
	s, _ := applyPgFormatFull(fmtStr, args)
	return s
}

// applyPgFormatFull implements PostgreSQL format():
//
//	%[position][flags][width]type
//
// position: N$ (1-based index into args; absent = sequential)
// flags:    - (left-align)
// width:    integer (minimum field width, space-padded)
// type:     s | I | L | %
//
// Returns an error for:
//   - argument 0 (arguments numbered from 1)
//   - too few arguments
//   - unterminated format specifier
//   - unrecognized type specifier
//   - NULL value for %I
//
// M0097-0063.
func applyPgFormatFull(fmtStr string, args []Datum) (string, error) {
	var sb strings.Builder
	seqIdx := 0 // next sequential arg index (0-based)
	for i := 0; i < len(fmtStr); i++ {
		if fmtStr[i] != '%' {
			sb.WriteByte(fmtStr[i])
			continue
		}
		i++
		if i >= len(fmtStr) {
			// Unterminated at very end.
			return "", &ExecError{Code: "22023",
				Message: "unterminated format() type specifier",
				Hint:    `For a single "%" use "%%".`}
		}
		if fmtStr[i] == '%' {
			sb.WriteByte('%')
			continue
		}

		// Parse optional position (digits followed by $).
		pos := -1 // -1 = sequential
		j := i
		for j < len(fmtStr) && fmtStr[j] >= '0' && fmtStr[j] <= '9' {
			j++
		}
		if j > i && j < len(fmtStr) && fmtStr[j] == '$' {
			// Positional argument.
			n := 0
			for _, c := range fmtStr[i:j] {
				n = n*10 + int(c-'0')
			}
			if n == 0 {
				return "", &ExecError{Code: "22023",
					Message: "format specifies argument 0, but arguments are numbered from 1"}
			}
			pos = n - 1 // convert to 0-based
			i = j + 1   // skip past '$'
		}

		if i >= len(fmtStr) {
			return "", &ExecError{Code: "22023",
				Message: "unterminated format() type specifier",
				Hint:    `For a single "%" use "%%".`}
		}

		// Parse optional flags.
		leftAlign := false
		if fmtStr[i] == '-' {
			leftAlign = true
			i++
		}

		if i >= len(fmtStr) {
			return "", &ExecError{Code: "22023",
				Message: "unterminated format() type specifier",
				Hint:    `For a single "%" use "%%".`}
		}

		// Parse optional width: either a decimal integer, or * / *N$ (width from arg).
		width := 0
		if fmtStr[i] == '*' {
			// Width taken from an argument.
			i++ // consume '*'
			// Check for *N$ positional width.
			widthPos := -1
			j2 := i
			for j2 < len(fmtStr) && fmtStr[j2] >= '0' && fmtStr[j2] <= '9' {
				j2++
			}
			if j2 > i && j2 < len(fmtStr) && fmtStr[j2] == '$' {
				n := 0
				for _, c := range fmtStr[i:j2] {
					n = n*10 + int(c-'0')
				}
				if n == 0 {
					return "", &ExecError{Code: "22023",
						Message: "format specifies argument 0, but arguments are numbered from 1"}
				}
				widthPos = n - 1
				i = j2 + 1
			}
			// Get width value from argument.
			// Even for positional *N$, we always advance seqIdx by 1 to mirror PG's
			// sequential-slot accounting — this prevents the same slot from being
			// reused as both the width provider and the value. M0097-0063.
			var wArgI int
			if widthPos >= 0 {
				wArgI = widthPos
			} else {
				wArgI = seqIdx
			}
			seqIdx++ // always advance, regardless of positional vs sequential
			if wArgI < len(args) && !args[wArgI].IsNull() {
				w := int(args[wArgI].Int)
				if w < 0 {
					leftAlign = true
					w = -w
				}
				width = w
			}
		} else {
			for i < len(fmtStr) && fmtStr[i] >= '0' && fmtStr[i] <= '9' {
				width = width*10 + int(fmtStr[i]-'0')
				i++
			}
		}

		if i >= len(fmtStr) {
			return "", &ExecError{Code: "22023",
				Message: "unterminated format() type specifier",
				Hint:    `For a single "%" use "%%".`}
		}

		// Determine argument index.
		var argI int
		if pos >= 0 {
			argI = pos
		} else {
			argI = seqIdx
			seqIdx++
		}

		// Type specifier.
		spec := fmtStr[i]
		switch spec {
		case 's':
			if argI >= len(args) {
				return "", &ExecError{Code: "22023", Message: "too few arguments for format()"}
			}
			d := args[argI]
			var s string
			if d.IsNull() {
				s = ""
			} else {
				s = d.Format()
			}
			sb.WriteString(padString(s, width, leftAlign))
		case 'I':
			if argI >= len(args) {
				return "", &ExecError{Code: "22023", Message: "too few arguments for format()"}
			}
			d := args[argI]
			if d.IsNull() {
				return "", &ExecError{Code: "22004",
					Message: "null values cannot be formatted as an SQL identifier"}
			}
			// Use Format() so integers, numerics, etc. get their string representation.
			ident := pgQuoteIdent(d.Format())
			sb.WriteString(padString(ident, width, leftAlign))
		case 'L':
			if argI >= len(args) {
				return "", &ExecError{Code: "22023", Message: "too few arguments for format()"}
			}
			d := args[argI]
			var lit string
			if d.IsNull() {
				lit = "NULL"
			} else {
				// Use Format() so integers, numerics, etc. get their string representation.
				escaped := strings.ReplaceAll(d.Format(), "'", "''")
				lit = "'" + escaped + "'"
			}
			sb.WriteString(padString(lit, width, leftAlign))
		default:
			return "", &ExecError{Code: "22023",
				Message: fmt.Sprintf("unrecognized format() type specifier %q", string(spec)),
				Hint:    `For a single "%" use "%%".`}
		}
	}
	return sb.String(), nil
}

// padString pads s to at least minWidth characters. If leftAlign, spaces are
// added on the right; otherwise on the left.
func padString(s string, minWidth int, leftAlign bool) string {
	if minWidth <= 0 || len(s) >= minWidth {
		return s
	}
	pad := strings.Repeat(" ", minWidth-len(s))
	if leftAlign {
		return s + pad
	}
	return pad + s
}

// pgKindTypeName returns the PostgreSQL type name for a Datum Kind,
// used in error messages like "operator does not exist: integer || numeric".
func pgKindTypeName(k DatumKind) string {
	switch k {
	case KindInt:
		return "integer"
	case KindNumeric:
		return "numeric"
	case KindBool:
		return "boolean"
	case KindTime:
		return "timestamp"
	case KindString:
		return "text"
	case KindBytes:
		return "bytea"
	default:
		return "unknown"
	}
}

// pgQuoteLiteral returns a SQL string literal for s.
// If s contains backslashes, uses E'...' escape-string syntax so that
// backslashes are correctly represented. Otherwise uses standard '...' form.
func pgQuoteLiteral(s string) string {
	if strings.Contains(s, `\`) {
		// E-string syntax: escape ' as '' and \ as \\.
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `''`)
		return `E'` + escaped + `'`
	}
	escaped := strings.ReplaceAll(s, `'`, `''`)
	return `'` + escaped + `'`
}

// pgQuoteIdent quotes a SQL identifier if necessary (uppercase, spaces, special chars). M0097-0003.
func pgQuoteIdent(s string) string {
	if s == "" {
		return `""`
	}
	// Safe unquoted: starts with letter/underscore, contains only letter/digit/underscore,
	// all lowercase, and is not a reserved word.
	safe := true
	for i, c := range s {
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || c == '_') {
				safe = false
				break
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				safe = false
				break
			}
		}
	}
	// Mirror PostgreSQL quote_identifier(): even a char-class-safe, all-lowercase
	// identifier must be quoted when it is a non-UNRESERVED keyword, so that e.g.
	// quote_ident('user') yields "user" and quote_ident('select') yields "select".
	if safe && sqlkeywords.IsReservedForQuoting(s) {
		safe = false
	}
	if safe {
		return s
	}
	// Must quote.
	escaped := strings.ReplaceAll(s, `"`, `""`)
	return `"` + escaped + `"`
}

// quoteQualifiedIdentifier mirrors PostgreSQL's quote_qualified_identifier
// (postgres/src/backend/utils/adt/ruleutils.c): qualifier.ident with each
// component run through quote_identifier, or just quote_identifier(ident) when
// qualifier is empty. The reg*out family (regproc.c) uses it to render a
// resolved object name — the name is ALWAYS identifier-quoted even when it is
// not schema-qualified, and the qualifier (the object's namespace) is quoted
// too. M0119-0006 (69th slice).
func quoteQualifiedIdentifier(qualifier, ident string) string {
	if qualifier == "" {
		return pgQuoteIdent(ident)
	}
	return pgQuoteIdent(qualifier) + "." + pgQuoteIdent(ident)
}

// isArrayLiteralText reports whether s is a PostgreSQL array literal of the
// canonical `{...}` form. Used to route the @>/<@/&& operators to anyarray
// set semantics rather than geometric box semantics (design 0118-0139).
func isArrayLiteralText(s string) bool {
	return len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}'
}

// evalArraySetOp evaluates the anyarray containment/overlap operators on two
// array literals, mirroring PG's arraycontains / arraycontained / arrayoverlap
// (src/backend/utils/adt/arrayfuncs.c). Element equality uses the canonical
// text rendering — adequate for the scalar element types goopg stores in array
// columns (int2/4/8, float, bool, text), since both operands arrive in PG's
// canonical output form. NULL elements never match (PG: array element equality
// over NULL is unknown, so they are not considered contained).
func evalArraySetOp(op parser.OpCode, ls, rs string) bool {
	le := parseTextArray(ls)
	re := parseTextArray(rs)
	switch op {
	case parser.OpContains:
		// a @> b: every element of b is present in a.
		return arrayElemsSubset(re, le)
	case parser.OpContainedBy:
		// a <@ b: every element of a is present in b.
		return arrayElemsSubset(le, re)
	case parser.OpOverlap:
		// a && b: a and b share at least one element.
		for _, x := range le {
			for _, y := range re {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	return false
}

// arrayElemsSubset reports whether every element of sub is present in super.
// An empty sub is trivially contained (PG: '{}' <@ anything is true).
func arrayElemsSubset(sub, super []string) bool {
	set := make(map[string]struct{}, len(super))
	for _, e := range super {
		set[e] = struct{}{}
	}
	for _, e := range sub {
		if _, ok := set[e]; !ok {
			return false
		}
	}
	return true
}

// parseTextArray parses a PostgreSQL text array literal {elem1,"elem2",...}
// and returns its elements. Used for name[] cast. M0097-0003.
// ParseTextArrayLiteral exposes parseTextArray for the initdb catalog
// reloads (B3.2: pg_event_trigger.evttags decodes to the canonical
// "{a,b}" text, which the reload splits back to element strings).
func ParseTextArrayLiteral(s string) []string { return parseTextArray(s) }

func parseTextArray(s string) []string {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return []string{s}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return nil
	}
	var elems []string
	i := 0
	for i < len(inner) {
		// PG's ReadArrayStr (postgres/src/backend/utils/adt/arrayfuncs.c)
		// skips leading whitespace before each element, so a raw literal
		// like '{"(0,0)", "(0,0)"}' (space after the comma) still yields
		// two elements, not a mis-split third. Without this skip, the
		// unquoted branch below would swallow the leading space AND the
		// following element's opening quote as one bogus element.
		for i < len(inner) && inner[i] == ' ' {
			i++
		}
		if i < len(inner) && inner[i] == '"' {
			// Quoted element.
			i++
			var sb strings.Builder
			for i < len(inner) {
				if inner[i] == '"' {
					if i+1 < len(inner) && inner[i+1] == '"' {
						sb.WriteByte('"')
						i += 2
					} else {
						i++
						break
					}
				} else if inner[i] == '\\' && i+1 < len(inner) {
					sb.WriteByte(inner[i+1])
					i += 2
				} else {
					sb.WriteByte(inner[i])
					i++
				}
			}
			elems = append(elems, sb.String())
		} else {
			// Unquoted element: read until comma or end.
			start := i
			for i < len(inner) && inner[i] != ',' {
				i++
			}
			elems = append(elems, inner[start:i])
		}
		if i < len(inner) && inner[i] == ',' {
			i++
		}
	}
	return elems
}

// charTypeParseOctalEscape handles PostgreSQL's "char" internal single-byte type
// which interprets backslash-octal sequences (\NNN) in string inputs.
// Returns (byte, true) if the string is a valid \NNN octal escape, else (0, false).
// M0097-0003.
func charTypeParseOctalEscape(s string) (byte, bool) {
	if len(s) != 4 || s[0] != '\\' {
		return 0, false
	}
	d0, d1, d2 := s[1], s[2], s[3]
	if d0 < '0' || d0 > '7' || d1 < '0' || d1 > '7' || d2 < '0' || d2 > '7' {
		return 0, false
	}
	val := int(d0-'0')*64 + int(d1-'0')*8 + int(d2-'0')
	if val > 255 {
		return 0, false
	}
	return byte(val), true
}

// charTypeDisplayForm returns the PostgreSQL charout() display form for a byte value:
// - Byte 0 → "" (null byte → empty, matching PostgreSQL's chartotext behavior)
// - Printable ASCII (32-126) → single character
// - Non-printable → \NNN (3-digit octal escape)
// M0097-0003.
func charTypeDisplayForm(b byte) string {
	if b == 0 {
		return ""
	}
	if b >= 32 && b <= 126 {
		return string([]byte{b})
	}
	// Non-printable: format as \NNN octal.
	return fmt.Sprintf("\\%03o", b)
}

// currentSchemaFromSearchPath resolves current_schema by walking the effective
// search_path and returning the first schema that exists. Built-in schemas
// (pg_catalog, information_schema, public) are always considered present.
// Returns NullDatum if no schema on the path exists.
// searchPathSchemas returns the schemas in the effective search_path that
// actually exist, in search-path order. Shared by current_schema (scalar,
// first entry) and current_schemas (array). $user expands to the connection
// user; built-in schemas (pg_catalog/information_schema/public) always exist,
// user schemas are confirmed via the schema registry (empty schemas included).
func searchPathSchemas(ctx *Context) []string {
	searchPath := `"$user", public` // default
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("search_path"); ok {
			searchPath = v
		}
	}
	user := "postgres"
	var out []string
	for _, rawSchema := range strings.Split(searchPath, ",") {
		s := strings.TrimSpace(rawSchema)
		s = strings.Trim(s, `"'`)
		if s == "$user" {
			s = user
		}
		if s == "" {
			continue
		}
		lc := strings.ToLower(s)
		switch lc {
		case "pg_catalog", "information_schema", "public":
			out = append(out, lc)
			continue
		}
		// User-created schemas: present if registered, even when empty.
		if ctx != nil && ctx.Catalog != nil {
			if ctx.Catalog.SchemaExists(s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// RegObjectSchemaVisible reports whether schema is visible for reg*-cast
// qualification purposes (format_operator/format_procedure's own
// OperatorIsVisible/ProcedureIsVisible check, regproc.c): pg_catalog is
// always implicitly searched regardless of search_path content, and every
// other schema must appear in the session's effective search_path. pg_dump
// always connects with search_path='' (ALWAYS_SECURE_SEARCH_PATH_SQL), so
// this is what makes dumpOpclass/dumpOpfamily's own
// amopopr::pg_catalog.regoperator / amproc::pg_catalog.regprocedure casts
// come back schema-qualified for a user-defined operator/function but bare
// for a builtin one. Exported (73rd slice) so the server's SELECT/COPY wire
// paths can pass it as the regprocedure arglist's per-arg visibility
// predicate (deferral row 1342). DU-002 (M0119-0004) slice 412.
func RegObjectSchemaVisible(ctx *Context, schema string) bool {
	if schema == "" || schema == "pg_catalog" {
		return true
	}
	for _, s := range searchPathSchemas(ctx) {
		if s == schema {
			return true
		}
	}
	return false
}

func currentSchemaFromSearchPath(ctx *Context) (Datum, error) {
	schemas := searchPathSchemas(ctx)
	if len(schemas) == 0 {
		return NullDatum, nil
	}
	return NewStringDatum(schemas[0]), nil
}

// currentSchemasArray renders the existing search-path schemas as a `{a,b}`
// text array literal (name[]). When includeImplicit is true, the implicitly
// searched pg_catalog is prepended unless already explicitly present.
func currentSchemasArray(ctx *Context, includeImplicit bool) (Datum, error) {
	schemas := searchPathSchemas(ctx)
	if includeImplicit {
		hasPgCatalog := false
		for _, s := range schemas {
			if s == "pg_catalog" {
				hasPgCatalog = true
				break
			}
		}
		if !hasPgCatalog {
			schemas = append([]string{"pg_catalog"}, schemas...)
		}
	}
	if len(schemas) == 0 {
		return NewStringDatum("{}"), nil
	}
	return NewStringDatum("{" + strings.Join(schemas, ",") + "}"), nil
}

func isValidBoolInput(v string) bool {
	_, ok := pgBoolIn(v)
	return ok
}

// hashPartTypesCompatible returns true if the arg type is compatible with the column type
// for satisfies_hash_partition type checking.
func hashPartTypesCompatible(colType, argTypeName string) bool {
	col := pgFormatTypeName(colType)
	arg := strings.ToLower(argTypeName)
	if col == arg {
		return true
	}
	// Integer family compatibility
	intFamily := map[string]bool{"integer": true, "smallint": true, "bigint": true, "int4": true, "int2": true, "int8": true}
	if intFamily[col] && intFamily[arg] {
		return true
	}
	return false
}

// hashPartTypeName returns the user-visible PG type name for a planner expression,
// used to build satisfies_hash_partition type mismatch messages. Returns "" if unknown.
func hashPartTypeName(e optimizer.Expr) string {
	switch x := e.(type) {
	case *optimizer.CastExpr:
		return pgFormatTypeName(x.TargetType)
	case *optimizer.IntegerConst:
		return "integer"
	case *optimizer.NumericConst:
		return "numeric"
	case *optimizer.StringConst:
		return "text"
	case *optimizer.BooleanConst:
		return "boolean"
	case *optimizer.FuncCall:
		switch strings.ToLower(x.Name) {
		case "now", "current_timestamp":
			return "timestamp with time zone"
		case "current_date":
			return "date"
		case "current_time":
			return "time with time zone"
		}
	}
	return ""
}

// pgFormatTypeName converts internal type names to PG user-visible names.
func pgFormatTypeName(t string) string {
	switch strings.ToLower(t) {
	case "int4", "int", "integer":
		return "integer"
	case "int2", "smallint":
		return "smallint"
	case "int8", "bigint":
		return "bigint"
	case "float4", "real":
		return "real"
	case "float8", "double precision":
		return "double precision"
	case "bool", "boolean":
		return "boolean"
	case "text":
		return "text"
	case "varchar", "character varying":
		return "character varying"
	case "bpchar", "character", "char":
		return "character"
	case "timestamptz", "timestamp with time zone":
		return "timestamp with time zone"
	case "timestamp", "timestamp without time zone":
		return "timestamp without time zone"
	case "date":
		return "date"
	case "time", "time without time zone":
		return "time without time zone"
	case "timetz", "time with time zone":
		return "time with time zone"
	case "numeric", "decimal":
		return "numeric"
	}
	return t
}

// userTypeNameForOID resolves a dynamically-allocated pg_type OID (one goopg
// itself assigned to a user-defined enum/domain/composite/range/multirange
// type, or one of their auto-generated array types) to its name. Shared by
// format_type's built-in-fallback path, the `::regtype` cast, and
// RegtypeName, since all three need the identical resolution across all four
// user-type kinds. Each arm resolves the type's ACTUAL schema from its
// NamespaceOID (slice A field) via SchemaNameForOID and renders through
// regOutQualified, so an off-search_path type is schema-qualified with its real
// schema AND quote_identifier'd — exactly PG's regtypeout → format_type_be →
// format_type_extended (format_type.c:303-326: quote_qualified_identifier
// when !TypeIsVisible), not a hardcoded "public." prefix. qualify is the
// per-object predicate "should this type's schema be qualified?" mirroring
// RegObjectSchemaVisible's role for the regproc/regoperator casts — callers
// pass a closure only when the schema is NOT visible on the effective
// search_path (e.g. pg_dump's search_path=''), matching real PostgreSQL's
// regtypeout/format_type, which only schema-qualifies when necessary rather
// than unconditionally. The array/multirange "[]"/MultirangeName suffix is
// split off BEFORE quoting and re-appended after, so the ELEMENT is quoted and
// `[]` follows unquoted (the 73rd-slice split/re-append convention).
// Returns ("", false) if oid does not match any user type. DU-002 (M0110-0001)
// regtype/format_type unification follow-up; qualify func added as the
// M0119-0006 slice B (deferral row 1355) schema-qualification fix.
func userTypeNameForOID(cat catalog.Catalog, oid uint32, qualify func(schema string) bool) (string, bool) {
	if et, ok := cat.LookupEnumByOID(oid); ok {
		schema := cat.SchemaNameForOID(et.NamespaceOID)
		return regOutQualified(schema, et.Name, regOutQualifySchema(schema, qualify)), true
	}
	if et, ok := cat.LookupEnumByArrayOID(oid); ok {
		schema := cat.SchemaNameForOID(et.NamespaceOID)
		return regOutQualified(schema, et.Name, regOutQualifySchema(schema, qualify)) + "[]", true
	}
	if dom, ok := cat.LookupDomainByOID(oid); ok {
		schema := cat.SchemaNameForOID(dom.NamespaceOID)
		return regOutQualified(schema, dom.Name, regOutQualifySchema(schema, qualify)), true
	}
	if dom, ok := cat.LookupDomainByArrayOID(oid); ok {
		schema := cat.SchemaNameForOID(dom.NamespaceOID)
		return regOutQualified(schema, dom.Name, regOutQualifySchema(schema, qualify)) + "[]", true
	}
	if ct, ok := cat.LookupCompositeTypeByOID(oid); ok {
		schema := cat.SchemaNameForOID(ct.NamespaceOID)
		return regOutQualified(schema, ct.Name, regOutQualifySchema(schema, qualify)), true
	}
	if ct, ok := cat.LookupCompositeTypeByArrayOID(oid); ok {
		schema := cat.SchemaNameForOID(ct.NamespaceOID)
		return regOutQualified(schema, ct.Name, regOutQualifySchema(schema, qualify)) + "[]", true
	}
	if rt, ok := cat.LookupRangeTypeByOID(oid); ok {
		schema := cat.SchemaNameForOID(rt.NamespaceOID)
		return regOutQualified(schema, rt.Name, regOutQualifySchema(schema, qualify)), true
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeOID(oid); ok {
		schema := cat.SchemaNameForOID(rt.NamespaceOID)
		return regOutQualified(schema, rt.MultirangeName, regOutQualifySchema(schema, qualify)), true
	}
	if rt, ok := cat.LookupRangeTypeByArrayOID(oid); ok {
		schema := cat.SchemaNameForOID(rt.NamespaceOID)
		return regOutQualified(schema, rt.Name, regOutQualifySchema(schema, qualify)) + "[]", true
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeArrayOID(oid); ok {
		schema := cat.SchemaNameForOID(rt.NamespaceOID)
		return regOutQualified(schema, rt.MultirangeName, regOutQualifySchema(schema, qualify)) + "[]", true
	}
	return "", false
}

// regOutQualifySchema evaluates the per-schema qualify predicate on the schema
// regOutQualified will ACTUALLY render with: an empty schema (a user type with
// no recorded NamespaceOID — slice A treats that as public, and regOutQualified
// defaults ""→"public") is defaulted to "public" FIRST, so a NamespaceOID=0
// type behaves exactly like a public type. Evaluating qualify("") directly
// would return true (RegObjectSchemaVisible treats "" as always-visible) and
// wrongly leave the name bare even under search_path='', where PG renders a
// public type qualified ("public.mood") because TypeIsVisible is false.
func regOutQualifySchema(schema string, qualify func(schema string) bool) bool {
	if schema == "" {
		schema = "public"
	}
	return qualify(schema)
}

// userTypeOIDForName resolves a user-defined type name to its pg_type OID,
// searching the enum/domain/composite/range/multirange registries in turn —
// the name-based mirror of userTypeNameForOID, used by `::regtype`'s
// string→OID direction (e.g. `'myrange'::regtype`). Array-form names
// (`myrange[]`) are not handled here, matching the pre-existing enum-only
// behavior this generalizes. Returns (0, false) if name matches no user type.
func userTypeOIDForName(cat catalog.Catalog, name string) (uint32, bool) {
	if et, ok := cat.LookupEnum(name); ok {
		return et.OID, true
	}
	if dom, ok := cat.LookupDomain(name); ok {
		return dom.OID, true
	}
	if ct := cat.LookupCompositeType(name); ct != nil {
		return ct.OID, true
	}
	if rt, ok := cat.LookupRangeType(name); ok {
		return rt.OID, true
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeName(name); ok {
		return rt.MultirangeOID, true
	}
	return 0, false
}

// unknownPseudoTypeOID is pg_type's row OID for the "unknown" pseudo-type
// (postgres/src/include/catalog/pg_type_d.h's UNKNOWNOID) — pg_typeof(NULL)
// reports this, not any real type.
const unknownPseudoTypeOID = 705

// pgTypeofOIDForName resolves a PostgreSQL type display name (as produced by
// planner.pgTypeofDisplayName, or this file's own Kind-to-name runtime
// fallback above) to its pg_type OID, so pg_typeof() can return a regtype
// value backed by the real OID instead of display text. Covers the quoted
// `"char"`/OID-18 special case, the "unknown" pseudo-type, every built-in
// type TypeNameToOID knows, and user-defined enum/domain/composite/range/
// multirange types via a live catalog lookup (mirroring the `::regtype`
// cast's own string->OID resolution). M0122-0005 pg_typeof()::oid follow-up.
func pgTypeofOIDForName(cat catalog.Catalog, name string) uint32 {
	if name == `"char"` {
		return catalog.OIDChar
	}
	if strings.EqualFold(name, "unknown") {
		return unknownPseudoTypeOID
	}
	if oid := catalog.TypeNameToOID(name); oid != catalog.OIDText || strings.EqualFold(name, "text") {
		return oid
	}
	// TypeNameToOID falls back to OIDText for any name it doesn't
	// recognize (the check above already handled genuine "text"), most
	// likely a user-defined type — pgTypeofDisplayName's default case
	// returns such a name unchanged (bare, unqualified).
	if cat != nil {
		if oid, ok := userTypeOIDForName(cat, strings.ToLower(name)); ok {
			return oid
		}
	}
	return catalog.OIDText
}

// RegtypeName renders a pg_type OID as PostgreSQL's canonical display name
// (regtypeout/format_type_be), covering built-in types plus user-defined
// enum/domain/composite/range/multirange types. Used by the wire-output
// layer (internal/server/dispatch.go's appendTypedCellText) to render a
// regtype-typed result column, e.g. pg_typeof()'s result or an
// `<oid>::regtype` cast — mirrors RegprocName/RegprocedureName's role for
// regproc/regprocedure. InvalidOid (0) renders "-", matching regtypeout.
// qualify is the per-object predicate "should this type's ACTUAL schema be
// qualified?" — passed through to userTypeNameForOID, which resolves the
// schema from the type's NamespaceOID and renders via regOutQualified
// (deferral row 1355). The caller determines the predicate from the session's
// effective search_path (see internal/server/dispatch.go's publicSchemaVisible),
// mirroring userTypeNameForOID's own schema-visibility contract. Builtin types
// (oidToBuiltinTypeName) are never qualified — they live in pg_catalog, which
// every search_path searches implicitly.
// M0122-0005 pg_typeof()::oid follow-up; M0119-0006 slice B qualify→predicate.
func RegtypeName(cat catalog.Catalog, oid uint32, qualify func(schema string) bool) string {
	if oid == 0 {
		return "-"
	}
	if oid == unknownPseudoTypeOID {
		return "unknown"
	}
	if name := oidToBuiltinTypeName(oid); name != "" {
		return name
	}
	if cat != nil {
		if uname, ok := userTypeNameForOID(cat, oid, qualify); ok {
			return uname
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// formatIntervalTypmod renders the suffix intervaltypmodout produces for a
// packed INTERVAL_TYPMOD (timestamp.c:1065): the range spelling with a leading
// space (" year to month", " second") plus the precision "(p)" when a SECOND(p)
// precision is declared, or "" for a bare/full-range interval. The field bit
// positions are datetime.h's MONTH=1, YEAR=2, DAY=3, HOUR=10, MINUTE=11,
// SECOND=12 — the same values pgdatetime.AdjustIntervalForTypmod and the
// parser's intervalFieldTypmodBit use. M0119-0006 (63rd slice).
func formatIntervalTypmod(typmod int64) string {
	if typmod < 0 {
		return ""
	}
	fields := int((typmod >> 16) & 0x7FFF)
	precision := int(typmod & 0xFFFF)
	fieldstr := ""
	switch fields {
	case 1 << 2:
		fieldstr = " year"
	case 1 << 1:
		fieldstr = " month"
	case 1 << 3:
		fieldstr = " day"
	case 1 << 10:
		fieldstr = " hour"
	case 1 << 11:
		fieldstr = " minute"
	case 1 << 12:
		fieldstr = " second"
	case (1 << 2) | (1 << 1):
		fieldstr = " year to month"
	case (1 << 3) | (1 << 10):
		fieldstr = " day to hour"
	case (1 << 3) | (1 << 10) | (1 << 11):
		fieldstr = " day to minute"
	case (1 << 3) | (1 << 10) | (1 << 11) | (1 << 12):
		fieldstr = " day to second"
	case (1 << 10) | (1 << 11):
		fieldstr = " hour to minute"
	case (1 << 10) | (1 << 11) | (1 << 12):
		fieldstr = " hour to second"
	case (1 << 11) | (1 << 12):
		fieldstr = " minute to second"
	case 0x7FFF:
		fieldstr = ""
	}
	if precision != 0xFFFF {
		return fmt.Sprintf("%s(%d)", fieldstr, precision)
	}
	return fieldstr
}

// formatTypeOID implements PostgreSQL's format_type(oid, typemod) built-in.
// Maps well-known system type OIDs to their SQL display names. Unknown OIDs
// return "???". Used by psql \d+ meta-commands. M0097-0023.
func formatTypeOID(typeOID, typmod int64) string {
	switch typeOID {
	case 16:
		return "boolean"
	case 17:
		return "bytea"
	case 18:
		return "\"char\""
	case 19:
		return "name"
	case 20:
		return "bigint"
	case 21:
		return "smallint"
	case 22:
		// int2vector: a space-separated list of int2 (pg_index.indkey). NOT
		// smallint[] — that is the genuine _int2 array type (OID 1005). DU-002
		// slice 81.
		return "int2vector"
	case 23:
		return "integer"
	case 25:
		return "text"
	case 26:
		return "oid"
	case 27:
		return "tid"
	case 28:
		return "xid"
	case 29:
		return "cid"
	// DU-002 slice 80: the OID-reference ("reg*") family. No typmod, bare names.
	case 24:
		return "regproc"
	case 2202:
		return "regprocedure"
	case 2203:
		return "regoper"
	case 2204:
		return "regoperator"
	case 2205:
		return "regclass"
	case 2206:
		return "regtype"
	case 3734:
		return "regconfig"
	case 3769:
		return "regdictionary"
	case 4089:
		return "regnamespace"
	case 4096:
		return "regrole"
	case 4191:
		return "regcollation"
	// DU-002 slice 80: the reg* array types. No typmod, bare element name + [].
	case 1008:
		return "regproc[]"
	case 2207:
		return "regprocedure[]"
	case 2208:
		return "regoper[]"
	case 2209:
		return "regoperator[]"
	case 2210:
		return "regclass[]"
	case 2211:
		return "regtype[]"
	case 3735:
		return "regconfig[]"
	case 3770:
		return "regdictionary[]"
	case 4090:
		return "regnamespace[]"
	case 4097:
		return "regrole[]"
	case 4192:
		return "regcollation[]"
	case 30:
		// oidvector: a space-separated list of oid (pg_proc.proargtypes). NOT
		// oid[] — that is the genuine _oid array type (OID 1028). DU-002 slice 81.
		return "oidvector"
	case 1006:
		// _int2vector: int2vector has no typmod, so the array is the bare name + [].
		return "int2vector[]"
	case 1013:
		// _oidvector: oidvector has no typmod, so the array is the bare name + [].
		return "oidvector[]"
	case 1003:
		// _name: name has no typmod, so the array is the bare name + []. Slice 82.
		return "name[]"
	case 114:
		return "json"
	case 142:
		return "xml"
	case 199:
		// _json: json has no typmod, so the array is the bare name. Slice 69.
		return "json[]"
	case 3807:
		// _jsonb: jsonb has no typmod, so the array is the bare name. Slice 69.
		return "jsonb[]"
	case 600:
		return "point"
	case 601:
		// lseg: no typmod, bare name. DU-002 slice 72.
		return "lseg"
	case 602:
		// path: no typmod, bare name. Slice 72.
		return "path"
	case 603:
		// box: no typmod, bare name. Slice 72.
		return "box"
	case 604:
		// polygon: no typmod, bare name. Slice 72.
		return "polygon"
	case 628:
		// line: no typmod, bare name. Slice 72.
		return "line"
	case 718:
		// circle: no typmod, bare name. Slice 72.
		return "circle"
	case 1017:
		// _point: no typmod, bare element name + []. Slice 72.
		return "point[]"
	case 1018:
		// _lseg: no typmod, bare element name + []. Slice 72.
		return "lseg[]"
	case 1019:
		// _path: no typmod, bare element name + []. Slice 72.
		return "path[]"
	case 1020:
		// _box: no typmod, bare element name + []. Slice 72.
		return "box[]"
	case 1027:
		// _polygon: no typmod, bare element name + []. Slice 72.
		return "polygon[]"
	case 629:
		// _line: no typmod, bare element name + []. Slice 72.
		return "line[]"
	case 719:
		// _circle: no typmod, bare element name + []. Slice 72.
		return "circle[]"
	case 650:
		// cidr: no typmod, bare name. DU-002 slice 71.
		return "cidr"
	case 651:
		// _cidr: no typmod, bare element name + []. Slice 71.
		return "cidr[]"
	case 774:
		// macaddr8: no typmod, bare name. Slice 71.
		return "macaddr8"
	case 775:
		// _macaddr8: no typmod, bare element name + []. Slice 71.
		return "macaddr8[]"
	case 829:
		// macaddr: no typmod, bare name. Slice 71.
		return "macaddr"
	case 869:
		// inet: no typmod, bare name. Slice 71.
		return "inet"
	case 1040:
		// _macaddr: no typmod, bare element name + []. Slice 71.
		return "macaddr[]"
	case 1041:
		// _inet: no typmod, bare element name + []. Slice 71.
		return "inet[]"
	case 700:
		return "real"
	case 701:
		return "double precision"
	case 1000:
		return "boolean[]"
	case 1001:
		// _bytea: bytea has no typmod, so the array is the bare name. Slice 67.
		return "bytea[]"
	case 1002:
		// _char: array of the single-byte "char" type (18). Slice 87.
		return "\"char\"[]"
	case 1005:
		return "smallint[]"
	case 1007:
		return "integer[]"
	case 1009:
		return "text[]"
	case 1016:
		return "bigint[]"
	case 1021:
		return "real[]"
	case 1022:
		return "double precision[]"
	case 1115:
		// _timestamp: element typmod (timestamp(p)) is carried onto the array;
		// formatTypeOID(1114) has no typmod decode, so this is the bare name.
		return "timestamp without time zone[]"
	case 1182:
		return "date[]"
	case 1183:
		// _time: bare name, mirroring scalar 1083 (no typmod decode). Slice 65.
		return "time without time zone[]"
	case 1270:
		// _timetz: bare name, mirroring scalar 1266 (no typmod decode). Slice 83.
		return "time with time zone[]"
	case 1185:
		// _timestamptz: bare name, mirroring scalar 1184. Slice 65.
		return "timestamp with time zone[]"
	case 1231:
		// _numeric: format_type strips the array, formats the element with
		// the carried typmod, then re-appends []. DU-002 slice 63.
		return formatTypeOID(1700, typmod) + "[]"
	case 1015:
		// _varchar: element typmod (varchar(n)) is carried onto the array;
		// format the element then re-append []. DU-002 slice 68.
		return formatTypeOID(1043, typmod) + "[]"
	case 1014:
		// _bpchar: element typmod (char(n)) carried onto the array. Slice 68.
		return formatTypeOID(1042, typmod) + "[]"
	case 1028:
		// _oid: oid has no typmod, so the array is the bare name. Slice 68.
		return "oid[]"
	case 1042:
		if typmod > 4 {
			return fmt.Sprintf("character(%d)", typmod-4)
		}
		return "character"
	case 1043:
		if typmod > 4 {
			return fmt.Sprintf("character varying(%d)", typmod-4)
		}
		return "character varying"
	case 1082:
		return "date"
	case 1083:
		return "time without time zone"
	case 1266:
		return "time with time zone"
	case 1114:
		return "timestamp without time zone"
	case 1184:
		return "timestamp with time zone"
	case 1186:
		// interval: atttypmod is the packed INTERVAL_TYPMOD. A bare interval
		// (typmod -1) is just "interval"; otherwise append the range/precision
		// qualifier intervaltypmodout renders. M0119-0006 (63rd slice).
		return "interval" + formatIntervalTypmod(typmod)
	case 1187:
		// _interval: a bare interval[] column has typmod -1, so this is the
		// bare element name with the [] suffix. DU-002 slice 70.
		return "interval[]"
	case 1700:
		// numeric(precision,scale): atttypmod = ((p<<16)|s)+VARHDRSZ.
		// Mirrors numerictypmodout. typmod<VARHDRSZ means no modifier.
		if typmod >= 4 {
			m := typmod - 4
			precision := (m >> 16) & 0xffff
			scale := m & 0xffff
			return fmt.Sprintf("numeric(%d,%d)", precision, scale)
		}
		return "numeric"
	case 2249:
		return "record"
	case 2281:
		// internal: pseudo-type for fmgr-internal-only arguments/results (e.g.
		// trigger, index_am_handler, and — the case this closes — the sole
		// arg/rettype of a CREATE TRANSFORM WITH FUNCTION clause naming a
		// built-in like int4recv/prsd_lextype). No typmod, bare name, exactly
		// like every other pseudo-type case here (2249 record). Real PG
		// resolves it via the genuine pg_type row (typname='internal');
		// goopg's format_type has no backing pg_type scan, so it needs an
		// explicit case like every other OID above.
		return "internal"
	case 2950:
		return "uuid"
	case 2951:
		// _uuid: uuid has no typmod, so the array is the bare name. Slice 66.
		return "uuid[]"
	case 3614:
		return "tsvector"
	case 3615:
		return "tsquery"
	case 3643:
		// _tsvector: tsvector has no typmod, so the array is the bare name. Slice 73.
		return "tsvector[]"
	case 3645:
		// _tsquery: tsquery has no typmod, so the array is the bare name. Slice 73.
		return "tsquery[]"
	case 790:
		return "money"
	case 143:
		// _xml: xml has no typmod, so the array is the bare name. Slice 74.
		return "xml[]"
	case 791:
		// _money: money has no typmod, so the array is the bare name. Slice 74.
		return "money[]"
	case 1560:
		// bit(n): atttypmod is the bit length stored raw (no VARHDRSZ), mirroring
		// anybit_typmodout. A column always carries typmod (bare `bit` => bit(1)).
		// DU-002 slice 75.
		if typmod >= 0 {
			return fmt.Sprintf("bit(%d)", typmod)
		}
		return "bit"
	case 1562:
		// bit varying(n): like bit, typmod is the raw bit length. A bare `varbit`
		// column has typmod -1 (unlimited) => the bare name. DU-002 slice 75.
		if typmod >= 0 {
			return fmt.Sprintf("bit varying(%d)", typmod)
		}
		return "bit varying"
	case 1561:
		// _bit: element typmod (bit(n)) is carried onto the array; format the
		// element then re-append []. DU-002 slice 75.
		return formatTypeOID(1560, typmod) + "[]"
	case 1563:
		// _varbit: element typmod (bit varying(n)) carried onto the array. Slice 75.
		return formatTypeOID(1562, typmod) + "[]"
	case 3220:
		// pg_lsn: no typmod, bare name. DU-002 slice 76.
		return "pg_lsn"
	case 3221:
		// _pg_lsn: pg_lsn has no typmod, so the array is the bare name. Slice 76.
		return "pg_lsn[]"
	case 2970:
		// txid_snapshot: no typmod, bare name. DU-002 slice 77.
		return "txid_snapshot"
	case 2949:
		// _txid_snapshot: no typmod, so the array is the bare name. Slice 77.
		return "txid_snapshot[]"
	case 5038:
		// pg_snapshot: no typmod, bare name. DU-002 slice 77.
		return "pg_snapshot"
	case 5039:
		// _pg_snapshot: no typmod, so the array is the bare name. Slice 77.
		return "pg_snapshot[]"
	case 5069:
		// xid8: no typmod, bare name. DU-002 slice 78.
		return "xid8"
	case 271:
		// _xid8: xid8 has no typmod, so the array is the bare name. Slice 78.
		return "xid8[]"
	case 1010:
		// _tid: tid has no typmod, so the array is the bare name. Slice 79.
		return "tid[]"
	case 1011:
		// _xid: xid has no typmod, so the array is the bare name. Slice 79.
		return "xid[]"
	case 1012:
		// _cid: cid has no typmod, so the array is the bare name. Slice 79.
		return "cid[]"
	case 3802:
		return "jsonb"
	case 4072:
		return "jsonpath"
	case 4073:
		// _jsonpath: jsonpath has no typmod, so the array is the bare name. Slice 84.
		return "jsonpath[]"
	case 1790:
		return "refcursor"
	case 2201:
		// _refcursor: refcursor has no typmod, so the array is the bare name. Slice 85.
		return "refcursor[]"
	case 1033:
		// aclitem: access-control-list item, no typmod, bare name. Slice 86.
		return "aclitem"
	case 1034:
		// _aclitem: aclitem has no typmod, so the array is the bare name. Slice 86.
		return "aclitem[]"
	default:
		return "???"
	}
}

// uuidToBytes parses a UUID string (any PG-accepted format) into 16 bytes.
func uuidToBytes(s string) ([16]byte, bool) {
	s = strings.ToLower(s)
	if len(s) == 38 && s[0] == '{' && s[37] == '}' {
		s = s[1:37]
	}
	var clean string
	if len(s) == 32 {
		clean = s
	} else if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		clean = s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	} else {
		return [16]byte{}, false
	}
	var b [16]byte
	_, err := hex.Decode(b[:], []byte(clean))
	return b, err == nil
}

// genUUIDv4 generates a random RFC 4122 version-4 UUID.
func genUUIDv4() (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// uuidV7LastNs is a process-wide monotonic clock for UUIDv7 generation,
// mirroring PostgreSQL's per-backend get_real_time_ns_ascending().
var (
	uuidV7Mu     sync.Mutex
	uuidV7LastNs int64
)

// uuidV7RealTimeNs returns a nanosecond timestamp that is guaranteed to
// advance by at least submsMinimalStepNs on every call.  This matches PG's
// get_real_time_ns_ascending(): if wall-clock hasn't advanced enough, we
// bump the virtual ns forward so that consecutive UUIDs are monotonic.
func uuidV7RealTimeNs() int64 {
	const submsMinimalStepNs = (1_000_000/4096 + 1) // 245 ns, matches PG SUBMS_MINIMAL_STEP_NS
	uuidV7Mu.Lock()
	defer uuidV7Mu.Unlock()
	ns := time.Now().UnixNano()
	if uuidV7LastNs+submsMinimalStepNs >= ns {
		ns = uuidV7LastNs + submsMinimalStepNs
	}
	uuidV7LastNs = ns
	return ns
}

// genUUIDv7 generates a UUIDv7 from the given nanosecond timestamp.
// rand_a (bytes 6-7) carries 12 bits of sub-ms precision (RFC 9562 Method 3).
func genUUIDv7(ns int64) (string, error) {
	return genUUIDv7FromMs(ns/1_000_000, ns%1_000_000)
}

// genUUIDv7FromTime is like genUUIDv7 but derives the ms-since-epoch and
// sub-ms-nanosecond components from a time.Time via ts.Unix()/ts.Nanosecond()
// instead of ts.UnixNano(). ts.UnixNano() is undefined/overflows for dates
// before 1678 or after 2262 (int64 nanoseconds can only span ~292 years);
// uuidv7(interval) accepts an arbitrary shift (uuid.sql exercises years
// 1970..10888), so it must not route through UnixNano. ts.Unix() (int64
// seconds) has no realistic overflow at those ranges.
func genUUIDv7FromTime(ts time.Time) (string, error) {
	ms := ts.Unix()*1_000 + int64(ts.Nanosecond())/1_000_000
	subNs := int64(ts.Nanosecond()) % 1_000_000
	return genUUIDv7FromMs(ms, subNs)
}

// genUUIDv7FromMs builds a UUIDv7 from ms-since-epoch and the nanosecond
// remainder within that millisecond (0..999999). Shared by genUUIDv7 (real
// time, ns-based) and genUUIDv7FromTime (arbitrary time.Time, overflow-safe).
func genUUIDv7FromMs(ms int64, subNs int64) (string, error) {
	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// 12-bit sub-ms precision in rand_a field, matching PG's generate_uuidv7
	subMsPrec := (subNs * 4096) / 1_000_000
	b[6] = byte(subMsPrec >> 8)
	b[7] = byte(subMsPrec)
	if _, err := cryptorand.Read(b[8:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0F) | 0x70 // version 7
	b[8] = (b[8] & 0x3F) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// parseTimezoneOffsetString converts a timezone name or offset string to seconds
// east of UTC. Handles POSIX-style names (UTC+10 = 10h west = -36000s east),
// ISO offsets (+05:30, -07), TZ abbreviations (EST, PDT), and named zones.
// M0097-0004: used by the timezone() built-in (AT LOCAL / AT TIME ZONE).
func parseTimezoneOffsetString(s string) (int, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)

	// POSIX-style: "UTC+N", "GMT+N" — sign is INVERTED (west-positive convention).
	for _, pfx := range []string{"UTC+", "GMT+"} {
		if strings.HasPrefix(upper, pfx) {
			rest := s[len(pfx):]
			if h, m, ok := parseTZHourMin(rest); ok {
				return -(h*3600 + m*60), nil
			}
		}
	}
	for _, pfx := range []string{"UTC-", "GMT-"} {
		if strings.HasPrefix(upper, pfx) {
			rest := s[len(pfx):]
			if h, m, ok := parseTZHourMin(rest); ok {
				return h*3600 + m*60, nil
			}
		}
	}
	if upper == "UTC" || upper == "GMT" {
		return 0, nil
	}

	// ISO-style offset: "+HH", "-HH", "+HH:MM", "-HH:MM".
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		if off, ok := parseTZOffset(s); ok {
			return off, nil
		}
	}

	// Bare interval-style string like "10:00" or "-10:00" (from INTERVAL literal).
	if strings.Contains(s, ":") {
		if off, ok := parseTZOffset("+" + s); ok {
			return off, nil
		}
	}

	// TZ abbreviations (EST, PDT, etc.).
	if off, ok := tzAbbrevOffsets[upper]; ok {
		return off, nil
	}

	// Named timezone (America/New_York, Europe/London, etc.).
	if loc, err := time.LoadLocation(s); err == nil {
		_, off := time.Now().In(loc).Zone()
		return off, nil
	}

	return 0, fmt.Errorf("unrecognized timezone: %q", s)
}

// enumTypeNameFromArgs inspects planner-level argument expressions to find the
// enum type name for enum_first / enum_last / enum_range. Arguments are
// typically NULL::typename or value::typename casts; the CastExpr carries the
// TargetType. M0097-0063.
func enumTypeNameFromArgs(args []optimizer.Expr) string {
	for _, arg := range args {
		if cast, ok := arg.(*optimizer.CastExpr); ok {
			return cast.TargetType
		}
	}
	return ""
}

// isUnsafeEnumValue returns true when label is a "pending" enum value that was
// added by ALTER TYPE … ADD VALUE inside the current (uncommitted) explicit
// transaction.  Such values must not be used until COMMIT.
func isUnsafeEnumValue(ctx *Context, typeName, label string) bool {
	if ctx == nil || ctx.PendingEnumValues == nil {
		return false
	}
	pending := ctx.PendingEnumValues[strings.ToLower(typeName)]
	return pending != nil && pending[label]
}

// enumUnsafeError builds the "unsafe use of new value" ExecError.
func enumUnsafeError(label, typeName string, pos int) error {
	return &ExecError{
		Code:    "0A000",
		Pos:     pos,
		Message: fmt.Sprintf("unsafe use of new value %q of enum type %s", label, typeName),
		Hint:    "New enum values must be committed before they can be used.",
	}
}

// evalRowToRowComparison evaluates (a,b,...) OP (c,d,...) using element-wise
// comparison with standard SQL NULL semantics: if any compared element is NULL,
// the result is NULL for that step. Implements ISO SQL §8.7 row comparison.
// Used for WHERE (proname, pronamespace) > ('abs', 0) style predicates.
func evalRowToRowComparison(op parser.OpCode, left, right *optimizer.RowExpr, slot SlotView, ctx *Context) (Datum, error) {
	n := len(left.Elems)
	if len(right.Elems) < n {
		n = len(right.Elems)
	}
	for i := 0; i < n; i++ {
		lDat, err := evalExprSlot(left.Elems[i], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		rDat, err := evalExprSlot(right.Elems[i], slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if lDat.IsNull() || rDat.IsNull() {
			return NullDatum, nil
		}
		cmp, err := compareDatum(lDat, rDat, 0)
		if err != nil {
			return Datum{}, err
		}
		isLast := (i == n-1)
		if cmp < 0 {
			return NewBoolDatum(op == parser.OpLt || op == parser.OpLe || op == parser.OpNe), nil
		} else if cmp > 0 {
			return NewBoolDatum(op == parser.OpGt || op == parser.OpGe || op == parser.OpNe), nil
		}
		// Equal — if last element, apply equality part of operator
		if isLast {
			return NewBoolDatum(op == parser.OpEq || op == parser.OpLe || op == parser.OpGe), nil
		}
		// Continue to next element
	}
	// All elements equal (or n=0)
	return NewBoolDatum(op == parser.OpEq || op == parser.OpLe || op == parser.OpGe), nil
}

// evalRowExpr evaluates a row constructor `(a, b, c)` and returns its
// PostgreSQL composite text representation `(v1,v2,...,vN)`. NULL elements
// appear as empty fields. Used for whole-row variable refs. M0097-0020.
func evalRowExpr(x *optimizer.RowExpr, slot SlotView, ctx *Context) (Datum, error) {
	parts := make([]string, len(x.Elems))
	allNull := true
	for i, elem := range x.Elems {
		d, err := evalExprSlot(elem, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if d.IsNull() {
			parts[i] = ""
			continue
		}
		allNull = false
		s := string(d.AppendValueText(nil))
		// Quote values that need it in composite syntax: commas, parens,
		// double-quotes, backslashes, whitespace, or empty string.
		needsQuote := false
		if s == "" {
			needsQuote = true
		} else {
			for _, c := range s {
				if c == ',' || c == '(' || c == ')' || c == '"' || c == '\\' || c == ' ' || c == '\t' || c == '\n' {
					needsQuote = true
					break
				}
			}
		}
		if needsQuote {
			var b strings.Builder
			b.WriteByte('"')
			for _, c := range s {
				if c == '"' || c == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(c)
			}
			b.WriteByte('"')
			parts[i] = b.String()
		} else {
			parts[i] = s
		}
	}
	// When all elements are NULL AND the row has zero elements, return NullDatum.
	// For non-empty rows, even if all elements are NULL, return "()" to match
	// PostgreSQL's display of a row with all-null fields (e.g. SELECT foo FROM
	// (SELECT NULL) AS foo → "()", not NULL). M0097-0125.
	if allNull && len(parts) == 0 {
		return NullDatum, nil
	}
	return NewStringDatum("(" + strings.Join(parts, ",") + ")"), nil
}

// evalRowNullTest implements PostgreSQL's row-valued IS [NOT] NULL semantics
// for a RowExpr operand (e.g. a whole-row variable `tbl` expanded by the
// planner into a RowExpr of its column refs):
//
//	row IS NULL      → true iff EVERY field is null
//	row IS NOT NULL  → true iff EVERY field is non-null
//
// These are deliberately not inverses — a row mixing null and non-null fields
// returns false for both — and the rule applies recursively to nested row
// fields. This is what lets `whole_row IS NOT NULL` correctly report false for
// an outer-join non-match (all fields null), as in pg_amcheck's database
// resolution query `COUNT(*) FILTER (WHERE d IS NOT NULL)`. M0110-0003.
func evalRowNullTest(re *optimizer.RowExpr, negated bool, slot SlotView, ctx *Context) (bool, error) {
	for _, el := range re.Elems {
		if sub, ok := el.(*optimizer.RowExpr); ok {
			// Nested row: recurse with the same test (per SQL standard).
			ok2, err := evalRowNullTest(sub, negated, slot, ctx)
			if err != nil {
				return false, err
			}
			if !ok2 {
				return false, nil
			}
			continue
		}
		d, err := evalExprSlot(el, slot, ctx)
		if err != nil {
			return false, err
		}
		if negated {
			// IS NOT NULL: any null field fails the all-non-null requirement.
			if d.IsNull() {
				return false, nil
			}
		} else {
			// IS NULL: any non-null field fails the all-null requirement.
			if !d.IsNull() {
				return false, nil
			}
		}
	}
	return true, nil
}

// evalMergeWholeRow formats a pre-materialised row as a composite text value
// using the same quoting rules as evalRowExpr. Used for MERGE RETURNING
// old/new composite references (MergeWholeRowRef). M0100-0007.
func evalMergeWholeRow(row Row) Datum {
	parts := make([]string, len(row))
	for i, d := range row {
		if d.IsNull() {
			parts[i] = ""
			continue
		}
		s := string(d.AppendValueText(nil))
		needsQuote := s == ""
		if !needsQuote {
			for _, c := range s {
				if c == ',' || c == '(' || c == ')' || c == '"' || c == '\\' || c == ' ' || c == '\t' || c == '\n' {
					needsQuote = true
					break
				}
			}
		}
		if needsQuote {
			var b strings.Builder
			b.WriteByte('"')
			for _, c := range s {
				if c == '"' || c == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(c)
			}
			b.WriteByte('"')
			parts[i] = b.String()
		} else {
			parts[i] = s
		}
	}
	return NewStringDatum("(" + strings.Join(parts, ",") + ")")
}

// parseTZHourMin parses "HH" or "HH:MM" into hours and minutes.
func parseTZHourMin(s string) (h, m int, ok bool) {
	if idx := strings.Index(s, ":"); idx >= 0 {
		hh, err1 := strconv.Atoi(s[:idx])
		mm, err2 := strconv.Atoi(s[idx+1:])
		if err1 == nil && err2 == nil {
			return hh, mm, true
		}
		return 0, 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	return n, 0, true
}

// buildFunctionArguments returns the argument list string for
// pg_get_function_arguments() / pg_get_function_identity_arguments(). Format:
// "IN name type, OUT name type, ..." matching PG's print_function_arguments.
// printDefaults mirrors PG's print_defaults flag: pg_get_function_arguments
// passes true (so trailing input args carry their ` DEFAULT <expr>` clause),
// while pg_get_function_identity_arguments passes false (defaults omitted, since
// they are not part of the function's ALTER/DROP identity).
// routineOrAggregateArgs resolves oid to a *catalog.Routine for
// pg_get_function_arguments/pg_get_function_identity_arguments. A CREATE
// AGGREGATE registers its identity in catalog.UserAggregate, not
// catalog.Routines — synthesize a throwaway Routine carrying just its
// argument types (no names/modes/defaults, matching a simple aggregate's
// unnamed-parameter signature, e.g. `newavg(integer)`) so the shared
// arg-list renderer works for both routines and aggregates. DU-002 slice 405.
func routineOrAggregateArgs(cat catalog.Catalog, oid uint32) *catalog.Routine {
	if rs := cat.Routines(); rs != nil {
		if r := rs.LookupByOID(oid); r != nil {
			return r
		}
	}
	for _, agg := range cat.ListUserAggregates() {
		if agg.OID != oid {
			continue
		}
		argTypes := make([]catalog.Type, len(agg.ArgTypes))
		for i, t := range agg.ArgTypes {
			argTypes[i] = catalog.Type{Name: t}
		}
		return &catalog.Routine{ArgTypes: argTypes}
	}
	return nil
}

func buildFunctionArguments(r *catalog.Routine, printDefaults bool) string {
	if len(r.ArgTypes) == 0 {
		return ""
	}
	// Procedures always show mode prefixes (IN/OUT/INOUT/VARIADIC).
	// Functions only show mode prefix when at least one param is OUT or INOUT;
	// if all params are IN/VARIADIC, omit the prefix (PG compat).
	showMode := r.IsProcedure
	if !showMode {
		for _, m := range r.ArgModes {
			// For RETURNS TABLE the OUT-mode args are table columns; they belong to
			// the result (rendered by pg_get_function_result), not the arg list, so
			// they must not flip showMode on. PG's print_function_arguments excludes
			// PROARGMODE_TABLE args entirely.
			if r.ReturnsTable && m == "o" {
				continue
			}
			if m == "o" || m == "b" {
				showMode = true
				break
			}
		}
	}
	parts := make([]string, 0, len(r.ArgTypes))
	for i, argType := range r.ArgTypes {
		// Skip RETURNS TABLE columns: they are stored as trailing OUT args but
		// surface only in the RETURNS TABLE(...) result clause, never the arg list.
		if r.ReturnsTable && i < len(r.ArgModes) && r.ArgModes[i] == "o" {
			continue
		}
		var part strings.Builder
		// Mode prefix. OUT/INOUT/VARIADIC always carry their prefix (matching
		// print_function_arguments, which prints every non-default mode regardless
		// of routine kind); the bare IN prefix is only emitted when showMode is set
		// (procedures, or functions that carry an OUT/INOUT arg).
		if r.ArgModes != nil && i < len(r.ArgModes) {
			switch r.ArgModes[i] {
			case "o":
				part.WriteString("OUT ")
			case "b":
				part.WriteString("INOUT ")
			case "v":
				part.WriteString("VARIADIC ")
			default: // "i" or ""
				if showMode {
					part.WriteString("IN ")
				}
			}
		} else if showMode {
			part.WriteString("IN ")
		}
		// Name (if any)
		if i < len(r.ArgNames) && r.ArgNames[i] != "" {
			part.WriteString(r.ArgNames[i])
			part.WriteByte(' ')
		}
		oid := uint32(0)
		if r.ArgTypeOIDs != nil && i < len(r.ArgTypeOIDs) {
			oid = r.ArgTypeOIDs[i]
		}
		part.WriteString(canonicalTypeName(argType.Name, oid))
		// DEFAULT clause. PG's print_function_arguments appends ` DEFAULT <expr>`
		// only when print_defaults is set AND the argument is an input arg
		// (IN/INOUT/VARIADIC) — output args never carry a default. goopg stores the
		// deparse-canonical default expression positionally in ArgDefaults.
		if printDefaults && i < len(r.ArgDefaults) && r.ArgDefaults[i] != "" && argIsInput(r.ArgModes, i) {
			part.WriteString(" DEFAULT ")
			part.WriteString(r.ArgDefaults[i])
		}
		parts = append(parts, part.String())
	}
	return strings.Join(parts, ", ")
}

// buildTableResult renders the `TABLE(name type, ...)` result clause for a
// RETURNS TABLE function, matching PG's pg_get_function_result (ruleutils.c,
// PROARGMODE_TABLE branch). The table columns are stored as the routine's
// trailing OUT args (mode "o"); pg_dump consumes this string verbatim for the
// RETURNS clause.
func buildTableResult(r *catalog.Routine) string {
	var cols []string
	for i := range r.ArgTypes {
		if i >= len(r.ArgModes) || r.ArgModes[i] != "o" {
			continue
		}
		name := ""
		if i < len(r.ArgNames) {
			name = r.ArgNames[i]
		}
		part := name
		if part != "" {
			part += " "
		}
		oid := uint32(0)
		if r.ArgTypeOIDs != nil && i < len(r.ArgTypeOIDs) {
			oid = r.ArgTypeOIDs[i]
		}
		part += canonicalTypeName(r.ArgTypes[i].Name, oid)
		cols = append(cols, part)
	}
	if len(cols) == 0 {
		// Defensive: a RETURNS TABLE with no recoverable columns falls back to the
		// SETOF record form rather than emitting an empty, unparsable TABLE().
		return "SETOF record"
	}
	return "TABLE(" + strings.Join(cols, ", ") + ")"
}

// argIsInput reports whether the argument at index i is an input argument
// (IN/INOUT/VARIADIC) — the only modes that can carry a DEFAULT. A nil/short
// ArgModes slice means all-IN (per catalog.Routine.ArgModes convention).
func argIsInput(modes []string, i int) bool {
	if modes == nil || i >= len(modes) {
		return true // all-IN
	}
	switch modes[i] {
	case "o": // OUT
		return false
	default: // "i" (IN), "b" (INOUT), "v" (VARIADIC), "" (defaults to IN)
		return true
	}
}

// buildFunctionDef reconstructs the CREATE FUNCTION / CREATE PROCEDURE DDL
// for pg_get_functiondef(). Matches PostgreSQL's output format: schema-qualified
// name, arg types, optional modifiers (STABLE/STRICT/etc.), then body.
func buildFunctionDef(r *catalog.Routine) string {
	var sb strings.Builder

	// Header: CREATE OR REPLACE FUNCTION/PROCEDURE schema.name(args)
	if r.IsProcedure {
		sb.WriteString("CREATE OR REPLACE PROCEDURE ")
	} else {
		sb.WriteString("CREATE OR REPLACE FUNCTION ")
	}
	// Schema-qualified name (lowercase matches pg_get_functiondef)
	if r.Schema != "" {
		sb.WriteString(r.Schema)
		sb.WriteByte('.')
	}
	sb.WriteString(r.Name)
	sb.WriteByte('(')
	wroteArg := false
	for i, argType := range r.ArgTypes {
		// RETURNS TABLE columns are stored as trailing OUT args but render in the
		// RETURNS TABLE(...) clause, not the arg list (sibling of buildFunctionArguments).
		if r.ReturnsTable && i < len(r.ArgModes) && r.ArgModes[i] == "o" {
			continue
		}
		if wroteArg {
			sb.WriteString(", ")
		}
		wroteArg = true
		// Mode prefix: OUT/INOUT/VARIADIC are emitted for both functions and
		// procedures; the bare IN prefix is procedure-only (sibling of
		// buildFunctionArguments).
		if r.ArgModes != nil && i < len(r.ArgModes) {
			switch r.ArgModes[i] {
			case "o":
				sb.WriteString("OUT ")
			case "b":
				sb.WriteString("INOUT ")
			case "v":
				sb.WriteString("VARIADIC ")
			case "i", "":
				if r.IsProcedure {
					sb.WriteString("IN ")
				}
			}
		}
		if i < len(r.ArgNames) && r.ArgNames[i] != "" {
			sb.WriteString(r.ArgNames[i])
			sb.WriteByte(' ')
		}
		oid := uint32(0)
		if r.ArgTypeOIDs != nil && i < len(r.ArgTypeOIDs) {
			oid = r.ArgTypeOIDs[i]
		}
		sb.WriteString(canonicalTypeName(argType.Name, oid))
		// DEFAULT clause: pg_get_functiondef calls print_function_arguments with
		// print_defaults=true, so input args carry their ` DEFAULT <expr>` (sibling
		// of buildFunctionArguments; output args never have a default).
		if i < len(r.ArgDefaults) && r.ArgDefaults[i] != "" && argIsInput(r.ArgModes, i) {
			sb.WriteString(" DEFAULT ")
			sb.WriteString(r.ArgDefaults[i])
		}
	}
	sb.WriteString(")\n")

	// RETURNS clause (functions only) — 1-space indent like PG's deparser
	if !r.IsProcedure {
		sb.WriteString(" RETURNS ")
		if r.ReturnsTable {
			// RETURNS TABLE(col type, ...) — rendered from the trailing OUT args.
			sb.WriteString(buildTableResult(r))
		} else {
			if r.ReturnsSet {
				sb.WriteString("SETOF ")
			}
			sb.WriteString(canonicalTypeName(r.ReturnType.Name, r.ReturnTypeOID))
		}
		sb.WriteByte('\n')
	}

	// LANGUAGE clause — 1-space indent
	sb.WriteString(" LANGUAGE ")
	sb.WriteString(r.Language)
	sb.WriteByte('\n')

	// Volatility modifiers (STABLE, IMMUTABLE — VOLATILE is the default, omitted)
	switch r.Volatile {
	case "s":
		sb.WriteString(" STABLE\n")
	case "i":
		sb.WriteString(" IMMUTABLE\n")
	}

	// STRICT
	if r.Strict {
		sb.WriteString(" STRICT\n")
	}

	// LEAKPROOF
	if r.Leakproof {
		sb.WriteString(" LEAKPROOF\n")
	}

	// SECURITY DEFINER
	if r.SecurityDefiner {
		sb.WriteString(" SECURITY DEFINER\n")
	}

	// PARALLEL SAFE / RESTRICTED (UNSAFE is the default, omitted — matches PG's deparser)
	switch r.Parallel {
	case "s":
		sb.WriteString(" PARALLEL SAFE\n")
	case "r":
		sb.WriteString(" PARALLEL RESTRICTED\n")
	}

	// COST — emitted only when non-default, matching pg_get_functiondef
	// (ruleutils.c): default is 1 for internal/C, 100 otherwise.
	if r.Cost != "" {
		defaultCost := "100"
		switch strings.ToLower(r.Language) {
		case "internal", "c":
			defaultCost = "1"
		}
		if r.Cost != defaultCost {
			sb.WriteString(" COST ")
			sb.WriteString(r.Cost)
			sb.WriteByte('\n')
		}
	}

	// ROWS — set-returning functions only; omitted at the 1000 default.
	if r.ReturnsSet && r.Rows != "" && r.Rows != "0" && r.Rows != "1000" {
		sb.WriteString(" ROWS ")
		sb.WriteString(r.Rows)
		sb.WriteByte('\n')
	}

	// Body
	body := r.Body
	if r.BeginAtomic {
		// Substitute $N positional parameter references with named parameters
		// to match PG's decompiled output format.
		if len(r.ArgNames) > 0 {
			for i, name := range r.ArgNames {
				if name != "" {
					body = strings.ReplaceAll(body, fmt.Sprintf("$%d", i+1), name)
				}
			}
		}
		// SQL-standard BEGIN ATOMIC ... END body
		sb.WriteString("BEGIN ATOMIC\n")
		// Each statement in the body on its own line with leading space
		for _, stmt := range strings.Split(body, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			sb.WriteString(" ")
			sb.WriteString(stmt)
			sb.WriteString(";\n")
		}
		sb.WriteString("END\n")
	} else if r.IsReturnForm {
		// RETURN form: stored as "SELECT expr" — output as "RETURN expr"
		sb.WriteString("RETURN ")
		sb.WriteString(strings.TrimPrefix(body, "SELECT "))
		sb.WriteByte('\n')
	} else if r.IsProcedure {
		// Procedure body: multi-line $procedure$...$procedure$ format
		sb.WriteString("AS $procedure$\n")
		sb.WriteString(strings.TrimLeft(body, "\n"))
		if body != "" && body[len(body)-1] != '\n' {
			sb.WriteByte('\n')
		}
		sb.WriteString("$procedure$\n")
	} else {
		// Function body: inline $function$...$function$ format
		sb.WriteString("AS $function$")
		sb.WriteString(body)
		sb.WriteString("$function$\n")
	}

	return sb.String()
}

// canonicalTypeName normalizes short PG type aliases to their canonical names,
// matching what pg_get_functiondef displays (e.g. "bool" → "boolean").
// pgTypeofNameFromPlanType converts a planner catalog type name to the string
// that pg_typeof returns for that type. Mirrors PostgreSQL's format_type_be.
// M0097-0155.
func pgTypeofNameFromPlanType(name string) string {
	switch strings.ToLower(name) {
	case "int4", "int", "integer", "serial":
		return "integer"
	case "int2", "smallint", "smallserial":
		return "smallint"
	case "int8", "bigint", "bigserial":
		return "bigint"
	case "float4", "real":
		return "real"
	case "float8", "double precision", "float":
		return "double precision"
	case "bool", "boolean":
		return "boolean"
	case "text":
		return "text"
	case "varchar", "character varying":
		return "character varying"
	case "char", "character", "bpchar":
		return "character"
	case "numeric", "decimal":
		return "numeric"
	case "timestamp", "timestamp without time zone":
		return "timestamp without time zone"
	case "timestamptz", "timestamp with time zone":
		return "timestamp with time zone"
	case "date":
		return "date"
	case "time", "time without time zone":
		return "time without time zone"
	case "timetz", "time with time zone":
		return "time with time zone"
	case "interval":
		return "interval"
	case "bytea":
		return "bytea"
	case "uuid":
		return "uuid"
	case "json":
		return "json"
	case "jsonb":
		return "jsonb"
	case "oid":
		return "oid"
	default:
		return name
	}
}

func canonicalTypeName(name string, oid uint32) string {
	// Handle array types (e.g. "text[]") by canonicalizing the base type.
	if strings.HasSuffix(name, "[]") {
		base := canonicalTypeName(name[:len(name)-2], oid)
		return base + "[]"
	}
	switch strings.ToLower(name) {
	case "bool":
		return "boolean"
	case "int", "int4":
		return "integer"
	case "int2":
		return "smallint"
	case "int8":
		return "bigint"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "varchar":
		return "character varying"
	case "char":
		// CHAROID (18) renders as the quoted pseudo-name "char" to match PG's
		// format_type_extended: BPCHAROID maps to "character", and there is no
		// CHAROID case, so the default quote_qualified_identifier path yields
		// `"char"` (postgres/src/backend/utils/adt/format_type.c:207-220,303-322).
		// OID 0 (no-OID baseline: aggregates, pre-90th routines, error-text path)
		// and 1042 (bpchar) both render "character". Row 1364: the array OIDs
		// (quoted `"char"[]` → OIDArrayChar 1002, bare `char[]` → OIDArrayBpChar
		// 1014) flow through this same arm via the recursive array-suffix split
		// above, so the quoted arm must accept OIDArrayChar too — otherwise
		// `"char"[]` degrades to `character[]`.
		if oid == catalog.OIDChar || oid == catalog.OIDArrayChar { // 18 / 1002
			return `"char"`
		}
		return "character" // 1042 (bpchar), 1014 (char[]), AND 0 (no-OID baseline)
	}
	return name
}

// evalPgClientEncoding returns the current session's client_encoding as a name,
// mirroring PG's pg_client_encoding() (postgres/src/backend/utils/adt/mb/pg_wchar.c).
// It reads the live client_encoding GUC value; falls back to "UTF8" when the
// setting is unavailable (nil context / nil GetSetting). M0122-0008.
func evalPgClientEncoding(ctx *Context) (Datum, error) {
	enc := "UTF8"
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("client_encoding"); ok {
			enc = v
		}
	}
	return NewStringDatum(enc), nil
}

// evalGetDatabaseEncoding returns the current database's encoding as a name,
// mirroring PG's getdatabaseencoding() (postgres/src/backend/utils/adt/dbsize.c).
// It reads the encoding ID from the in-memory catalog and maps it to a canonical
// name; falls back to "UTF8" when the context or catalog is unavailable.
// M0122-0008.
func evalGetDatabaseEncoding(ctx *Context) (Datum, error) {
	if ctx == nil || ctx.Catalog == nil {
		return NewStringDatum("UTF8"), nil
	}
	dbName := ctx.CurrentDatabase
	if dbName == "" {
		dbName = "postgres"
	}
	// DatabaseEncoding lives on *InMemory, not the Catalog interface. Type-assert
	// so this works against the production implementation; the fallback covers
	// tests that supply a different Catalog implementation.
	var encID int32 = -1
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		encID = im.DatabaseEncoding(dbName)
	}
	encName := catalog.EncodingIDToName(encID)
	if encName == "" {
		encName = "UTF8"
	}
	return NewStringDatum(encName), nil
}
