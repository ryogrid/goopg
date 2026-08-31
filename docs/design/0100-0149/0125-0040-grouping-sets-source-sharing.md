# M0125-0040 — grouping sets re-scan the source once per set ("C6")

Status: **landed 2026-08-06** (default ON, `GOOPG_GS_SHARE_SOURCE=0` reopens).
Scope: `internal/planner/groupingsets_share.go`, one call site in
`rewriteGroupingSets` (`internal/planner/planner.go`).
Filed by `M0125-0037`(i) on 2026-07-31; evidence
`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/q18.txt`, `q67.txt`.

## 1. The defect

`rewriteGroupingSets` expands `GROUP BY ROLLUP/CUBE/GROUPING SETS` into the
SQL:1999 §7.9 UNION ALL of one plain-`GROUP BY` branch per generated set. The
expansion is semantically exact and is not what this task changes. What it did
NOT do is share anything between the branches: each generated `SelectStmt`
carried its own copy of `From`, `FromExprs` and `Where`, so the whole join
subtree was planned **and executed** once per set.

Measured at SF=0.5 before this change:

| query | branches | MHJs in plan | full fact-table scans |
|---|---|---|---|
| Q18 | 4 | 5 | 5 × `catalog_sales` (720,657 rows each) |
| Q67 | 8 | 9 | 9 × `store_sales` (1,439,608 rows each) |

Neither query's SQL contains a `UNION ALL` — the `Append` is goopg's own
grouping-sets expansion, which is why the class was invisible to
`M0125-0026`'s timeout classification, and why it was mis-attributed for a
while to join-order or cardinality work. An 8× multiplier on a 1.44 M-row join
is not something a cardinality input can recover.

PostgreSQL computes every level in ONE pass over ONE scan. Q18's PG plan is a
single `GroupAggregate` with five stacked `Group Key:` lines (the last
`Group Key: ()`); Q5's is a `MixedAggregate` with `Hash Key` lines. The
executor side is `nodeAgg.c`'s `AGG_MIXED` / `AGG_SORTED` phases (one hash
table per hashable set, all fed from a single input stream); the planner side
is `preprocess_grouping_sets` / `consider_groupingsets_paths` in
`postgres/src/backend/optimizer/plan/planner.c`.

## 2. The two candidate fixes, and which one landed

The filing named two, cheapest first:

- **(a)** materialize the common subtree once and let the N branches read it —
  keeps the expansion, removes the re-scan;
- **(b)** implement PG's real multi-level `AGG_MIXED`/`AGG_SORTED` aggregate —
  the faithful answer, which also fixes the `Group Key: ()` plan shape.

**(a) landed.** It buys the whole runtime win at a fraction of (b)'s blast
radius, and — the reason it is cheap at all — goopg *already has* the
materialize-once mechanism it needs. A non-recursive CTE is buffered on its
first reference and REPLAYED from `ctx.CTERowCache` by every later one
(`internal/executor/operators_cte_dml.go`, `cteScanOp.Open`), which is
PostgreSQL's CTE optimization-fence semantics and is exactly "execute once,
feed many". So the fix is an AST rewrite, not a new executor node.

(b) is **not** obsoleted by this and is not folded into this item — see §5.

## 3. What the rewrite does

`shareGroupingSetsSource` runs inside `rewriteGroupingSets`, before the
per-set branches are built and before the `universe`/`active` key sets are
computed (both must be derived from the already-rewritten expressions). It
hoists `FROM` + `WHERE` into one synthetic CTE and points the statement at it:

```
SELECT i_item_id, ca_state, avg(x)      WITH __gs_src_42 AS MATERIALIZED (
FROM a, b, c                              SELECT a.i_item_id AS __gs_c0,
WHERE <join + filter quals>          =>          b.ca_state  AS __gs_c1, ...
GROUP BY ROLLUP(i_item_id, ca_state)      FROM a, b, c WHERE <quals>)
                                        SELECT __gs_c0 AS i_item_id, ...,
                                               avg(__gs_c2)
                                        FROM __gs_src_42 GROUP BY __gs_c0, __gs_c1
                                        UNION ALL … (one branch per set)
```

Four details that are decisions, not incidentals:

1. **The CTE projects exactly the referenced columns, never `*`.** The
   materialized footprint is the narrow projection, not the join's full width
   — which is what keeps a rewrite that trades time for memory affordable.
2. **The CTE name is `__gs_src_<parse position>`.** Deterministic for a given
   statement text (so plan snapshots stay comparable) and distinct per
   grouping-sets construct in the same statement, which matters because the
   runtime cache is keyed by name.
3. **Output column names are pinned.** The hoist renames every projected
   column to a generated `__gs_cN`; a target with no `AS` clause takes its
   output name from its expression, so a bare column would silently be renamed
   and the statement's own `ORDER BY` would stop resolving. `gsImplicitTargetName`
   restores the original name as an explicit alias, descending `CAST`/`COLLATE`
   the way PG's `FigureColname`
   (`postgres/src/backend/parser/parse_target.c`) does.
4. **`Materialized: "materialized"` is set explicitly** even though goopg
   materializes every non-recursive CTE today. Sharing is the rewrite's whole
   purpose; it must not become an inlining candidate if that policy changes.

## 4. Fail-closed is the governing rule

Every helper returns an `ok bool` and the caller leaves the statement
*completely* untouched when any part declines — the query still runs, with
today's N-scan expansion. Three hazards are closed this way, and the first was
found only by writing the guard:

- **Correlation.** A CTE body cannot be correlated. If the grouping-sets
  SELECT is a correlated subquery, its `WHERE` references an enclosing
  relation, and hoisting `WHERE` would turn a working query into a 42703 plan
  error. `WHERE` is walked with a *verify-only* resolver (it stays inside the
  body, so it is checked but not rewritten): every reference must resolve to a
  `FROM`-clause base table via the catalog, and an outer reference resolves
  nowhere. The first draft of this rewrite checked only the target list and
  would have shipped that regression.
- **Name shadowing.** A sublink brings its own `FROM` scope, in which an
  unqualified name may not mean what the resolver decides. Any subquery in a
  rewritten expression — or in `WHERE` — declines the rewrite.
- **Unknown expression shapes.** `rewriteGSExpr` is an exhaustive type switch
  over parser's `Expr` implementations with **no pass-through default**. A
  node type added later fails closed instead of silently carrying a base-table
  reference into a scope where that table is no longer visible.

Also declined: explicit `JOIN` syntax (`FromExprs` carrying `Joins`), derived
tables / table functions / `LATERAL` / CTE references in `FROM`, duplicate
`FROM` qualifiers, ambiguous unqualified columns, `FOR UPDATE`, a `WINDOW`
clause, a single generated set (nothing to share), and a construct that
projects no columns at all.

## 5. What is still owed — (b), and the EXPLAIN rendering

Two things this deliberately does not do, each with a deferral-ledger row
dated 2026-08-06:

- **(b) is still the faithful answer.** goopg has no multi-level grouping-sets
  aggregate, so the `Group Key: ()` grand-total shape is still expressed as a
  UNION ALL branch, and the source is *materialized* where PG streams it. The
  memory/time trade this makes is real: PG's single pass needs no buffer at
  all. Resume point: a `GroupingSetsAgg` plan node carrying `Sets [][]Expr`
  plus a grouping-set id column, and an executor operator keeping one hash
  table per set, modelled on `nodeAgg.c` `AGG_MIXED`. It would replace this
  rewrite rather than extend it.
- **EXPLAIN still renders the body N times.** `preplanWithClause` clones a CTE
  body per consumer, so Q67's plan shows eight `CTE Scan on __gs_src_871`
  nodes each with a full copy of the four-table join — 36 `store_sales`
  mentions. The *execution* is one (see §6), but the item's literal acceptance
  wording, "Q67's plan shows ONE scan of `store_sales`", is met in runtime and
  not in rendering. PG prints a CTE body once, above the plan that references
  it. Resume point: `internal/executor/operators_explain.go` — render the
  first reference's body under a `CTE <name>` heading and print later
  references as bare `CTE Scan` lines.

## 6. Verification

**TPC-DS SF0.5, `scripts/tpcds-sf05-regression.sh sweep`, subset probe over
the six ROLLUP queries the guard accepts** (`QUERIES="18 22 27 67 77 80"`,
report `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260806-140755.txt`,
private binary, S-cold, 300 s cap):

```
PASS=6 (1 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=0
```

Against the immediately preceding report (`sweep-20260806-110031`, same
script, same cluster, adjacent commits):

| query | before | after | speedup |
|---|---|---|---|
| Q67 | 82 s | 14 s | **5.9×** |
| Q18 | 37 s | 8 s | **4.6×** |
| Q27 | 31 s | 10 s | **3.1×** |
| Q22 | 21 s | 7 s | **3.0×** |
| Q77 | 17 s | 17 s | — (declined; see below) |
| Q80 | 15 s | 15 s | — (declined) |

Row counts identical everywhere; Q77's value checksum matches the oracle.
The gate's own plan channel covers all 99 queries and reports
**`same=95 changed=4 added=0 removed=0`** — the four changed are exactly
Q18/Q22/Q27/Q67, so nothing outside the grouping-sets path moved. Q77 and Q80
write `ROLLUP` too but their grouping-sets SELECT reads CTEs rather than base
tables, so the guard declines them: they are the fail-closed path measured in
production, unchanged in plan and in time.

The four speedups are themselves the "one execution" evidence — an 8-branch
query cannot get 5.9× faster while still running its join eight times. The
claim is also pinned as a unit test that cannot pass by accident:
`TestGroupingSetsShareSourceExecutesSourceOnce` puts `nextval()` in `WHERE`
and asserts the sequence advanced 3 times for a 3-row source under a 3-branch
`ROLLUP` — 9 under the old expansion.

Other gates: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
green; the pre-existing `grouping_sets_compat_test.go` ROLLUP/CUBE/GROUPING
SETS/`GROUPING()` answer pins now run through the hoisted path unchanged; new
planner tests pin both the applies and the declines arms
(`internal/planner/groupingsets_share_test.go`) and new executor tests pin the
declined shapes' answers end to end
(`internal/executor/grouping_sets_share_test.go`). `make plan-diff` (TPC-H) was
not run: TPC-H contains no grouping-sets query, so no TPC-H plan can reach
this code, and the SF0.5 plan channel above is the stronger statement.

## 7. Reopen path

`GOOPG_GS_SHARE_SOURCE=0` (or `off`/`false`/`no`) restores byte-identical
pre-M0125-0040 plans. Anything else — including the empty string and an
unparseable value — is the shipped default, following the M0125-0005
convention that a typo may never silently hand an operator a planner
production does not run. The flag is stamped into every benchmark artefact via
`FlagProvenanceTable()` / `scripts/planner-flags.env`, so a TPC-DS report
always says which arm it measured.
