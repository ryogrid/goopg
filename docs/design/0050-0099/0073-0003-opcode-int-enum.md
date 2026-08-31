# Design 0073-0003 — UnaryOp / BinaryOp `Op` field: string → OpCode int8 enum

**Milestone:** M0073-0003
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** none (independent of arena work).

## Context

Q5's CPU profile post-M0072-final
(`pprof-data/m0072-final/q5.cpu.prof`) shows the
expression evaluator dominating wall-time:

| Function | flat % | cum % |
|----------|------:|-----:|
| `evalExprSlot` | 26.11 % | 68.27 % |
| `evalBinary`   |  8.78 % | 29.20 % |
| `evalAnd`/`evalOr` (called from evalBinary) | embedded | embedded |
| `compareDatum` |  5.26 % | 11.62 % |

The hot path inside `evalBinary` is a string switch on
`op` (e.g. `"+"`, `"-"`, `"AND"`, `"OR"`, `"="`, `"<"`,
…). The full enumeration found by the explore agent is
**16 distinct Op values** (3 unary + 13 binary):

- Unary: `-`, `+`, `NOT`
- Binary arithmetic: `+`, `-`, `*`, `/`, `%`
- Binary text: `||` (concat)
- Binary comparison: `=`, `<`, `>`, `<=`, `>=`, `<>`,
  `!=` (alias of `<>`)
- Binary boolean: `AND`, `OR`
- Binary pattern: `LIKE`, `NOT LIKE`

Go switches on string operands compile to a sequence of
length-checks + byte-comparisons; switches on small int
constants compile to either a jump table (when dense) or
a binary search. With 16 values, the int-enum form gets
a jump table — ~2-4 × faster per dispatch. Across Q5's
millions of evalBinary calls, the projected savings are
~5-10 pp Q5 CPU.

This refactor is independent of the arena work
(M0073-0001 / 0002 / 0004); it lands as a single atomic
commit (Commit B in the M0073 plan).

## Goals

- Define `OpCode int8` enum + 16 + sentinel constants in
  a new `internal/parser/op.go` so both parser and
  planner reference the same type.
- Provide `ParseUnaryOp(string) OpCode`,
  `ParseBinaryOp(string) OpCode` for token-text → OpCode
  mapping at parse time.
- Provide `(OpCode).String() string` for debug / test
  output.
- Provide `(OpCode).IsBoolean() bool` for the
  AND-or-OR short-circuit detection currently embedded
  in `evalBinary`'s entry switch.
- Flip `parser.UnaryOp.Op` / `BinaryOp.Op` and their
  `planner.*` mirrors from `string` to `OpCode`.
- Update all switch statements (`evalUnary`,
  `evalBinary`, `arithmetic`, `tryFoldBinaryOp`,
  `tryFoldUnaryOp`) to switch on OpCode constants.
- Update all construction sites (parser select.go,
  planner unnest / pushdown / bushy / nl_index_join /
  mhj_input_rewrite / likeprefix / planner.go /
  foldconst.go) to use `parser.Op<Name>` constants.
- Update test fixtures (~34 sites) that construct
  `&BinaryOp{Op: "<str>"}` literals.
- Update test debug helpers (`q20_unnest_test.go`,
  `q21_live_test.go`) to call `op.String()`.

## Non-goals

- **Wire-protocol output of OpCode.** Currently no
  public surface displays Op directly (EXPLAIN uses
  expression-tree printers that delegate to `op.String()`
  or similar; the existing string-output continues to
  work via the new String() method).
- **Adding new operators.** The 16 values are exactly
  what the parser produces today.
- **OpCode size compression** (bit-packing into other
  fields). int8 is fine; struct alignment dominates.

## Proposed enum

```go
// internal/parser/op.go (NEW)
package parser

// OpCode is the typed dispatch key for UnaryOp / BinaryOp.
// Replaces the historical `Op string` field — switching on
// OpCode produces a jump-table dispatch, eliminating the
// per-row string-compare cost the M0072-final pprof
// flagged on Q5.
//
// The zero value (OpUnknown) is reserved for the unset /
// parse-error state; all valid expressions carry a
// non-zero OpCode.
type OpCode int8

const (
    OpUnknown OpCode = iota

    // Unary operators
    OpUnaryNeg // -x
    OpUnaryPos // +x
    OpNot      // NOT x

    // Binary arithmetic
    OpAdd // a + b
    OpSub // a - b
    OpMul // a * b
    OpDiv // a / b
    OpMod // a % b

    // Binary text
    OpConcat // a || b

    // Binary comparison
    OpEq // a = b
    OpLt // a < b
    OpGt // a > b
    OpLe // a <= b
    OpGe // a >= b
    OpNe // a <> b OR a != b (single OpCode for both forms)

    // Binary boolean
    OpAnd // a AND b
    OpOr  // a OR b

    // Binary pattern
    OpLike    // a LIKE b
    OpNotLike // a NOT LIKE b
)
```

### Helpers

```go
// ParseUnaryOp converts the token text the parser
// emits ("-", "+", "NOT") to an OpCode. Unknown
// strings return OpUnknown — caller is expected to
// surface a parse-time error rather than constructing a
// UnaryOp with an unknown op.
func ParseUnaryOp(s string) OpCode {
    switch s {
    case "-": return OpUnaryNeg
    case "+": return OpUnaryPos
    case "NOT": return OpNot
    }
    return OpUnknown
}

// ParseBinaryOp converts the token text to an OpCode.
// Recognises the 13 binary operators; treats "<>" and
// "!=" as the same OpNe.
func ParseBinaryOp(s string) OpCode {
    switch s {
    case "+": return OpAdd
    case "-": return OpSub
    case "*": return OpMul
    case "/": return OpDiv
    case "%": return OpMod
    case "||": return OpConcat
    case "=": return OpEq
    case "<": return OpLt
    case ">": return OpGt
    case "<=": return OpLe
    case ">=": return OpGe
    case "<>", "!=": return OpNe
    case "AND": return OpAnd
    case "OR": return OpOr
    case "LIKE": return OpLike
    case "NOT LIKE": return OpNotLike
    }
    return OpUnknown
}

// String returns the canonical SQL surface form. "!="
// canonicalises to "<>" (the upstream-Postgres preferred
// spelling) on output.
func (o OpCode) String() string {
    switch o {
    case OpUnaryNeg: return "-"
    case OpUnaryPos: return "+"
    case OpNot:      return "NOT"
    case OpAdd:      return "+"
    case OpSub:      return "-"
    case OpMul:      return "*"
    case OpDiv:      return "/"
    case OpMod:      return "%"
    case OpConcat:   return "||"
    case OpEq:       return "="
    case OpLt:       return "<"
    case OpGt:       return ">"
    case OpLe:       return "<="
    case OpGe:       return ">="
    case OpNe:       return "<>"
    case OpAnd:      return "AND"
    case OpOr:       return "OR"
    case OpLike:     return "LIKE"
    case OpNotLike:  return "NOT LIKE"
    }
    return "<unknown>"
}

// IsBoolean reports whether o is AND or OR.
// Replaces the explicit `if op == "AND" || op == "OR"`
// check at the head of evalBinary.
func (o OpCode) IsBoolean() bool {
    return o == OpAnd || o == OpOr
}
```

### Field flips

```go
// internal/parser/expr.go:220-225
type BinaryOp struct {
    pos   int
    Op    OpCode  // was: string
    Left  Expr
    Right Expr
}

// internal/parser/expr.go:231-235
type UnaryOp struct {
    pos     int
    Op      OpCode  // was: string
    Operand Expr
}

// internal/planner/plan.go:295-310 (mirror types)
type BinaryOp struct {
    pos   int
    Op    parser.OpCode  // was: string
    Left  Expr
    Right Expr
}

type UnaryOp struct {
    pos     int
    Op      parser.OpCode  // was: string
    Operand Expr
}
```

### Switch sites

```go
// internal/executor/expr.go::evalUnary (l.139-156)
func evalUnary(op parser.OpCode, d Datum, pos int) (Datum, error) {
    if d.IsNull() { return NullDatum, nil }
    switch op {
    case parser.OpUnaryNeg:
        // ... existing -x logic
    case parser.OpUnaryPos:
        // ... existing +x logic
    case parser.OpNot:
        // ... existing NOT logic
    }
    return Datum{}, &ExecError{Code: "42883", Pos: pos,
        Message: fmt.Sprintf("unknown unary operator %s", op)}
}

// internal/executor/expr.go::evalBinary (l.163-260)
func evalBinary(op parser.OpCode, left, right Datum, pos int) (Datum, error) {
    if op.IsBoolean() {  // was: if op == "AND" || op == "OR"
        switch op {
        case parser.OpAnd: return evalAnd(left, right), nil
        case parser.OpOr:  return evalOr(left, right), nil
        }
    }
    if left.IsNull() || right.IsNull() { return NullDatum, nil }
    switch op {
    case parser.OpAdd, parser.OpSub:
        // ... existing time/interval + numeric + int paths
    case parser.OpMul, parser.OpDiv, parser.OpMod:
        // ... existing numeric + int paths
    case parser.OpConcat:
        // ... existing string concat
    case parser.OpEq, parser.OpLt, parser.OpGt,
         parser.OpLe, parser.OpGe, parser.OpNe:
        // ... existing comparison via cmpResult(op, cmp)
    case parser.OpLike, parser.OpNotLike:
        // ... existing LIKE
    }
    return Datum{}, &ExecError{Code: "42883", Pos: pos,
        Message: fmt.Sprintf("unknown operator %s", op)}
}
```

The `cmpResult(op, cmp)` helper (l.211 today, takes `op
string`) becomes `cmpResult(op parser.OpCode, cmp int)`
with a switch on the 6 comparison ops.

`arithmetic(op, a, b, pos)` similarly takes `parser.OpCode`.

### Op comparison sites

```go
// internal/planner/nl_index_join.go:253
if ab.Op != parser.OpEq || bb.Op != parser.OpEq { ... }

// internal/planner/joinorder.go:219
if b.Op != parser.OpEq { ... }

// internal/planner/likeprefix.go:103
if b.Op != parser.OpLike { ... }
```

## Migration plan

Atomic single commit (Commit B in the M0073 plan). Order
of operations within the commit:

1. Create `internal/parser/op.go` with `OpCode` enum +
   helpers.
2. Add `internal/parser/opcode_test.go` pinning Parse*
   round-trip + String() reverse + IsBoolean() coverage.
3. Flip `parser.UnaryOp.Op` / `BinaryOp.Op` field types.
4. Update parser construction sites (`select.go`).
5. Flip `planner.UnaryOp.Op` / `BinaryOp.Op` mirror
   field types.
6. Update planner construction sites and 3 op-comparison
   sites.
7. Update executor switches (`evalUnary`, `evalBinary`,
   `arithmetic`, `cmpResult`).
8. Update foldconst (`tryFoldUnaryOp`,
   `tryFoldBinaryOp`).
9. Update test fixtures (~34 sites; the explore agent's
   report enumerates them).
10. Update test debug helpers
    (`q20_unnest_test.go::exprDebugIdx`,
    `q21_live_test.go::exprDebug`).
11. `go build ./... && go test ./...` PASS.
12. Pre-commit gate (Q12/Q13/Q21 + Q9 1100 s + 21-q
    sweep + Q5 pprof).

The Go type system fails closed: any missed
`Op: "<str>"` literal becomes a compile error
("cannot use string as OpCode").

## Verification

**Pre-commit gate:**
- Build server, fresh-restart.
- `./tpch-runner --queries=12,13,21
  --per-query-timeout=400s --cancel-after=380s` —
  Q12=2, Q13=35, Q21≥100.
- `./tpch-runner --queries=9 --per-query-timeout=1200s
  --cancel-after=1100s` — Q9=175 rows, wall ≤ 1100 s.
- `go test ./internal/parser/... ./internal/planner/...
  ./internal/executor/...` PASS.
- 21-query sweep row counts match Phase-4 baseline.

**Q5 CPU pprof rerun:**

```sh
mkdir -p pprof-data/m0073-0003
( go tool pprof -seconds=120 \
    -output=pprof-data/m0073-0003/q5.cpu.prof \
    http://127.0.0.1:6060/debug/pprof/profile ) &
sleep 1
./tpch-runner --queries=5 --per-query-timeout=620s
wait

go tool pprof -top -cum pprof-data/m0073-0003/q5.cpu.prof \
    | grep -E "evalBinary|evalUnary|evalExprSlot|cmpResult|arithmetic"
```

Acceptance:
- `evalBinary` cum CPU ≤ 15 % (was 29.20 %).
- `evalExprSlot` cum CPU may also drop slightly
  (the OpCode field-load is faster than string-header
  load).
- `cmpResult` cum CPU drops proportionally.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Mechanical-change typos cause wrong dispatch | Type system catches missing cases at compile; 21-q sweep checks dispatch correctness on every operator; `go test ./...` PASS gate. |
| R2 | Test fixtures using `Op: "<str>"` literals | Updated atomically with the planner mirror types. The explore agent's report enumerates the 34 sites; sweep them in one pass. |
| R3 | EXPLAIN ANALYZE / debug output relies on string Op | `OpCode.String()` returns the canonical form; debug helpers updated to call `op.String()` instead of reading `op.Op` directly. |
| R4 | OpUnknown leaks into a switch (parser bug or unmapped token) | Default case in evalUnary / evalBinary returns `42883 unknown operator` with `op.String()` displaying `<unknown>` — same external behaviour as today's "unknown operator <str>" path. |
| R5 | Future operators added (e.g. SIMILAR TO, ~) need new OpCode constants | Adding a constant + ParseBinaryOp case + String() case + executor switch case is mechanical; no architectural blocker. |
| R6 | Wire-protocol output changes shape (e.g. "!=" → "<>") | Acceptable — the `<>` form is the canonical Postgres spelling. Wire-protocol tests already accept either form on input; output canonicalisation matches upstream. |

## References

- `internal/parser/expr.go:220-235` — UnaryOp / BinaryOp
  field declarations (parser side).
- `internal/parser/select.go:775-804` — `peekBinaryOp()`
  token map; updated to produce OpCode directly.
- `internal/planner/plan.go:295-310` — UnaryOp / BinaryOp
  mirror types (planner side).
- `internal/executor/expr.go:139-260` — evalUnary +
  evalBinary switch bodies.
- `internal/executor/expr.go:346-367` — `arithmetic`
  helper.
- `internal/planner/foldconst.go:141-194, 218-245` —
  tryFoldBinaryOp / tryFoldUnaryOp.
- `pprof-data/m0072-final/q5.cpu.prof` — empirical
  motivation.
