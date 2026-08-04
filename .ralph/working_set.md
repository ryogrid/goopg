(idle — nothing in flight)

M0127-P5.5-f-i is CLOSED. **The search boundary now exists**
(`internal/planner/createplanroot.go`): `createPlanAtSearchRoot(p,
bindingWidth)` is the only `createPlan` entry point a search caller may use, and
it republishes the root's row in pre-search BINDING order.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — the P5.5 PARENT, i.e. `P5.5-f-ii` (IMPLEMENTATION-TODO): pinned-spine
re-resolution (`predp.go`) consumes the boundary map; searched-subtree TAGGING
so the `buildBindingsPosMap` / `applyJoinTreePosMap` family skips;
`reconcileNLILayout` no-op assertion on searched trees;
`assertColumnRefsWithinSchema` widened from the boundary node to the whole
enclosing tree. Plan-snapshot re-baseline in the SAME commit. Bar: UNITS + SPOT
+ DS05 + PLAN (re-baseline).**

Carry-over facts a next loop should not re-derive:

- **At the search ROOT, canonical relid order == pre-search binding order.**
  `buildInitialRels` gives FROM item i relid `1<<i` with ascending
  `baseOffset`, and the root relset is full. That is why the boundary `Project`
  IS 03 §10's canonical layout, not a detour around it — and why nothing above
  the root needs rewriting.
- `bindingWidth` is a PARAMETER, never `len(layout)`: a FROM item that never
  entered the search yields a root that is permutation-clean against its own
  width and short of columns the enclosing tree references (M0097-0058 shape).
- Clause coordinates are BINDING coordinates; `relidsOfExpr`
  (joinrestrict.go:357) buckets `ColumnRef.Index` against the same offsets, so
  any new translator must use `scopeIgnore` (rule #2).
- The join prologue is ONE function: `joinInputsFor` + `keyPairs` +
  `joinPredicate`; all three arms reuse it. NLI is the only node with TWO
  coordinate spaces (`createplannl.go`).
- `boundaryMap`'s duplicate branch is defensive: `joinInputsFor`'s
  `bindingIndex()` already panics on a duplicate before the root sees it.
- **P5.6 `sizeJoinRel` open**; `GOOPG_PGSHAPED_DP` stays OFF. **P4.1 ledger row
  #3 still open** (`mergeJoinStream.bufferGroup` twin).
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; the 8 new boundary tests PASS; UNITS PASS
(exit 0, 0 FAILs, `/tmp/units_p55fi.log`); SPOT PASS (`/tmp/spot_p55fi.log`,
Q12=2 Q13=35 canonical, 28.9 s); pgbench SMOKE PASS via the commit hook (0 failed
on all 3 scripts, 13.9k TPS select-only). DS05 not applicable — the boundary is
reachable only from the inert search. Committed + pushed as `8f1ae13d`.

**Smoke flake, second sighting:** the FIRST hook attempt failed with 1 failed txn
in 14417 (`client 1 script 0 aborted … current transaction is aborted`) and ZERO
ERROR lines in the gate server log. Identical to the 2026-08-03 ledger row
(M0125-0027) — appended a second-sighting note there; it is recurring, not a
one-off, and stays undiagnosable until goopg logs statement-level ERRORs the way
PG's `errstart` does. Retry was clean. Do not re-bisect it blind.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
