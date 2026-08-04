(idle — nothing in flight)

M0127-P5.5-f-ii-a is CLOSED. **The searched-subtree tag exists**
(`internal/planner/searchedtree.go`): `searchedTree` is a one-bit embedded tag
on the 7 node kinds `createPlanAtSearchRoot` can return as a root, and
`buildBindingsPosMap` / `applyJoinTreePosMap` / `reconcileNLILayout` all skip it.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — the P5.5 PARENT, i.e. `P5.5-f-ii-b` (IMPLEMENTATION-TODO): pinned-spine
re-resolution consumes the boundary map (`predp.go` — the map is the IDENTITY
above the root, so `layoutPosMap` should come out nil and the spine
re-resolution be *provably* skipped, not merely unexercised);
`assertColumnRefsWithinSchema` widened from the boundary node to the whole
enclosing tree. Plan-snapshot re-baseline in the SAME commit. Bar: UNITS + SPOT
+ DS05 + PLAN (re-baseline).**

Carry-over facts a next loop should not re-derive:

- **The boundary `Project` was ALREADY opaque to the legacy posmap family** —
  M0125-0012 made every `*Project` in a join tree a scope boundary on BOTH
  sides (collect advances past, applyJoinTreePosMap returns). Measured probe:
  `buildBindingsPosMap` returns nil over it, no target moves. The hole the tag
  closes is the **ELIDED** root (bare `*Join`), where `reresolveJoinByName` /
  `reconcileNLILayout` would rebind by NAME over a coordinate-derived layout.
- **P5.5-e fixtures build clause operands with `col(i)` — UNNAMED.**
  `reresolveJoinByName` returns immediately on an unnamed ColumnRef, so any
  name-resolution assertion reusing them passes VACUOUSLY. `searchedtree_test.go`
  has `stNamedEqui` / `stHashRoot`; use those.
- `reconcileNLILayout` is now a guard + `reconcileNLILayoutBody`; the body is
  what the assertion runs. `reconcileNLILayout` only runs when
  `costDrivenJoinOrder` is ON (Plan()), so the skip is mostly latent today.
- At the search ROOT canonical relid order == pre-search binding order
  (`buildInitialRels`: relid `1<<i`, ascending `baseOffset`, full relset).
- `bindingWidth` is a PARAMETER, never `len(layout)` (M0097-0058 shape).
- **P5.6 `sizeJoinRel` open**; `GOOPG_PGSHAPED_DP` stays OFF. **P4.1 ledger row
  #3 still open** (`mergeJoinStream.bufferGroup` twin).
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; 10 new tag tests PASS; UNITS PASS (exit 0,
0 FAILs, `/tmp/units_p55fiia.log`); SPOT PASS (`/tmp/spot_p55fiia.log`, Q12=2
Q13=35 canonical, 29.9 s); pgbench SMOKE via the commit hook. DS05 not
applicable — the tag is set only by the inert search boundary.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
