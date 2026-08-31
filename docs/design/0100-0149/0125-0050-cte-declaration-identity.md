# M0125-0050 — a CTE's runtime identity is its DECLARATION, not its name

Status: landed 2026-08-06. Supersedes the name-keying decision recorded in
[0125-0049](0125-0049-explain-shared-cte-section.md) §"hoist key".

## 1. The bug

`ctx.CTERowCache` was `map[string][]Row` keyed by the **lowercased CTE name**,
statement-wide. `cteScanOp.Open` buffers the rows on the first scan of a key and
replays them for every later scan of that key, with no notion of *which
declaration* the name came from. Two unrelated declarations that happen to share
a name therefore shared one buffer, and the second replayed the first's rows:

```sql
SELECT v FROM (WITH x AS (SELECT 1 AS v) SELECT v FROM x) a
UNION ALL
SELECT v FROM (WITH x AS (SELECT 2 AS v) SELECT v FROM x) b
```

| engine | result |
|---|---|
| PostgreSQL 18.3 | `1, 2` |
| goopg (before) | `1, 1` |

This is a **wrong answer**, not a plan-shape difference. `SELECT 2` was planned
correctly and never ran.

PostgreSQL cannot express the bug. `SS_process_ctes`
(`optimizer/plan/subselect.c`) turns each WITH entry into its own subplan of the
declaring query level, and `CteScanState` keys its tuplestore off that subplan's
`ctePlanId` (`executor/nodeCtescan.c`) — per-declaration by construction. Name
never enters the runtime identity at all.

## 2. Why the obvious keys are both wrong

The fix is a key change, and the two cheap candidates each fail in one
direction. This is the whole content of the task; the code change is six lines.

### 2.1 `plannedCTE` pointer — under-shares

One declaration can legitimately be planned **more than once**. `planSelect`
re-enters on the head operand of a set-op chain (the `cutAt` segment logic), so
M0125-0040's grouping-sets rewrite — which hoists FROM+WHERE into a synthetic
`__gs_src_<pos>` CTE precisely so the N generated UNION branches share ONE
execution of the source — produces two distinct `*plannedCTE` values for the one
synthetic declaration. Measured on a 3-branch ROLLUP:

```
CTEScans: 3
  [0] name=__gs_src_94 declSeq=2 ctePtr=0x…850 bodyPtr=0x…ea0
  [1] name=__gs_src_94 declSeq=1 ctePtr=0x…7e0 bodyPtr=0x…840
  [2] name=__gs_src_94 declSeq=1 ctePtr=0x…7e0 bodyPtr=0x…840
```

Two pointers, two `declSeq`s, one declaration. Keying by either would split the
buffer and run the hoisted join twice — undoing M0125-0040 on the exact queries
it was filed for (TPC-DS Q18, Q67). It compiles, and it passes a name-only
assertion.

`declSeq` (added by -0049) is therefore **not** reusable as an identity: it is a
per-*planning-pass* counter for render ordering, not a per-declaration id.

### 2.2 CTE name — over-shares

The bug in §1.

## 3. The key that lands: the declaration site

`planner.CTEScan.DeclKey()` returns `"<declPos>:<lowercased name>"`, where
`declPos` is the source offset of the declaring `parser.CommonTableExpr`
(carried on `plannedCTE.declPos`). It is goopg's analogue of `ctePlanId`.

Why the *site* rather than an assigned id:

- **Stable across replanning.** Both grouping-sets preplan passes read the same
  `CommonTableExpr`, so both derive the same key — sharing preserved.
- **Distinct per declaration.** There are only three producers of a
  `CommonTableExpr`, and each gives distinct declarations distinct `(pos, name)`
  pairs: the parser (`internal/parser/with.go`) stamps `pos` from the declaring
  identifier token; the two synthetic producers
  (`internal/planner/groupingsets_share.go`, and CREATE RECURSIVE VIEW in
  `internal/parser/ddl.go`) leave `pos` 0 but generate a name that is unique
  within the statement (`__gs_src_<spec.Pos()>` / the view's own name).
- **Race-free by construction.** No counter and no lazily-mutated AST field, so
  two sessions planning concurrently cannot disagree about a key. (The
  pre-existing `cteDeclSeq` global is untouched; it only affects render order.)

Falls back to the bare name when `cte == nil` — a `CTEScan` built outside
`preplanWithClause`, i.e. in tests, where a plan has at most one declaration per
name.

## 4. EXPLAIN moves with it

-0049 keyed `collectCTEHoist` by name and said explicitly that the key "has to be
fixed with" this bug. It now keys by `DeclKey`, so:

- the grouping-sets source still prints **one** `CTE __gs_src_*` section
  (both passes, one key), and
- two same-named declarations now print **two** `CTE x` sections with a body
  each — which is also what PG prints, one heading per subplan.

The -0049 divergence "sections are hoisted to the root of the rendered plan"
becomes more visible here: PG nests each section under its declaring query level,
so the two `CTE x` headings would be distinguishable by position; goopg lifts
both to the root. That divergence is unchanged and already carries a ledger row.

## 5. Verification

| gate | result |
|---|---|
| `TestCompatCTESameNameDisjointScopes` (new) | `1, 2` — the witness |
| `TestCompatCTEMultiReferenceStillMaterializesOnce` (new) | one declaration, two references, still one materialization |
| `TestExplainSameNameDisjointScopesSectionedTwice` (new) | 2 sections, 2 bodies, 2 reference leaves |
| `TestGSShareSourceAppliesToJoinedRollup` (strengthened) | asserts on `DeclKey`, not name — the guard against §2.1 |
| `TestExplainGroupingSetsSharedSourceSectioned` | still exactly 1 section |
| `internal/planner`, `internal/executor` | PASS |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | Q12=2, Q13=35 — canonical |
| `scripts/tpcds-sf05-regression.sh plans` | `queries=99 same=99 changed=0` |

`same=99` is the meaningful one for §2.1: TPC-DS has no same-name-different-scope
CTE, so the corpus should be untouched — and would have moved on the 30 `WITH`
queries plus the grouping-sets ones had the key un-shared anything.

## 6. Deferred (ledger rows filed 2026-08-06)

1. **`MaterializedCTEs` is still name-keyed.** The DML-CTE sibling
   (`ctx.MaterializedCTEs map[string][][]Datum`, RETURNING rows) has the
   identical aliasing shape and was NOT changed. It is only unreachable in valid
   SQL because PG requires data-modifying CTEs at the top level, where names are
   unique — and see (2).
2. **goopg does not enforce PG's top-level rule for data-modifying CTEs.**
   Probed at HEAD: goopg accepts
   `SELECT v FROM (WITH x AS (INSERT … RETURNING a) SELECT a AS v FROM x) s`,
   where PG 18.3 raises 42P19 "WITH clause containing a data-modifying statement
   must be at the top level" (`parser/parse_cte.c:336`). That missing check is
   what makes (1) reachable, so (2) is the one to fix first.
