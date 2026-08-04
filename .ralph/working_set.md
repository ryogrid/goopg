(idle — nothing in flight)

M0127-P5.5-e-ii-a is CLOSED and committed. **The merge-join `createPlan` arm
exists, and with it the finding that goopg must DELETE the Sort nodes PG's
arm creates.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — `P5.5-e-ii-b`: `create_nestloop_plan` (createplan.c:4322) for
`PathNestLoop` (plain, `pathgen.go:109`) and the NLI paths
(`joinpathsnli.go:191`). It reuses `joinInputsFor`/`keyPairs`/`joinPredicate`
whole; what is its own is the parameter-binding contract with a parameterised
inner index path (`*NestedLoopIndexJoin` + the P5.4b-ii-b-2 Memoize/binding
contract) and the residual DROP via `indexPathClause.ri`/`ecID` the P5.5-c
ledger row waits on.**

Carry-over facts a next loop should not re-derive:

- **Clause coordinates are BINDING coordinates** (pre-search concatenation of
  every FROM item's schema). `relidsOfExpr` (joinrestrict.go:357) buckets
  `ColumnRef.Index` against those same offsets, so any new translator must use
  `scopeIgnore` to keep agreeing with it (rule #2).
- The join prologue is now ONE function: `joinInputsFor(p, kind, outerPath,
  innerPath)` → `joinInputs{outer,inner,outerRelids,innerRelids,merged,lay,
  index}`, plus `keyPairs` (orientation + translation, ORDER PRESERVED) and
  `joinPredicate` (keys + residual folded into `Join.Predicate`). The NL arm
  should not re-write any of it.
- **`Join.HashKeys` order IS the merge sort order** (`mergeSideKeyExprs` →
  `mergeSortedSource.less`), and `fillJoinHashKeys` REBUILDS that list from
  `Predicate` at the tail of `Plan()` — so key conjuncts must be appended in
  key order or the rebuild re-orders the sort.
- **goopg's `JoinAlgoMerge` sorts both inputs itself** (`openMergeJoin`), fixed
  ascending / NULL-keys-last. Hence `absorbMergeSort` + the two ordering
  refusals. NL has no such absorption question.
- `createSortPlan` now TRANSLATES its pathkey exprs onto the child layout (was
  a latent P5.5-d defect: untranslated keys sorted by whatever sat at that
  binding index). Nil child layout still passes through — ledgered.
- **P5.5-f still open** (03 §10 boundary map); **P5.6 `sizeJoinRel` still
  open**. `GOOPG_PGSHAPED_DP` stays OFF. **P4.1 ledger row #3 still open**
  (`mergeJoinStream.bufferGroup` twin).
- Do NOT `git stash`; repo gofmt baseline go1.25 (never wholesale `-w`);
  `cd` persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh`. DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p55eiia.log`);
SPOT PASS (`/tmp/spot_p55eiia.log`, Q12=2 Q13=35 canonical, 28.5s); pgbench
SMOKE via the commit hook. DS05 not applicable — the arm is reachable only from
the inert search.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed.
Nothing new.

In-flight: none.
