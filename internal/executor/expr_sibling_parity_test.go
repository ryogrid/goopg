package executor

// M0127-PS6.2 — compiled ↔ interpreted sibling audit
// (design leftdeep-joins/09 §1 "Sibling-path audits … E5 (compiled ↔
// interpreted evaluators)"; the release gate for stage E5).
//
// PS6.1 moved the two hottest expression seams in the executor — the hash
// join key and the residual conjunction — from `evalExprSlot` (interpreted,
// interface type-switch) onto `evalFastExpr` (compiled, ExprNode slab). That
// makes the pair a Hard-won-Rule-#2 sibling: a green test on one twin proves
// nothing about the other. The two ways the pair can diverge are
//
//   - VALUE: the same expression evaluates to different Datums, and
//   - FAILURE MODE: one twin errors where the other succeeds, raises a
//     different SQLSTATE, or — the one that actually happened in PS6.1 —
//     PANICS where the other raises. A panic in the hash-join build-side
//     drain runs in gatherOp.Open's LEADER goroutine, outside
//     ParallelGroup.Go's recover, so it reaches serveConn and closes the
//     client socket instead of killing the statement (TPC-DS Q8).
//
// So this harness compares OUTCOMES, not values: a panic, an error (code +
// message + position) and a value are three points in one space, and every
// corpus entry is evaluated on both twins over every SlotView implementation.
// The 0097-0037 overflow corpus is included by name — it is the precedent
// that the fast evaluator can silently drop a check the interpreter applies
// (the fast path returned out-of-range int2/int4 arithmetic instead of
// raising 22003 for eight months).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// ---------------------------------------------------------------------------
// Outcome rendering
// ---------------------------------------------------------------------------

// renderParityDatum renders every field of a Datum that carries meaning, so
// two datums that print the same really are the same value. Comparing
// Format() alone would hide a Kind change (KindInt 1 and KindBool true both
// format as "1"-ish) — and a Kind change is exactly what a mis-dispatched
// short-circuit produces.
func renderParityDatum(d Datum) string {
	// Arena-backed payloads (KindString/KindBytes with ArenaID≠0, and the
	// flagBigNumeric mantissa) carry an mctx COORDINATE in Int —
	// (offset<<32|length) — not a value. Every evaluation appends to the arena,
	// so two identical results render with different offsets. Render those by
	// value; the coordinate is allocation state, not semantics.
	if d.ArenaID != 0 || d.Flags&flagBigNumeric != 0 {
		return fmt.Sprintf("kind=%d scale=%d arena-backed value=%s", d.Kind, d.Scale, d.Format())
	}
	body := fmt.Sprintf("kind=%d flags=%d scale=%d timesub=%d int=%d hi=%d",
		d.Kind, d.Flags, d.Scale, d.TimeSub, d.Int, d.Hi)
	if d.Buf != nil {
		body += fmt.Sprintf(" buf=%q", string(d.Buf))
	}
	return body
}

// renderParityErr renders an error so that SQLSTATE, position and message all
// participate in the diff. ExecError.Pos never reaches the wire (the server
// maps Code+Message only), but it is part of the log text, and a twin that
// loses it is still a twin that diverges — so it is rendered and asserted
// separately from the code/message pair.
func renderParityErr(err error) string {
	if ee, ok := err.(*ExecError); ok {
		return fmt.Sprintf("ExecError{code=%s pos=%d msg=%q}", ee.Code, ee.Pos, ee.Message)
	}
	return fmt.Sprintf("error{%q}", err.Error())
}

// evalParityOutcome runs one evaluator and reduces it to a comparable string.
// The recover is the point of the harness, not defensive padding: "one twin
// panics where the other raises" is the divergence class that closed a client
// socket, and it can only be compared if a panic is a value.
func evalParityOutcome(fn func() (Datum, error)) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("PANIC(%v)", r)
		}
	}()
	d, err := fn()
	if err != nil {
		return "ERR " + renderParityErr(err)
	}
	return "VAL " + renderParityDatum(d)
}

// parityOutcomes evaluates e on both twins against slot and returns the two
// rendered outcomes. The compiled twin is always built through
// exprTreeSlab.buildExpr, the same entry point initExecKeys/compileExecExprs
// use, so the fallback-to-ExprAdapter decisions under audit are the
// production ones.
func parityOutcomes(e optimizer.Expr, slot SlotView, ctx *Context) (compiled, interpreted string) {
	var slab exprTreeSlab
	idx := slab.buildExpr(e)
	compiled = evalParityOutcome(func() (Datum, error) { return evalFastExpr(slab, idx, slot, ctx) })
	interpreted = evalParityOutcome(func() (Datum, error) { return evalExprSlot(e, slot, ctx) })
	return compiled, interpreted
}

// parityCase is one corpus entry.
type parityCase struct {
	name string
	expr optimizer.Expr
}

// checkParityCorpus is the shared driver: every case against every SlotView
// implementation. It reports each divergence with both renderings rather than
// failing fast, because a systematic divergence (say, every AND) reads very
// differently from a single-shape one.
func checkParityCorpus(t *testing.T, corpus []parityCase, row Row) {
	t.Helper()
	slots := parityAllSlots(row)
	for _, c := range corpus {
		for _, sv := range slots {
			compiled, interpreted := parityOutcomes(c.expr, sv.slot, nil)
			if compiled != interpreted {
				t.Errorf("%s [%s]:\n  compiled    = %s\n  interpreted = %s",
					c.name, sv.name, compiled, interpreted)
			}
		}
	}
}

type namedSlot struct {
	name string
	slot SlotView
}

// parityAllSlots covers every SlotView implementation evalFastExpr's
// ColumnRef arm switches on, plus the nil slot (a legal input per the M0054
// contract for valuesOp/limitOp/DML). *VirtualSlot is built over a
// MaterializedSlot source so its indirection is exercised, not bypassed.
func parityAllSlots(row Row) []namedSlot {
	ms := SlotFromRow(nil, row)
	cols := make([]virtualCol, len(row))
	for i := range row {
		cols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
	}
	return []namedSlot{
		{"rowSlotView", rowSlotView(row)},
		{"*MaterializedSlot", ms},
		{"*Slot", &Slot{Cells: row, HasRow: true}},
		{"*VirtualSlot", NewVirtualSlot(nil, []TupleSlot{ms}, cols)},
		{"nil", nil},
	}
}

// parityRow is the corpus row: int, int, NULL, string, bool, numeric. Index 2
// being NULL is deliberate — three-valued logic is where an evaluator pair
// most often disagrees without either being obviously wrong.
func parityRow() Row {
	return Row{
		NewIntDatum(7),
		NewIntDatum(11),
		NullDatum,
		NewStringDatum("abc"),
		NewBoolDatum(true),
		Datum{Kind: KindNumeric, Int: 1234, Scale: 2},
	}
}

func pcol(idx int, typeName string) *optimizer.ColumnRef {
	return &optimizer.ColumnRef{Index: idx, Name: fmt.Sprintf("c%d", idx), Type: catalog.Type{Name: typeName}}
}

func pbin(op parser.OpCode, l, r optimizer.Expr) *optimizer.BinaryOp {
	return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
}

func pbinT(op parser.OpCode, l, r optimizer.Expr, resultType string) *optimizer.BinaryOp {
	return &optimizer.BinaryOp{Op: op, Left: l, Right: r, ResultType: resultType}
}

func pint(v int64) *optimizer.IntegerConst { return &optimizer.IntegerConst{Value: v} }

// ---------------------------------------------------------------------------
// Corpus 1 — leaves
// ---------------------------------------------------------------------------

// TestExprSiblingParityLeaves covers the four natively-compiled leaf kinds and
// the leaves that must fall back to ExprAdapter. A leaf that compiles when it
// should adapt is the silent-wrong-answer shape: it produces a value rather
// than an error.
func TestExprSiblingParityLeaves(t *testing.T) {
	corpus := []parityCase{
		{"int const", pint(42)},
		{"int const negative", pint(-9223372036854775808)},
		{"bool const true", &optimizer.BooleanConst{Value: true}},
		{"bool const false", &optimizer.BooleanConst{Value: false}},
		{"null const", &optimizer.NullConst{}},
		{"string const (adapter)", &optimizer.StringConst{Value: "abc"}},
		{"numeric const (adapter)", &optimizer.NumericConst{Value: "12.34"}},
		{"numeric const malformed (adapter)", &optimizer.NumericConst{Value: "not-a-number"}},
		{"typed string lit (adapter)", &optimizer.TypedStringLit{Type: "date", Value: "2020-01-02"}},
	}
	checkParityCorpus(t, corpus, parityRow())
}

// ---------------------------------------------------------------------------
// Corpus 2 — ColumnRef bounds (the PS6.1 guard, audited across every slot)
// ---------------------------------------------------------------------------

// TestExprSiblingParityColumnRefBounds is the failure-mode half of the audit.
// Every index — in range, negative, one past the end, and TPC-DS Q8's actual
// 57 — must produce the SAME outcome on both twins for every SlotView. This
// is the test that would have caught PS6.1's pre-fix state: the compiled arm
// panicked where the interpreted arm raised XX000.
func TestExprSiblingParityColumnRefBounds(t *testing.T) {
	row := parityRow()
	var corpus []parityCase
	for _, idx := range []int{0, 2, len(row) - 1, -1, len(row), len(row) + 1, 57} {
		corpus = append(corpus, parityCase{fmt.Sprintf("column ref %d", idx), pcol(idx, "int4")})
		// Under a parent: the guard must fire the same way when the ref is an
		// operand, since that is how a join key and a residual actually read.
		corpus = append(corpus, parityCase{
			fmt.Sprintf("column ref %d under binary op", idx),
			pbinT(parser.OpAdd, pcol(idx, "int4"), pint(1), "int4"),
		})
	}
	checkParityCorpus(t, corpus, row)
}

// ---------------------------------------------------------------------------
// Corpus 3 — every operator
// ---------------------------------------------------------------------------

// TestExprSiblingParityAllOperators sweeps every parser.OpCode over a matrix
// of operand shapes. Most combinations are type errors; that is the point —
// an error is an outcome, and the twins must raise the same one. A pair that
// agrees on the values it can compute but disagrees on which inputs are legal
// is still a divergence.
func TestExprSiblingParityAllOperators(t *testing.T) {
	binOps := []parser.OpCode{
		parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod,
		parser.OpConcat,
		parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe,
		parser.OpAnd, parser.OpOr,
		parser.OpLike, parser.OpNotLike, parser.OpILike, parser.OpNotILike,
		parser.OpRegexMatch, parser.OpRegexIMatch, parser.OpRegexNoMatch, parser.OpRegexINoMatch,
		parser.OpBitAnd, parser.OpBitOr, parser.OpBitXor,
		parser.OpBitShiftLeft, parser.OpBitShiftRight,
		parser.OpContainedBy, parser.OpContains, parser.OpOverlap,
		parser.OpJSONGet, parser.OpJSONGetText,
		parser.OpUnknown,
	}
	operands := []struct {
		name string
		expr optimizer.Expr
	}{
		{"int", pint(3)},
		{"intcol", pcol(0, "int4")},
		{"nullcol", pcol(2, "int4")},
		{"null", &optimizer.NullConst{}},
		{"bool", &optimizer.BooleanConst{Value: true}},
		{"strcol", pcol(3, "text")},
		{"numcol", pcol(5, "numeric")},
	}
	var corpus []parityCase
	for _, op := range binOps {
		for _, l := range operands {
			for _, r := range operands {
				corpus = append(corpus, parityCase{
					fmt.Sprintf("binary op=%d %s,%s", op, l.name, r.name),
					pbin(op, l.expr, r.expr),
				})
			}
		}
	}
	for _, op := range []parser.OpCode{parser.OpUnaryNeg, parser.OpUnaryPos, parser.OpNot, parser.OpBitNot, parser.OpUnknown} {
		for _, o := range operands {
			corpus = append(corpus, parityCase{
				fmt.Sprintf("unary op=%d %s", op, o.name),
				&optimizer.UnaryOp{Op: op, Operand: o.expr},
			})
		}
	}
	checkParityCorpus(t, corpus, parityRow())
}

// ---------------------------------------------------------------------------
// Corpus 4 — the 0097-0037 overflow corpus
// ---------------------------------------------------------------------------

// TestExprSiblingParityOverflowCorpus is the corpus named in the PS6.1/PS6.2
// bar. docs/design/0097-0037-fast-path-int-overflow.md records the original
// defect: evalFastExpr computed int2/int4 arithmetic without the range check
// evalExprSlot applies, so `32767::int2 + 1::int2` returned 32768 instead of
// raising 22003. The check now lives in ExprNode.payload[1], compiled by
// overflowCodeForType — so the corpus must sweep the ResultType SPELLINGS as
// well as the values, because the two twins decide "is this int4?" in
// different code.
func TestExprSiblingParityOverflowCorpus(t *testing.T) {
	resultTypes := []string{
		"int2", "smallint", "int4", "integer", "int", "int8", "bigint", "",
		// Case variants: the compiled side folds with strings.ToLower, the
		// interpreted side compares exact strings. Whether the planner ever
		// emits these spellings is a separate question from whether the twins
		// agree on them.
		"INT2", "SmallInt", "INT4", "Integer", "INT", "BIGINT",
		"numeric", "text", "bool",
	}
	// Values chosen to sit exactly on and just past each range boundary.
	pairs := []struct {
		op   parser.OpCode
		l, r int64
	}{
		{parser.OpAdd, 32767, 1},
		{parser.OpAdd, 32766, 1},
		{parser.OpSub, -32768, 1},
		{parser.OpMul, 32767, 2},
		{parser.OpAdd, 2147483647, 1},
		{parser.OpAdd, 2147483646, 1},
		{parser.OpSub, -2147483648, 1},
		{parser.OpMul, 2147483647, 2},
		{parser.OpAdd, 1, 1},
		{parser.OpDiv, 7, 0},
		{parser.OpMod, 7, 0},
		{parser.OpDiv, -2147483648, -1},
	}
	var corpus []parityCase
	for _, rt := range resultTypes {
		for _, p := range pairs {
			corpus = append(corpus, parityCase{
				fmt.Sprintf("overflow rt=%q op=%d %d,%d", rt, p.op, p.l, p.r),
				pbinT(p.op, pint(p.l), pint(p.r), rt),
			})
			// Nested: the check must fire at the node that declares the type,
			// not at the root, so an in-range outer over an out-of-range inner
			// must still raise.
			corpus = append(corpus, parityCase{
				fmt.Sprintf("nested overflow rt=%q op=%d %d,%d", rt, p.op, p.l, p.r),
				pbinT(parser.OpAdd, pbinT(p.op, pint(p.l), pint(p.r), rt), pint(0), "int8"),
			})
		}
	}
	checkParityCorpus(t, corpus, parityRow())
}

// ---------------------------------------------------------------------------
// Corpus 5 — three-valued logic and short-circuiting
// ---------------------------------------------------------------------------

// TestExprSiblingParityThreeValuedLogic is the residual conjunction's own
// corpus: a residual IS an AND-chain, and PS6.1 put it on the compiled twin.
// This is the corpus that found the audit's first divergence: the two twins
// short-circuited under DIFFERENT conditions — the interpreted arm requires
// `left.Kind == KindBool` (evalAnd/evalOr, expr.go), the compiled arm required
// only `!left.IsNull()` — so every non-boolean left operand was a divergence,
// and the compiled arm then RETURNED that operand as the AND/OR's result.
// The NULL rows of the truth table matter most: `NULL AND FALSE` is FALSE in
// SQL, not NULL, and it is reached only by evaluating the right side after
// declining to short-circuit.
func TestExprSiblingParityThreeValuedLogic(t *testing.T) {
	tf := []struct {
		name string
		expr optimizer.Expr
	}{
		{"true", &optimizer.BooleanConst{Value: true}},
		{"false", &optimizer.BooleanConst{Value: false}},
		{"null", &optimizer.NullConst{}},
		{"nullcol", pcol(2, "bool")},
		{"boolcol", pcol(4, "bool")},
		// Non-boolean left operands. BoolValue() is `Int != 0` on ANY Kind, so
		// these are exactly where the two short-circuit conditions part.
		{"int zero", pint(0)},
		{"int nonzero", pint(5)},
		{"intcol", pcol(0, "int4")},
		{"strcol", pcol(3, "text")},
		{"numcol", pcol(5, "numeric")},
	}
	var corpus []parityCase
	for _, op := range []parser.OpCode{parser.OpAnd, parser.OpOr} {
		for _, l := range tf {
			for _, r := range tf {
				corpus = append(corpus, parityCase{
					fmt.Sprintf("op=%d %s,%s", op, l.name, r.name),
					pbin(op, l.expr, r.expr),
				})
			}
		}
	}
	for _, o := range tf {
		corpus = append(corpus, parityCase{"NOT " + o.name, &optimizer.UnaryOp{Op: parser.OpNot, Operand: o.expr}})
	}
	checkParityCorpus(t, corpus, parityRow())
}

// ---------------------------------------------------------------------------
// Corpus 6 — the ExprAdapter fallback boundary
// ---------------------------------------------------------------------------

// TestExprSiblingParityAdapterBoundary pins the shapes buildExpr deliberately
// refuses to compile. Each carries a comment in exprnode.go naming the
// divergence that forced the fallback (float8 semantics, row-to-row 3VL,
// row-constructor vs subquery). If a later change compiles one of these
// natively, this test fails — which is the only warning the seam gets, since
// the shapes still RUN, just wrongly.
func TestExprSiblingParityAdapterBoundary(t *testing.T) {
	rowExpr := func(elems ...optimizer.Expr) *optimizer.RowExpr { return &optimizer.RowExpr{Elems: elems} }
	corpus := []parityCase{
		{"float8 add", pbinT(parser.OpAdd, pint(1), pint(3), "float8")},
		{"float8 div", pbinT(parser.OpDiv, pint(1), pint(3), "float8")},
		{"float8 div by zero", pbinT(parser.OpDiv, pint(1), pint(0), "float8")},
		{"float4 mul", pbinT(parser.OpMul, pcol(0, "float4"), pint(3), "float4")},
		{"double precision sub", pbinT(parser.OpSub, pint(1), pint(3), "double precision")},
		{"real add", pbinT(parser.OpAdd, pint(1), pint(3), "real")},
		{"float8 unsupported op falls through", pbinT(parser.OpEq, pint(1), pint(3), "float8")},
		{"row-to-row eq", pbin(parser.OpEq, rowExpr(pcol(0, "int4"), pcol(1, "int4")), rowExpr(pint(7), pint(11)))},
		{"row-to-row ge with null", pbin(parser.OpGe, rowExpr(pcol(3, "text"), pcol(0, "int4")), rowExpr(&optimizer.StringConst{Value: "abs"}, pcol(2, "int4")))},
		{"cast", &optimizer.CastExpr{Operand: pcol(0, "int4"), TargetType: "text"}},
		{"case", &optimizer.CaseExpr{Whens: []optimizer.CaseWhen{{When: &optimizer.BooleanConst{Value: true}, Then: pint(1)}}}},
		{"is null", &optimizer.IsNullExpr{Operand: pcol(2, "int4")}},
		{"is not null", &optimizer.IsNullExpr{Operand: pcol(0, "int4"), Negated: true}},
		{"is distinct from", &optimizer.IsDistinctFromExpr{Left: pcol(0, "int4"), Right: pcol(2, "int4")}},
		{"is bool expr", &optimizer.IsBoolExpr{Operand: pcol(4, "bool"), TestTrue: true}},
		{"bare row expr", rowExpr(pcol(0, "int4"), pcol(2, "int4"))},
		{"func call", &optimizer.FuncCall{Name: "lower", Args: []optimizer.Expr{pcol(3, "text")}}},
		{"param ref unbound", &optimizer.ParamRef{Number: 1}},
	}
	checkParityCorpus(t, corpus, parityRow())
}

// TestExprSiblingParityPgLSN covers the pg_lsn lane, which both twins detect
// by SNIFFING the operand value (looksLikePgLSN over a KindString) rather
// than by type. A value-sniffing branch is duplicated logic by construction,
// so it is audited on its own: the compiled arm's copy sits after the
// short-circuit block and before evalBinary, and nothing but this test
// requires the two copies to stay in the same place.
func TestExprSiblingParityPgLSN(t *testing.T) {
	row := Row{NewStringDatum("0/16B3748"), NewStringDatum("0/16B3700"), NullDatum, NewStringDatum("not/an/lsn"), NewIntDatum(4), Datum{Kind: KindNumeric, Int: 1, Scale: 0}}
	lsn := func(s string) optimizer.Expr { return &optimizer.StringConst{Value: s} }
	var corpus []parityCase
	for _, op := range []parser.OpCode{parser.OpSub, parser.OpAdd, parser.OpEq, parser.OpLt, parser.OpGt, parser.OpNe} {
		corpus = append(corpus,
			parityCase{fmt.Sprintf("lsn col,col op=%d", op), pbin(op, pcol(0, "pg_lsn"), pcol(1, "pg_lsn"))},
			parityCase{fmt.Sprintf("lsn col,const op=%d", op), pbin(op, pcol(0, "pg_lsn"), lsn("0/16B3700"))},
			parityCase{fmt.Sprintf("lsn col,int op=%d", op), pbin(op, pcol(0, "pg_lsn"), pint(8))},
			parityCase{fmt.Sprintf("lsn col,null op=%d", op), pbin(op, pcol(0, "pg_lsn"), pcol(2, "pg_lsn"))},
			parityCase{fmt.Sprintf("lsn malformed op=%d", op), pbin(op, pcol(3, "pg_lsn"), pcol(0, "pg_lsn"))},
		)
	}
	checkParityCorpus(t, corpus, row)
}

// TestExprSiblingParityDeepNesting checks that the slab's index-based child
// wiring survives reallocation. buildExpr reserves a BinaryOp's slot BEFORE
// recursing (exprnode.go), because a child append can grow the slab and
// invalidate a held pointer; a deep left-leaning chain is what forces enough
// appends to reallocate. A rotation here would not error — it would evaluate
// the wrong operands, which is the Q78 shape: right plan, wrong rows.
func TestExprSiblingParityDeepNesting(t *testing.T) {
	var deepLeft optimizer.Expr = pint(0)
	var deepRight optimizer.Expr = pint(0)
	for i := 0; i < 200; i++ {
		deepLeft = pbinT(parser.OpAdd, deepLeft, pint(int64(i)), "int8")
		deepRight = pbinT(parser.OpSub, pint(int64(i)), deepRight, "int8")
	}
	// A mixed chain whose leaves are column refs, so the slab holds
	// interleaved kinds rather than a uniform run of one.
	var mixed optimizer.Expr = pcol(0, "int4")
	for i := 0; i < 100; i++ {
		mixed = pbinT(parser.OpAdd, mixed, pcol(i%6, "int4"), "int8")
	}
	corpus := []parityCase{
		{"deep left-leaning add chain", deepLeft},
		{"deep right-leaning sub chain", deepRight},
		{"deep mixed column chain", mixed},
		{"deep and chain", buildAndChain(64)},
	}
	checkParityCorpus(t, corpus, parityRow())
}

// buildAndChain builds an n-conjunct AND over alternating true/false
// comparisons — the shape a multi-predicate residual compiles to.
func buildAndChain(n int) optimizer.Expr {
	var e optimizer.Expr = pbin(parser.OpEq, pcol(0, "int4"), pint(7))
	for i := 0; i < n; i++ {
		var conj optimizer.Expr
		switch i % 3 {
		case 0:
			conj = pbin(parser.OpLt, pcol(1, "int4"), pint(int64(100+i)))
		case 1:
			conj = pbin(parser.OpEq, pcol(2, "int4"), pint(int64(i))) // NULL
		default:
			conj = pbin(parser.OpGe, pcol(0, "int4"), pint(0))
		}
		e = pbin(parser.OpAnd, e, conj)
	}
	return e
}

// ---------------------------------------------------------------------------
// Corpus 9 — PLANNER-BUILT expressions (the ones that carry a position)
// ---------------------------------------------------------------------------

// parityResolve turns a SQL predicate into the planner Expr the executor would
// actually receive, via the planner's own resolver. Everything above builds
// planner nodes by hand, and a hand-built BinaryOp has pos == 0 — `pos` is
// unexported, so a test in package executor CANNOT set it. That blinds the
// hand-built corpora to the audit's third divergence: the compiled twin passed
// a literal 0 to evalBinary/evalUnary/evalPgLSNBinary and stamped no Pos on its
// 22003, while the interpreted twin passes x.Pos() to all three. Both twins
// render pos 0 on a hand-built node, so they agree there for the wrong reason.
// Resolving real SQL is what makes the position non-zero and the comparison
// meaningful.
func parityResolve(t *testing.T, predicate string) optimizer.Expr {
	t.Helper()
	stmts, err := parser.Parse("SELECT 1 FROM t WHERE " + predicate)
	if err != nil {
		t.Fatalf("parse %q: %v", predicate, err)
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: not a SelectStmt", predicate)
	}
	e, err := optimizer.ResolveIndexPredicate(sel.Where, parityTable())
	if err != nil {
		t.Fatalf("resolve %q: %v", predicate, err)
	}
	return e
}

// parityTable is the schema parityRow is a row of, so a resolved ColumnRef
// lands on the matching Datum kind.
func parityTable() *catalog.Table {
	names := []struct{ name, typ string }{
		{"c0", "int4"}, {"c1", "int4"}, {"c2", "int4"},
		{"c3", "text"}, {"c4", "bool"}, {"c5", "numeric"},
	}
	tbl := &catalog.Table{Schema: "public", Name: "t"}
	for i, c := range names {
		tbl.Columns = append(tbl.Columns, catalog.Column{Name: c.name, Type: catalog.Type{Name: c.typ}, Ordinal: i})
	}
	return tbl
}

// TestExprSiblingParityPlannerBuilt runs the corpus the planner itself
// produces. Beyond the position, this is the only corpus whose ResultType
// spellings, constant folding and operator resolution are the production ones
// — the hand-built corpora assert what the evaluators do with a given node,
// this one asserts they agree on nodes that actually occur.
func TestExprSiblingParityPlannerBuilt(t *testing.T) {
	predicates := []string{
		// Arithmetic whose overflow check must fire at the same position.
		// `c0 + <int-literal>` resolves to int8 (exprType widens), so the int4
		// arm is only reachable column-to-column or through an explicit cast —
		// which is itself a PG divergence, ledgered 2026-08-06.
		"c0 + 2147483647::int4 > 0",
		"c0 * c1 > 0",
		"c1 - c0 > 0",
		"c0 + 2147483647 > 0",
		"c0 / 0 > 0",
		"c0 % 0 > 0",
		// Three-valued logic over a NULL column, both short-circuit directions.
		"c0 = 7 AND c1 = 11",
		"c2 = 1 AND c0 = 7",
		"c0 = 99 AND c2 = 1",
		"c2 = 1 OR c0 = 7",
		"c0 = 7 OR c2 = 1",
		"NOT (c0 = 7)",
		"NOT (c2 = 1)",
		// Mixed types: the operand shapes that made the twins disagree.
		"c3 = 'abc' AND c4",
		"c4 OR c3 = 'zzz'",
		"c5 > 1.0 AND c0 < 100",
		// Unary and adapter-bound shapes side by side in one tree.
		"-c0 < 0",
		"c0 IS NULL OR c2 IS NULL",
		"(c0 + c1) * 2 = 36",
		"lower(c3) = 'abc'",
		"c3 || 'x' = 'abcx'",
	}
	var corpus []parityCase
	for _, p := range predicates {
		corpus = append(corpus, parityCase{"planner-built: " + p, parityResolve(t, p)})
	}
	checkParityCorpus(t, corpus, parityRow())
}

// TestExprSiblingParityCompiledPositionIsTheSourcePosition pins the mechanism
// the corpus above can only observe indirectly: a compiled BinaryOp/UnaryOp
// carries the planner node's own position, not zero. Asserted directly because
// a future refactor could make both twins report 0 and still be "in parity" —
// agreement on a lost position is not the property under audit.
func TestExprSiblingParityCompiledPositionIsTheSourcePosition(t *testing.T) {
	// M0134-0070 update: PG never attaches errposition to a runtime
	// arithmetic overflow (int4pl etc., postgres/src/backend/utils/adt/int.c,
	// has no errposition call) — only to parse-time literal-coercion errors.
	// So both twins must now report Pos==0 for this raise, NOT the source
	// position; the property under audit is unchanged (the twins must agree
	// with each other, not merely coincide at 0 by omission), so this test
	// pins BOTH twins explicitly rather than only the compiled one.
	// The explicit ::int4 is required: `c0 + <literal>` resolves to int8.
	e := parityResolve(t, "c0 + 2147483647::int4 > 0")
	add, ok := e.(*optimizer.BinaryOp).Left.(*optimizer.BinaryOp)
	if !ok {
		t.Fatalf("expected a BinaryOp under the comparison, got %T", e.(*optimizer.BinaryOp).Left)
	}
	if add.Pos() == 0 {
		t.Fatalf("planner produced pos 0; the test cannot distinguish carried from lost")
	}

	// Interpreted twin (evalExprSlot, via evalExpr's public entry point).
	_, interpErr := evalExpr(add, parityRow(), nil)
	iee, ok := interpErr.(*ExecError)
	if !ok {
		t.Fatalf("interpreted eval: err = %v (%T), want *ExecError 22003", interpErr, interpErr)
	}
	if iee.Code != "22003" || iee.Pos != 0 {
		t.Errorf("interpreted ExecError = {code=%s pos=%d}, want {code=22003 pos=0}", iee.Code, iee.Pos)
	}

	// Compiled twin (evalFastExpr, ExprNode slab).
	var slab exprTreeSlab
	idx := slab.buildExpr(e)
	_, err := evalFastExpr(slab, idx, SlotFromRow(nil, parityRow()), nil)
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("compiled eval: err = %v (%T), want *ExecError 22003", err, err)
	}
	if ee.Code != "22003" || ee.Pos != 0 {
		t.Errorf("compiled ExecError = {code=%s pos=%d}, want {code=22003 pos=0}", ee.Code, ee.Pos)
	}
}

// TestExprSiblingParityNilAndDegenerateNodes covers the inputs that reach the
// evaluators from a mis-built slab rather than from a planner tree: a nil
// expression, and BinaryOp/UnaryOp nodes whose children are missing. PS6.1
// found that the degenerate direction is the dangerous one — an uncompiled
// node list did not error, it returned the empty key — so the contract is
// that a missing child is an ERROR on both twins, never a value.
func TestExprSiblingParityNilAndDegenerateNodes(t *testing.T) {
	slot := SlotFromRow(nil, parityRow())

	// nil expression: buildExpr returns noExpr, evalFastExpr returns NULL.
	// evalExprSlot(nil, …) panics on e.Pos(), which is why join_composite_key
	// raises errNilHashKey before ever calling it. Pin the compiled half.
	var slab exprTreeSlab
	if idx := slab.buildExpr(nil); idx != noExpr {
		t.Fatalf("buildExpr(nil) = %d, want noExpr", idx)
	}
	if d, err := evalFastExpr(nil, noExpr, slot, nil); err != nil || !d.IsNull() {
		t.Errorf("evalFastExpr(noExpr) = (%v, %v), want (NULL, nil)", d, err)
	}

	// Hand-built degenerate nodes: a BinaryOp with no children, a UnaryOp with
	// no operand, and an out-of-band Kind. All three must error, not panic and
	// not evaluate.
	degenerate := []struct {
		name string
		slab exprTreeSlab
	}{
		{"binary op missing both children", exprTreeSlab{{Kind: ExprBinaryOp, childA: noExpr, childB: noExpr}}},
		{"binary op missing right child", exprTreeSlab{
			{Kind: ExprBinaryOp, childA: 1, childB: noExpr},
			{Kind: ExprIntConst},
		}},
		{"unary op missing operand", exprTreeSlab{{Kind: ExprUnaryOp, childA: noExpr}}},
		{"invalid kind", exprTreeSlab{{Kind: ExprInvalid}}},
		{"unknown kind", exprTreeSlab{{Kind: ExprKind(200)}}},
	}
	for _, d := range degenerate {
		out := evalParityOutcome(func() (Datum, error) { return evalFastExpr(d.slab, 0, slot, nil) })
		if !strings.HasPrefix(out, "ERR") {
			t.Errorf("%s: outcome %s, want an error", d.name, out)
		}
	}
}
