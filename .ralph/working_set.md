(idle — nothing in flight)

Last loop: **M0127-P5.6-f-iv** — REFUTED as filed, closed, successors filed.
Committed + pushed. Facts the next loop must NOT re-derive:

1. **PG has NO functional-dependency arm for JOIN clauses.**
   `clauselist_selectivity_ext` (clausesel.c) gates extended statistics on
   `find_single_rel_for_clauses`, which returns NULL at the first clause whose
   `clause_relids` is not a singleton. A join clause has 2 relids ⇒
   `dependencies_clauselist_selectivity` never runs on one. Extended stats are
   a RESTRICTION-clause mechanism. Do not re-file a correlation damper.
2. **Measured:** PG 18.3 SF0.5 (`EXPLAIN query47.sql` on :65438 db `tpcds05`)
   estimates BOTH correlated 5-pair joins at `rows=1` — same collapse as goopg.
   PG still picks Merge Join because its `CTE Scan on v1` is **7 643 rows**;
   goopg's is **18**, so a nested loop looks free. The 425× predates P5.6-f
   (`30293f78` has the same 18 and still picks Hash Join).
3. **The real defect (→ P5.6-f-vi):** a pushed-down restriction is charged a
   SECOND time at the join above it. Probe table (goopg SF0.5, :65437, db
   `postgres`, 4-table item/store_sales/date_dim/store join, row-preserving
   `⋈ store`): none ⇒ 1 439 608→1 439 608 (×1.0); `d_year=2000` ⇒ 7 193→35;
   `d_dom=15` ⇒ 7 193→35; Q47's OR ⇒ 7 252→36; `d_year>1999` ⇒
   726 987→367 128 (×0.505 = the scan's own factor). PG: 2 583→2 465.
4. **Ruled out, do not re-walk:** `exprSide` is correct in isolation
   (`col = const`→sideLeft, `col = col`→sideMixed), so
   `joinResidualSelectivity`'s guard is not the leak. Prime suspect:
   `joinEquiPairs`→`splitAllEqualitiesForHash` admitting `col = const` as an
   equi-pair. Caveat: `d_year > 1999`'s 0.505 is neither `1/nd` nor
   `defaultEqSelectivity`, so that arm alone can't explain every row.
5. goopg SF0.5 probes need NO `ANALYZE` preamble (stats persist, M0125-0028/-0029),
   which is why `sf05_capture_plans` has none. db name is `postgres`, not `tpcds05`.

Files: doc 09 §5.17 (+ correction box on §5.15), `analysis/m0127-p56fiv/README.md`,
§6 of `analysis/m0127-p56fiii/README.md` retracted in place, fix_plan
(P5.6-f-iv [x], P5.6-f-vi / -f-vii filed), 2 ledger rows. No Go source changed.

Gates run: UNITS green (planner 0.588s, rest cached); `make ralph-state-guard`;
commit-hook pgbench smoke. No DS05 sweep — nothing executable changed.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), take **M0127-P5.6-f-vi** —
the double-charge. Start with the discriminating unit test named in the item
(join whose LEFT is a `*Filter` over a scan, same conjunct still in
`Predicate`; assert the estimate is scaled exactly once), then fix
`estimateJoin`'s pair loop. It moves plan shape broadly, so capture `plans`
before and after and accept on a named-victim TIMEOUT-set diff.

In-flight: none.
