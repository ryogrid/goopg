(idle — nothing in flight)

M0127-P5.5 is CLOSED — its last sub-item P5.5-f-ii-b landed
(`internal/planner/enclosingtree.go` new + the `predp.go` call site): the
pinned spine asserts the boundary map is the identity and skips its
re-resolution, and 03 §10's tripwire is widened from the boundary node to the
enclosing tree.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — `M0127-P5.6`: `calcJoinrelSize` + FK-superkey generalisation +
eqjoinsel + FK clamp (04 §3.1-3.3, the Q9 class-(a) fix); delete the quadratic
build penalty; estimate-audit tooling (09 §5 — Q9's chain ≤ 10²× at the final
joinrel). Re-evaluate M0125-0003 stage 3 there (rows-once per RelOptInfo,
04 §2). Bar: UNITS + DS05 + estimate audit run. It is LARGER than one loop —
decompose it the way P5.5-e/-f were decomposed.**

Carry-over facts a next loop should not re-derive:

- **`layoutPosMap` returns nil for TWO reasons** — "identical" and "widths
  differ, refuse to remap". Never read nil as "the map is the identity";
  `assertSpineConsumesIdentityBoundaryMap` compares schemas itself.
- **`enclosingNodeScopeOf` enumerates 11 of 53 node kinds** and STOPS at the
  rest; the vacuity guard (walk must reach a tagged subtree, else panic naming
  `stoppedAt`) is what keeps that honest. Extend it kind by kind as the S5 soak
  reports stops.
- A `*Join`'s predicate AND both keys index the MERGED `Left ++ Right` row —
  including Semi/Anti, whose `Output()` is Left only. Checking join
  expressions against `Output()` rejects every legal right-side key.
- `walkPlanExprs` (unnest.go) misses `Aggregate.Passthrough`,
  `AggregateCall.Filter`, `WindowFunc.Args/Filter`, frame offsets (ledger row).
- `pushOneConjunct` is the FOURTH legacy family member and is NOT taught about
  the searched tag (ledger row); it descends into any `*Join`'s children.
- P5.5-e fixtures build operands with `col(i)` — UNNAMED, so name-resolution
  assertions over them pass VACUOUSLY. Use `stNamedEqui` / `stHashRoot`.
- `tryBushyDP` returns immediately when `ctx == nil` — that is how
  `enclosingtree_test.go` drives `runJoinSearchBelowPinned` with no catalog.
- **P5.6 `sizeJoinRel` open**; `GOOPG_PGSHAPED_DP` stays OFF. **P4.1 ledger row
  #3 still open** (`mergeJoinStream.bufferGroup` twin).
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; 12 new tests PASS; UNITS PASS (exit 0,
0 FAILs, `/tmp/units_p55fiib.log`); SPOT PASS (`/tmp/spot_p55fiib.log`, Q12=2
Q13=35 canonical, 28.0 s); pgbench SMOKE via the commit hook. DS05 + PLAN
re-baseline NOT applicable and not run — every new line is reachable only from
a tagged node, and only `createPlanAtSearchRoot` tags, which nothing calls from
`planSelect`; no plan can move.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
