# 0118-0136 — PG-faithful `float4out`/`float8out` text output (`PGFloatOut`)

**Status:** accepted
**Milestone:** M0118-0002 (predicate-gist enabler) — also a cluster-wide
correctness fix for float text output
**Type:** Enabler, NOT a promotion

## Problem

goopg rendered `float4`/`float8` values with Go's
`strconv.FormatFloat(f, 'g', -1, bitSize)` and a one-off post-hoc fixup that
forced fixed-point only for exponents in `[1, 14]`. Go's `'g'` verb switches to
scientific notation at a magnitude- and type-independent threshold that does
**not** match PostgreSQL's `float8out`/`float4out`, so values such as
`2233750::float8` printed as `2.23375e+06` where PG prints the plain integer
`2233750`. Three independent output sites carried three slightly different
copies of the same flawed heuristic:

- `internal/executor/codec.go` `encodeValuePG` (storage/varlena text encode for
  float4 + float8) — `FormatFloat(f, 'g', -1, …)` with no fixup at all.
- `internal/server/dispatch.go` `appendFloatText` / `appendFloat8Text` (the
  simple-query wire-output path) — the `exp ∈ [1,14] → 'f'` fixup.
- `internal/server/dispatch_extended.go` (the extended/parameterised wire-output
  path) — `Datum.AppendValueText`, which re-emitted a Go-formatted string
  verbatim (e.g. an aggregate `sum`'s cached `"2.18875e+06"`).

A fourth site, the isolation **test harness**
(`internal/testport/framework/isolation_runner.go`), scanned float columns as
`sql.NullString`, letting `database/sql`'s `convertAssign` re-render the float
with Go's `'g'` verb — so even when the server emitted correct text, the golden
comparison saw Go-formatted text.

This was the **sole** remaining divergence in `predicate-gist.spec` after the
read-step enabler (0118-0135): the spec's `select sum(p[0]) …` rows printed in
scientific notation against PG's plain-integer golden output.

## PostgreSQL behaviour

Under the default `extra_float_digits = 1` (PG 12+), `float8out` uses the
shortest round-trip decimal (Ryu, `pg_strtod`/`double_to_shortest_decimal_buf`
in `src/common/d2s.c`; float4 via `f2s.c`). The Ryu `to_chars` routine then
chooses fixed-point vs scientific by the **display exponent** `e` (the power of
ten of the most-significant digit): fixed-point output when `-4 ≤ e < sciExp`,
scientific otherwise. `sciExp` is **15** for `float8` and **6** for `float4`
(the type's `FLT_DIG`/`DBL_DIG`-derived precision). Scientific form is
`d[.ddd]e±NN` with a single leading digit, no trailing zeros, and the exponent
zero-padded to at least two digits. Special values are the canonical
`Infinity` / `-Infinity` / `NaN`; negative zero renders `-0`.

## Implementation

New exported `executor.PGFloatOut(f float64, bitSize int) string`
(`internal/executor/codec.go`) reproduces that logic:

1. Special-case `NaN`/`±Inf` to PG's canonical names.
2. `sciExp = 15` for `bitSize == 64`, `6` for `bitSize == 32`.
3. Obtain the shortest round-trip digits + display exponent from
   `strconv.FormatFloat(abs(f), 'e', -1, bitSize)` (Go's `'e'` gives exactly the
   Ryu shortest mantissa with an explicit exponent — we re-place the decimal
   point ourselves rather than trust `'g'`'s threshold).
4. If `-4 ≤ exp < sciExp`, emit fixed-point (three sub-cases: leading `0.000…`,
   trailing `…000`, or embedded `dddd.dddd`); otherwise emit `d[.ddd]e±NN` with a
   `≥2`-digit zero-padded exponent.
5. `math.Signbit` preserves the leading `-` (so `-0` renders correctly).

All four sites now call `PGFloatOut`:

- `codec.go` `encodeValuePG`: `float4` → `PGFloatOut(f, 32)`, `float8` →
  `PGFloatOut(f, 64)`.
- `dispatch.go`: `appendFloatText`/`appendFloat8Text` delegate to `PGFloatOut`
  (including the special-value names, deleting their local copies).
- `dispatch_extended.go`: float4/float8 result columns route through
  `appendFloatText`/`appendFloat8Text` (re-formatting the possibly Go-formatted
  Datum) instead of `Datum.AppendValueText`; all other types keep the
  M0092-0004 zero-double-alloc fast path.
- `isolation_runner.go` `scanResultSet`: float4/float8 columns are scanned as
  `sql.NullFloat64` (lib/pq decodes OIDs 700/701 to `float64`) and rendered via
  `PGFloatOut`, mirroring the existing `dateCols` special-case — so the golden
  comparison sees PG-format text regardless of `database/sql`'s reconversion.

These are the **sibling paths** that must agree (encode ↔ wire-simple ↔
wire-extended ↔ test-harness); updating one without the others would leave a
silent divergence (project rule #2 / `pattern_sibling_paths_must_agree`).

## Why this is an enabler, not a `predicate-gist` promotion

With float output fixed, `predicate-gist.spec`'s first divergence advances from
the read step (scientific notation) to a **genuine SSI over-detection**:
permutation `rxy3 wx3 rxy4 c1 wy4 c2` has goopg raise
`40001 could not serialize access due to read/write dependencies among
transactions` on `c2`'s commit where PG **commits cleanly**. `rxy3` reads points
with `p >> point(6000,6000)` (X > 6000) and `rxy4` reads `p << point(1000,1000)`
(X < 1000); `wx3` inserts in the high-X region and `wy4` in the low-X region.
PG's GiST **page-level** predicate locks see that each reader's predicate lock
covers a spatial region disjoint from the other writer's insert, so no dangerous
structure forms. goopg's coarse **relation/tuple-grain** SIREAD locks the whole
relation, so any concurrent insert conflicts → a false write-skew cycle →
spurious `40001`.

This is the canonical predicate-lock **granularity** gap — the same class that
`predicate-hash` resolved with bucket-grain SIREAD
(`goopg_hash_index_ssi_bucket_locking`, design 0118-0099). For GiST on `point`
it needs spatial page-grain (or bounding-box/grid-cell) predicate locking, a
distinct Effort-L subsystem. `predicate-gist` stays `defer`; the remaining
blocker is now cleanly isolated to GiST spatial SIREAD granularity (recorded in
the deferral ledger).

The float fix is independently valuable: every `float4`/`float8` value goopg
emits — on the wire, in storage text, and in test comparison — is now
byte-faithful to PG 18.3, removing a latent source of silent row-text
divergence in any current or future float-bearing test.

## Tests / gates

- New `TestPGFloatOut` (`internal/executor/pgfloatout_test.go`): 28 `float8` +
  17 `float4` cases captured from a real PG 18.3 instance
  (`extra_float_digits = 1`), spanning the fixed↔scientific thresholds
  (`1e14`→`100000000000000`, `1e15`→`1e+15`; `123456`→`123456`,
  `1234567::float4`→`1.234567e+06`), sub-normals (`5e-324`), the max double
  (`1.7976931348623157e+308`), negative zero, and `±Inf`/`NaN`.
- Full regress-port suite (`TestPort_RegressSuite`) re-run — no new failure
  (codec/format change gate, project rule #5).
- TPC-H Q12/Q13 spot-check (`scripts/tpch-spotcheck.sh`) — canonical
  `Q12=2 / Q13=35` (display-path change gate; many TPC-H aggregates are float8).
- `predicate-gist` probe: first divergence advanced from the read step to the
  SSI over-detection (`rxy3 wx3 rxy4 c1 wy4 c2`), confirming the float fix is the
  only output-format blocker.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Files

- `internal/executor/codec.go` — `PGFloatOut`; `encodeValuePG` float4/float8.
- `internal/executor/pgfloatout_test.go` — new unit test (PG-captured goldens).
- `internal/server/dispatch.go` — `appendFloatText`/`appendFloat8Text` delegate.
- `internal/server/dispatch_extended.go` — float result columns re-format.
- `internal/testport/framework/isolation_runner.go` — float column scan/render.
