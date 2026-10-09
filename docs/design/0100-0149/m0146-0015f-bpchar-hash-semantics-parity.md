# M0146-0015f: bpchar blank-insensitive equality and hash parity

Status: **LANDED**. Task: `.ralph/fix_plan.md` "bpchar HASH semantics
parity — verify/fix blank-insensitive hashing wherever bpchar keys are
grouped, joined, or de-duplicated" (filed 2026-09-27 after the
padding-correct TPC-DS SF0.25 reload exposed the class). Companion
finding recorded in OWNER DECISIONS 2026-09-27. Evidence:
`analysis/m0146/m0146-0015f/`.

## Defect

With correctly padded data in place, the SF0.25 sweep fell from 96/96
to `PASS=70 MISMATCH=20 CKMISMATCH=6` — the unpadded legacy load had
masked the defect both ways. Two halves of one root cause:

- `bpchareq` ignores trailing blanks (upstream `bpchar_eq` compares on
  `bcTruelen` images, varchar.c), but goopg's scalar `=` evaluated
  `KindString` operands byte-for-byte: `'Javier  '::char(20) =
  'Javier'` returned `f` where PG returns `t`, and `count(*) from
  customer where c_first_name='Javier'` returned 0 vs 31.
- `hashbpchar` hashes the blank-stripped image, so `char(20) 'x   '`
  and `char(5) 'x'` land in the same bucket wherever bpchar keys are
  hashed or de-duplicated. goopg rendered bytewise keys, so pad-aware
  `=` alone was NOT sufficient: hash joins, `GROUP BY`, `DISTINCT`,
  SetOp dedup, hashed `IN`, window partitions, and correlated-subquery
  hash maps would still silently lose matches.

## PostgreSQL semantics (verified on :65438, PG 18.3)

- `bpchar = bpchar` compares on `bcTruelen` images — width and padding
  of either side are irrelevant.
- An **untyped** literal opposite a bpchar resolves to bpchar and is
  compared under `bpchareq` — `'x  '` (unknown) equals `'x'::bpchar`.
- An explicitly **text/varchar** operand keeps its trailing blanks:
  `text = bpchar` resolves to `texteq` via the `bpchar→text`
  (`rtrim1`) cast on the bpchar side only — `'x  '::text = 'x'::bpchar`
  is `f`. `char(20) UNION ALL text` resolves to `character` in the
  tested shape, so SetOp members retain bpchar grouping.
- `IS [NOT] DISTINCT FROM` shares the equality semantics.
- `hashbpchar`/`hash_bpchar` hash the blank-stripped image, matching
  `bpchareq` — hash joins, grouping, and dedup see the same
  equivalence classes as scalar `=`.
- ANALYZE's frequency/MCV bucketing groups by the same equality, while
  stored sample values keep their padded width.

## Change

One rule, applied at every comparison and key boundary: **declared
operand types decide blank-insensitivity, never datum contents**. A
string that merely happens to end in spaces is never trimmed; a
declared bpchar expression is compared/keyed on its `bcTruelen` image
while stored and output values keep their padding.

`declaredBpcharTypmod` (`internal/executor/expr.go`) reports a positive
typmod for `ColumnRef`, `OuterColumnRef`, `ExecParamRef`, `CastExpr`,
`TypedStringLit`, and selected type-preserving calls declared as
`char`/`bpchar`/`character` (bare `char` normalises to length 1).
`comparisonOperandsAsBpchar(op, lv, rv, lTyp, rTyp, lLit, rLit)`
applies the PG rule: a declared-bpchar side trims; a bare string
literal opposite a bpchar is coerced to bpchar (trims); a declared
text/varchar side never trims. `trimStringDatum` removes trailing
ASCII spaces from `KindString` datums — the `bcTruelen` image.

Call-site inventory (build-side and probe-side always use identical
normalisation):

- `evalBinary`/`evalBinaryOp` — scalar `=`, `<>`, `<`, `<=`, `>`, `>=`
  on both the interpreted twin and the compiled `exprnode.go` twin
  (the compiled node serialises each operand's declared typmod into
  `payload[8:16]` and bare-literal flags into `payload[16]` so the
  twins agree).
- `IS [NOT] DISTINCT FROM`.
- `IN`/`NOT IN`/`= ANY`/`!= ANY`/simple `CASE`: per-item metadata from
  `x.List` expressions, the subplan's `Output()` schema type for
  subquery `IN`, or the single-source expression for array expansion;
  `evalInHashProbe` normalises the probe key the same way.
- Row-valued `(a,b) IN (subquery)` and `(a,b) op (c,d)`: per-column
  trim flags on `subPlanRowHash` and the linear row-IN path.
- Correlated scalar-subquery hash maps: inner scan keys trim by
  `innerBP`; the outer correlated value trims by `outerBP` — the
  lowering from `OuterColumnRef` to `ExecParamRef` preserves the
  declared type (TPC-DS Q41 was the witness).
- Hash join: `buildKeyTrim`/`probeKeyTrim` computed from
  `buildKeyExprs`/`probeKeyExprs` at `compileExecExprs`, applied in
  `encodeCompositeKey` and the single-key slot evals; `joinBatchHash`
  hashes the already-trimmed datum, so multi-batch spill routing stays
  consistent with the map keys.
- Merge join: sort keys via `evalSortKeyValue` (shared by `sortOp`,
  Gather Merge, incremental sort, and SetOp merge keys) and
  `joinMergeStream` comparison keys trim on declared bpchar-ness.
- `GROUP BY`/hash aggregate: `gkTrims` on `aggregateOp` applied in
  `evalGroupExprs`, `evalGroupKeysScratch`, partial-state decode, and
  `COUNT(DISTINCT arg)`. Stored aggregate values stay padded.
- `DISTINCT`/`DISTINCT ON`: schema-derived `colTrims` on the dedup
  keys (and ordering keys, so the two agree).
- `UNION`/`INTERSECT`/`EXCEPT` and recursive-CTE dedup:
  `rowKeyTrimmed` — per-column schema trim flags inside the existing
  canonical row key, numeric canonicalisation preserved.
- Window functions: partition and order trim flags applied to
  precomputed keys, `partitionKey`, `samePeer`, and the internal sort.
- `ANALYZE`: `computeColumnStats` buckets frequencies by the trimmed
  key; stored MCV values and width accounting keep the padded datum.

Deliberately NOT keyed (conservative over-keying is safe — extra cache
entries, never missed matches): memoize probe keys and the correlated
subquery result cache use raw `datumKey` for cache granularity only.

## Residual (ledgered)

`left op ANY(array-value)` where the elements arrive as an evaluated
array **datum** — a `bpchar[]` column or a multi-element `ARRAY[...]`
literal — exposes no declared element type, so the element side stays
byte-exact while a declared-bpchar operand trims. Scalar `IN` lists
and subquery `IN` do carry item types and are covered. A
bpchar-typed array operand against a bpchar column hits this only when
element padding differs from the operand's trimmed image.

## Tests

`internal/executor/bpchar_hash_parity_test.go` plus the widened
`TestHashJoinBpcharTrailingSpaceAgreement` and
`bpchar_padded_storage_test.go` cover: mixed-width `char(n)` hash
joins (both directions), scalar and `IN`/`CASE` comparisons under each
coercion arm, hash and sorted `GROUP BY`, `DISTINCT`/`DISTINCT ON`,
`UNION`/`INTERSECT`/`EXCEPT`, recursive-CTE dedup, hashed and linear
subquery `IN` (single- and multi-column), correlated scalar-subquery
hash maps, window partition/peer keys, and the `ExecParamRef`
lowering. Interpreted/compiled twin parity is pinned by the existing
exprnode tests plus the `payload[16]` literal flags.

## Gates

- `RALPH_PRECOMMIT_SCOPE=units` — PASS.
- `scripts/tpch-spotcheck.sh` — Q12=2, Q13=33, PASS.
- TPC-H acceptance arm vs `bench/tpch/baseline-digests.txt` — 24
  MATCH, PASS.
- `scripts/tpcds-sf025-regression.sh sweep` — 96 PASS / 0 MISMATCH /
  0 CKMISMATCH (was `PASS=70 MISMATCH=20 CKMISMATCH=6` on the padded
  reload; the remaining Q41 correlated-subquery case was the
  `ExecParamRef` arm).
- Live probes vs PG 18.3 `:65438` — `analysis/m0146/m0146-0015f/`:
  scalar `=`, text asymmetry, `IN`, correlated scalars, semi joins,
  hash joins, multi-column `IN`, ordering, mixed-width grouping,
  set ops, window peers, `IS [NOT] DISTINCT FROM` — all identical.
