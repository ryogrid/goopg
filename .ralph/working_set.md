# Working set — M0134-0001 S18 LANDED

**Task:** M0134-0001 (`aggregates.sql`), slice **S18 — `Sort Key:`/`Group Key:`
parenthesisation**. Selected per the Current Priority banner (M-NIGHTLY drained:
`ci/logs/action-items.md` still run `20260817-011734`, all 6 `[x]`; nothing new
to file).

**The finding worth carrying:** the extra paren pair is NOT an
expression-shape property. `show_sort_group_keys` (`explain.c:2767-2823`) adds
nothing; the pair comes from `ruleutils.c get_special_variable` ("force
parentheses for a non-Var referent"), reached only when the key is an
`OUTER_VAR` chased into a child's tlist by `resolve_special_varno`. It marks
**where the value is evaluated**. Corpus proof, same expression both ways:
`aggregates.out:3464-3465` `GroupAggregate`→`((g % 10000))` vs `:3500-3501`
`HashAggregate`→`(g % 10000)`. "Always wrap" would have been wrong.

**goopg's rule (structural proxy — no varno machinery, class-10 row 615):**
`*optimizer.Sort` wraps every non-`ColumnRef` key unconditionally; `Aggregate`
wraps iff `p.Child` is a `*optimizer.Sort`. Wrap sits INSIDE the
`DESC`/`NULLS`/`COLLATE` decoration. New `forceParen` — NOT `wrapParen`, which
is idempotent and would have silently no-opped on `(g % 10000)` (the scoping
research flagged this trap in advance; first slice this milestone where a
predicted trap was avoided rather than hit).

**Measurement:** `aggregates` **981 → 963 lines, 28 → 27 hunks** (beat the
predicted ~968). Blast radius across 8 cases: net **−18, zero growth**.

**Files:** `internal/executor/{operators_explain.go,explain_sortgroup_paren_test.go}`,
`docs/design/0134-0001-p2-explain-format.md` (S18 section) + README row.

**Gates run:** guard 5/5 PASS (3 FAIL-pre/PASS-post); UNITS suite PASS;
`scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35; pgbench smoke PASS via hook.

**Deferral ledger:** S15's parenthesisation row flipped `resolved`; 3 new rows
2026-08-17 — the proxy's two blind spots (non-`Sort` pass-through under an
Aggregate ⇒ under-wrap; the corpus's rarer single-wrapped
`Sort Key: (COALESCE(t3.q1))` ⇒ over-wrap), the absent per-set grouping-sets key
lines (must adopt this rule when M0125-0048 adds them), and **the regress gate
is not run-to-run deterministic**: `groupingsets` 2373-2377 and `subselect`
2845-2846 move on an unchanged tree. Only `functional_deps` (56) is a
trustworthy sentinel — stop quoting `groupingsets` 2373 as one.

**Next step:** continue **M0134-0001**. At 27 hunks: deparser/C11c 8 (confirmed
`ruleutils.c`-grade, own milestone — rule it out), S6 min/max-InitPlan 5
(`rewriteMinMaxAggregates` `planner.go:8814` already ports `planagg.c`; its gate
at `:8842-8848` bails on `OrderBy`/`Distinct`/multi-target — that gate is the
slice), the NEW unledgered VERBOSE `Output:` column-pruning gap 2, isolated-bug
residue. S6-gate is the cheapest confirmed next.

**Delegation:** `tmp/ralph-handoffs/m0134-0001-s18-scope/` (researcher, 1 round —
its full hunk table is the map for the next slice) and
`tmp/ralph-handoffs/m0134-0001-s18-sortgroup-parens/` (implementer 1 round,
tester 1 round; both reported as text, the harness blocked their report.md
writes).

**In-flight:** none.
