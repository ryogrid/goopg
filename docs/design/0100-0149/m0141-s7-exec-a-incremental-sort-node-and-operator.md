# M0141-S7-exec-a — the `IncrementalSort` optimizer Node type and the executor operator

Status: accepted (landed `99a0a39a6`, 2026-09-17). Production change: `internal/optimizer/incrementalsort.go`, `internal/executor/operators_incremental_sort.go`, plus two `operators_explain.go` arms forced by the package's hard coverage gates.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-17e — M0141-S7-exec-a LANDED

Landed both halves exec-a scoped:

- `internal/optimizer/incrementalsort.go` — the `IncrementalSort` Node type.
  Mirrors `Sort` (`plan.go`) field-for-field (`PlanCost`, `searchedTree`,
  `pos`, `Child`, `Keys`) plus one addition, `PresortedCount` (PG's
  `nPresortedCols`, `plannodes.h`). Zero production callers: no
  `createPlanNode` arm exists yet (that is exec-b). 1 test file
  (`incrementalsort_test.go`), pins `Pos()`/`Output()`/field storage and
  that the type satisfies `Node`.
- `internal/executor/operators_incremental_sort.go` — the `incrementalSortOp`
  executor operator. Pull-based (`Open`/`Next`/`Close`/`Schema`), all in
  `Open`: drains the child fully, splits into presorted-prefix groups as
  they arrive using `sortPrefixEqual` (E-15's contract — a group closes the
  moment a row's leading `PresortedCount` keys stop matching the previous
  row's), sorts each closed group **by all keys** (not just the trailing
  ones) via a permutation-based `sort.SliceStable`, streams groups out in
  arrival order from `Next`. Sorting by all keys rather than just the
  trailing keys is deliberately simpler than PG's own per-group tuplesort
  scope and is behaviourally identical: every row in a closed group already
  shares the same leading-key values, so comparing the full key vector adds
  ties that never break, never a wrong order. 5 tests
  (`operators_incremental_sort_test.go`), including two that cross-check
  output against `sortOp` (the existing full-sort oracle) for order-
  equivalence over presorted and NULL/DESC-prefixed input, one for the
  `PresortedCount=0` degenerate (single group, must equal a plain full
  sort), and one that pins the actual per-group sort (not just end-to-end
  order) by feeding each group in reverse-sorted arrival order.

**Discovery not in the original four-way split**: adding the `IncrementalSort`
Go type — even with zero production callers — immediately failed two
existing hard gates in `internal/executor`:
`TestEveryPlanNodeTypeHasAnExplainArm` and `TestEveryPlanNodeWithChildrenIsWalked`
(`explain_node_coverage_test.go`). Both enumerate plan node types by
reflection/AST-scan over the whole package, not by construction reachability
— they fire the moment a new `optimizer.Node` implementer exists at all,
regardless of whether the planner can ever build one. Of exec-c's own
"6 `operators_explain.go` sites", exactly 2 are behind these hard gates:
`describePlanMode`'s label switch and `planChildren`'s child-walk switch.
Declining to fix these (via `explainCoverageExempt`/`childWalkExempt`) was
rejected as the wrong call here: the fix is one line each, mirrors `Sort`'s
own arm exactly, and the exempt maps' own header comment frames exemption as
an argued-for exception, not a default — so both arms were added now rather
than deferred:

- `describePlanMode`: `case *optimizer.IncrementalSort: return "Incremental
  Sort"` (the label string was already reserved for this in
  `estimateaudit/parity_test.go:58`'s map key).
- `planChildren`: `case *optimizer.IncrementalSort: return
  []optimizer.Node{p.Child}` (identical shape to `Sort`'s own arm).

This **narrows exec-c's remaining scope** to the other 4 of the original 6
sites: `resolveKeySource`, `childNodeOf`, `execParamOwnerChildren` (all
decline gracefully today, matching every other not-yet-relevant Node type —
safe, not gated by any test) and `emitNodeDetailLines`'s `Sort Key:` line,
which still needs the real `Presorted Key:` extension
(`nodeIncrementalSort.c`/`explain.c` oracle) — that is exec-c's one
substantive remaining item.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...` and
`./internal/executor/...` both green (including the two previously-failing
coverage tests, now passing); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — only pre-existing failure is
`internal/parser` (the tracked `GroupedJoinUnaliased` AST-drift issue,
`.ralph/fix_plan.md`'s "Manually discovered" entry, filed 2026-09-15,
untouched by this change). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench
schema not currently loaded — CLAUDE.md's M0142-0003k blocker, pre-existing
and unrelated); moot regardless since this task has zero production callers,
so no plan can change. `GOOPG_INCREMENTAL_SORT` stays default-off; nothing
in this update touches the flag or `addIncrementalSortPaths`.

Resume point: **M0141-S7-exec-b** — `createplansimple.go`'s `createPlanNode`
arm for `PathIncrementalSort` (needs to decide the real `PresortedCount`
value from the winning `*Path`'s recorded prefix — `addIncrementalSortPaths`
computes `nCommon` today but does not stash it on the `*Path`; check whether
a new `Path` field or a re-derivation via `pathkeysCountContainedIn` at
`createPlanNode` time is cheaper), both `executor.go` builder sites (classic
`buildNode` + slab/tree fast path), and the four mechanical tree-walkers
(`scan_deform.go` x2, `subplan.go`, `operators_cte_dml.go`). This is the
correctness-gating step before `GOOPG_INCREMENTAL_SORT=on` can ever be
pointed at the corpus.
