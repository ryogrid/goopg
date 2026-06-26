# 0118-0122 — `index-only-bitmapscan` PROMOTED: EXPLAIN over DECLARE CURSOR (M0118-0002)

**Status:** accepted
**Spec:** `postgres/src/test/isolation/specs/index-only-bitmapscan.spec`
**Test:** `TestPort_IsolationIndexOnlyBitmapscan` (`internal/testport/isolation_port_test.go`, strict)
**Result:** all permutations byte-for-byte vs PostgreSQL 18.3; spec promoted `defer` → `pass`.

## What the spec checks

`index-only-bitmapscan` is a **regression guard** kept upstream after an unsound
index-only bitmap heap scan optimization was removed (see
`https://postgr.es/m/873c33c5-ef9e-41f6-80b2-2f5e11869f1c%40garret.ru`). It does
**not** require goopg to implement that optimization — it verifies the *anomaly*
the optimization caused is absent.

The schema is a wide-tuple, low-fillfactor table `ios_bitmap(a int, b int, pad
char(1024))` with two single-column indexes. The permutation:

1. `s2_vacuum` — `VACUUM (TRUNCATE false)` to materialize the visibility map.
2. `s2_mod` — `DELETE FROM ios_bitmap WHERE a > 1` (deletes nearly all rows).
3. `s1_explain` — `EXPLAIN (COSTS OFF) DECLARE foo NO SCROLL CURSOR FOR SELECT
   row_number() OVER () FROM ios_bitmap WHERE a > 0 OR b > 0` — confirms the
   chosen plan (a `BitmapOr` of the two index scans under a bitmap heap scan).
4. `s1_begin` / `s1_prepare` — open the cursor.
5. `s1_fetch_1` — fetch one row (`row_number = 1`), forcing the index-scan
   portion to complete *before* the next vacuum.
6. `s2_vacuum` — re-vacuum; with the historical bug this marked now-empty pages
   all-visible.
7. `s1_fetch_all` — the anomaly check: **must return 0 rows**. With the bug it
   returned rows from pages whose tuples had all been removed.

The substantive assertion is the FETCH row counts (`1 row`, then `0 rows`).

## Why it was deferred — and the single remaining blocker

Prior loops (working-set notes, design `0118-0038`/`0118-0108`) cleared earlier
blockers: the `INSERT … SELECT` arity PANIC (0118-0038) and the `VACUUM
(TRUNCATE false)` option parse (0118-0108). The working set still listed the
spec as needing "real Bitmap Heap Scan + BitmapOr + EXPLAIN DECLARE CURSOR plan
rendering" — an over-estimate.

The actual remaining blocker was a single one: step `s1_explain` raised
`0A000 unsupported statement type *parser.DeclareCursorStmt`. The parser already
accepts `EXPLAIN … DECLARE … CURSOR FOR …` (`parseExplain` calls
`parseStatement` for the inner, which routes to `parseDeclareCursor`), but
`planner.Plan`'s `ExplainStmt` case planned `s.Inner` directly — and a
`DeclareCursorStmt` hits the planner's default `feature_not_supported` arm. That
error line appeared in the actual output where the expected output has the
(stripped) plan block, so the permutation diverged at `s1_explain` and the spec
stayed red.

### The BitmapOr plan body is not compared

`framework.normalizeIsoOutput` deliberately **strips the EXPLAIN plan block**
(`QUERY PLAN` header through the `(N rows)` footer) on *both* the expected and
actual sides, with the documented rationale that "goopg and PostgreSQL choose
different plan strategies, so plan text never matches byte-for-byte." This is the
same established policy under which `merge-join` and other plan-strategy-divergent
specs already pass. goopg renders no `BitmapOr` node (there is no bitmap-scan
operator in `internal/executor` / `internal/planner`), but because the plan body
is stripped, only the *success* of the EXPLAIN step and the surrounding
structure (step echoes, FETCH row data) are compared. The spec's real anomaly
check — `s1_fetch_all` → 0 rows — already passes on goopg's existing index-scan +
cursor + VACUUM machinery.

## The fix

`internal/planner/planner.go`, `case *parser.ExplainStmt`: unwrap a
`DeclareCursorStmt` inner to its `.Query` before planning, mirroring upstream
PostgreSQL's `ExplainOneUtility` → `ExplainOneQuery` dispatch for a
`DeclareCursorStmt` (the cursor is never created; only its query is planned and
rendered).

```go
explainInner := s.Inner
if dc, ok := explainInner.(*parser.DeclareCursorStmt); ok {
    explainInner = dc.Query
}
inner, err := Plan(explainInner, cat)
```

This is the standard PG behaviour for `EXPLAIN DECLARE c CURSOR FOR <query>` and
is useful beyond this spec (any `EXPLAIN DECLARE CURSOR` now works).

## Blast radius

Nil for non-`EXPLAIN-DECLARE-CURSOR` paths: the unwrap fires only when an
`ExplainStmt`'s inner is a `DeclareCursorStmt`. Plain `EXPLAIN <query>`,
`EXPLAIN EXECUTE`, and a bare `DECLARE … CURSOR` (executed, not explained) are
unchanged. No execution-engine change.

## Gates

- `TestExplainDeclareCursorExplainsInnerQuery` (new, `internal/executor`) — unit
  test that `EXPLAIN (COSTS OFF) DECLARE foo CURSOR FOR SELECT * FROM t WHERE
  data = 34` renders the inner query's scan + predicate and surfaces no
  cursor/declare node.
- `TestPort_IsolationIndexOnlyBitmapscan` strict PASS — byte-for-byte vs PG 18.3.
- `go build ./...` clean; `go test ./internal/executor/` (explain family) PASS.
- CSV `D-002` row promoted `failed` → `pass`; inventory + isolation-coverage md
  regenerated (`gen-oracle-inventory`, `gen-isolation-coverage`). Isolation tally
  now **117 pass / 4 failed**.
- pgbench smoke = pre-commit hook.

## Remaining M0118-0002 group blockers

The group stays open. Remaining deferred specs: `predicate-gin` (int[] + GIN AM
+ AM-grain SIREAD), `predicate-gist` (point type + GiST AM + AM-grain SIREAD).
`predicate-hash` over-detects 40001 (coarse relation-grain SIREAD vs PG's finer
hash-index predicate locking). These need genuine index access methods and are
Effort-L.
