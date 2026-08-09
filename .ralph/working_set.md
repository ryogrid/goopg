(idle — nothing in flight)

Last loop: M0119-0006 tenth slice — FLOAT expression key encoding, made
type-directed instead of kind-directed.

- Defect found: `encodeArbiterExprKey` dispatched purely on the runtime Datum
  kind, and goopg has NO `KindFloat`. `codec.go` renders a stored float via
  `PGFloatOut` and re-parses it (`floatTextDatum`), so ONE float column yields
  `KindNumeric` for `1.5` and `KindString` for `1e+30`/`Infinity`/`NaN` — the
  same expression index was written by `EncodeNumericKey` AND `EncodeVarchar`,
  byte spaces that do not interleave. Dumped from a real build of `((f * 2))`:
  `02800000003300` / `2d342e3500` / `32652b333000` (7/5/6 bytes). Not ordered
  at all; a range scan could miss arbitrarily many live rows.
- Fix: `encodeArbiterExprKey(v, keyExpr, pos)` — new `exprKeyIsFloat` uses
  `planner.ExprResultType`; a float-typed expression sends EVERY row through
  `btree.EncodeFloat8` (PG `float8_cmp_internal` order, NaN last). `keyExpr ==
  nil` keeps the old kind dispatch verbatim. `datumToFloat64ForKey` extracted
  from `encodeBTreeKeyForColumn`'s float arm and shared (Rule #2). All three
  call sites (upsert arbiter / ddl bulk build / storage maintain) already held
  the resolved expression. `exprKeyDecodeType` gains float8 surrogate,
  allowRoutine=true — also un-declines amcheck's opclass comparator for any
  index with a float expression key.
- NEW tests `internal/executor/expression_index_float_key_test.go`:
  `TestEncodeArbiterExprKeyFloatIsTypeDirected` (10 values, both kinds, both
  infinities + NaN; built-in non-vacuity assertion) and
  `TestExpressionIndexBuildFloatKey` (physical tree scan, bulk build +
  post-build INSERT). Float rows added to `TestExprKeyDecodeTypeRoundTrip` /
  `…RoutineSafety`; float removed from `…DeclinesUninvertible`.

Design doc `docs/design/0119-0006-expression-index-float-key.md` (+ README row
`0119-0006e`), 1 ledger row, fix_plan 10th-slice note.

Gates run: units precommit PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2,
Q13=35); pgbench hook PASS. Non-vacuity confirmed by forcing `exprKeyIsFloat`
false (all three float gates fail with the mixed-encoding key dump).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
Remaining M0119-0006:
  1. enum expression key encoding — blocked on `planner.exactTypeOID` refusing
     enum names; needs catalog access in the type resolver.
  2. posting-list duplicate coverage in the `checkunique` tier.
  3. `box`/`int4range`/`int4[]`/interval key encodings.
  4. unscoped whole-database `pg_amcheck` run in the 005 port.
Gate note carried forward: the TPC-DS SF0.5 sweep (~1 h) has NOT been run for
the last ten M0119-0006 slices; worth one run before M0119-0006 closes.

In-flight: none.
