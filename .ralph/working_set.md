(idle — nothing in flight)

Last loop (#6, 2026-07-31) closed **M0125-0039** — EXPLAIN column
qualification. `internal/executor/explain_names.go` (new) + qual-site changes
in `operators_explain.go`; design
`docs/design/0125-0039-explain-column-qualification.md`; tests
`internal/executor/explain_qualify_test.go` (8 cases).

Upstream's rule turned out to be TWO mechanisms, both taken from the oracle:
explain.c splits the prefix decision by node kind (`show_scan_qual` prints a
scan's `Filter:` BARE; `show_upper_qual`/`show_sort_group_keys` prefix once
`es->rtable_size > 1`), and ruleutils.c `get_parameter` FORCES prefixing for a
Param's expansion — goopg's `OuterColumnRef`. Measured on :65437 vs PG :65438
(plain EXPLAIN, nothing executed, arm
`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0039/`): **Q30 and Q81 now
print `Filter: (ctr1.ctr_state = ctr_state)` — byte-identical to PG 18.3**;
Q64 → `(cd1.cd_marital_status <> cd2.cd_marital_status)`; Q72 names `d1`/`d2`.

Two findings the next loop should not have to rediscover:
1. **`SourceTableIdx` is NOT a range-table id.** `planner.go`'s `nextSourceIdx`
   restarts at 1 for EVERY query level, so an outer subquery binding collides
   with a base relation inside it. The naive version printed a *wrong* relation
   name (`(a.s1 <> a.s2)` where `s2` came from `b`) — worse than printing none.
   Contained by a column-membership guard + ancestor-match uniqueness gate; the
   real fix is PG's `varno` and is planner work with plan-shape blast radius.
   This is why **Q31 is only partial** (2 of 12 conjuncts qualify).
2. A CTE body ending in an aggregate ZEROES `SourceTableIdx`, so Q30's
   correlation is unresolvable from the id alone. Solved the way upstream does
   — resolve against the ancestor plan node (`push_ancestor_plan`).

Ledger: 2 rows appended 2026-07-31 (the `varno` gap; VERBOSE-forced prefixing +
`Output:` qualification + `_N` suffix numbering).

Per the banner's adopted order the **next selection is `M0125-0034`**
(`M0125-0037`(i) → `-0039` → **`-0034`** → `-0035` → `-0036` → `-0037`(ii) →
`-0038`). Re-read the banner before selecting; it outranks this note.

Host note: the nightly CI batch (run `20260731-001201`) was live all loop. The
SF0.5 goopg cluster was started/stopped twice with
`GOOPG_BIN=tmp/goopg-m0125-0039-bin`, so the shared `tmp/goopg-bench-bin` was
never rebuilt under the nightly; both goopg TPC-DS clusters are DOWN again, as
they were found.
