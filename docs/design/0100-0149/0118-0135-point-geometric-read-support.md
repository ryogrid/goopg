# 0118-0135 — Point coordinate subscripting + strictly-left/right operators (M0118-0002, predicate-gist enabler)

**Status:** accepted
**Scope:** enabler, NOT a spec promotion
**Spec advanced:** `postgres/src/test/isolation/specs/predicate-gist.spec`

## Problem

`predicate-gist` is one of the three remaining `failed` isolation specs in
M0118 (the others: `predicate-gin`, `deadlock-parallel`). Probing it (throwaway
`RunAndCompare` + `.Diff`) showed goopg already supports everything the spec's
setup needs — the `point` type, the `point(x,y)` constructor, `CREATE INDEX …
USING gist(p)`, and the SERIALIZABLE machinery — but the very first read step
errored, so no permutation produced output.

Two distinct gaps blocked the read steps:

1. **`p[0]` subscripting a point returned garbage.** goopg backs `point` with
   its text form `"(x,y)"`. The `array_subscript` evaluator's fallback branch
   (intended for fixed-length pseudo-arrays like `name`) char-subscripted that
   text 0-based, so `point(10,10)[0]` returned `"("` and `[1]` returned `"1"`.
   The spec's `sum(p[0])` then failed analysis with `42804 sum() argument must
   be numeric` because the subscript typed as `text`.

2. **`<<` / `>>` on points errored.** PostgreSQL overloads `<<` (strictly left
   of) and `>>` (strictly right of) for geometric types; goopg parsed both as
   the integer bit-shift operators and the executor raised `42883 operator <<
   requires integer operands` for point operands.

After both are fixed the **only** remaining `predicate-gist` divergence is
goopg's float8 *text-output* formatting (`2.23375e+06` vs PG's `2233750`) —
see "Remaining blocker" below. Crucially, a filtered probe confirmed there are
**no SSI divergences at all**: goopg raises 40001 in exactly the permutations
PG does and stays clean in the disjoint-region permutations. The serialization
behaviour already matches byte-for-byte.

## Change

PostgreSQL geometric semantics: a `point` is subscriptable **0-based**, where
`point[0]` is the X coordinate and `point[1]` the Y coordinate, both `float8`;
`p << q` ⇔ `p.x < q.x` and `p >> q` ⇔ `p.x > q.x`, both `bool`.

- **`internal/executor/expr.go` — `array_subscript`**: before the existing
  `name`/char-subscript fallback, when the base value looks like a point literal
  (`sv[0]=='('` and `parsePointText` succeeds), return the 0/1 coordinate as a
  `numeric` Datum (formatted via `parseNumeric`/`newNumeric`, mirroring `sqrt`);
  any other index yields NULL. `parsePointText` requires exactly two parseable
  floats, so `name`/typname (`_int4`), box (`(x1,y1),(x2,y2)`), inet, etc. all
  fall through unchanged.
- **`internal/executor/expr.go` — `OpBitShiftLeft`/`OpBitShiftRight`**: at the
  top of the bitwise-operator case, when the op is a shift and both operands are
  point-shaped text, return the X-coordinate comparison as a bool. NULL operands
  are already short-circuited to NULL upstream (`evalBinary` L1172). Integer
  shifts (`KindInt` operands) and non-point strings are untouched, so `1 << 4 =
  16` and `'abc' << 'def'` → integer-operands error are preserved.
- **`internal/analyzer/analyzer.go` — `ArraySubscriptExpr`**: a `point` base
  types the element as `float8`, so `sum(p[0])`/`avg(p[0])` pass the
  numeric-argument check.
- **`internal/planner/planner.go` — `array_subscript` type inference**: same
  `point` → `float8` element type for downstream typing.

## Blast radius

Bounded. The point branches fire only when an operand's text parses as a
two-float point literal — a shape no other goopg-backed type produces in these
positions. Integer bit-shifts, name/typname char-subscripting (the DU-002
`typname[0]='_'` pg_dump path), array-element subscripting, and all other
binary-operator paths are byte-identical. No central format or storage change.

## Remaining blocker (deferred to its own loop)

`predicate-gist`'s sole remaining divergence is **goopg's float8 text output**:
`codec.go` encodes float8 with `strconv.FormatFloat(f,'g',-1,64)`, which emits
scientific notation (`2.23375e+06`) for normal-magnitude values where PG's
`float8out` prints plain decimal (`2233750`). This is a cluster-wide display
path (every float8 value), so a faithful `float8out` (shortest round-trip digits
with PG's fixed-vs-scientific exponent threshold) must be done as a dedicated
loop with ground-truth comparison against `./postgres/local_install` across
magnitudes **and** a full regress-port re-run — exactly the codec/format change
class the project rules gate behind the full suite. Once it lands,
`predicate-gist` is expected to promote (no SSI work remains). Ledger row
recorded. `predicate-gin` independently needs real `int4[]`-column array typing
+ a GIN AM; `deadlock-parallel` needs parallel-worker lock groups.

## Gates

- New unit `TestPointStrictlyLeftRightOperators` (executor): `<<`/`>>` point
  comparison incl. equal-X, NULL→NULL, integer-shift unaffected, non-point
  string still errors.
- New end-to-end `TestPort_PointGeometricRead` (testport): `p[0]`/`p[1]`
  coordinates, `sum(p[0])`, and `p << / >> point(...)` row filtering through the
  full analyzer→planner→executor path.
- `go build ./...` clean; `internal/analyzer` + `internal/executor` +
  `internal/planner` suites PASS no regression.
- pgbench smoke = pre-commit hook.
