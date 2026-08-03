(idle — nothing in flight)

M0127-P5.4b-ii-b-1 is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — `M0127-P5.4b-ii-b-2` (Memoize `get_memoize_path` joinpath.c:562 + the 03
§5.2 constructor binding contract). Bar: UNITS + SPOT + DS05.**

Carry-over facts a next loop should not re-derive:

- **P5.4b-ii-b-2 is NOT a path-generation slice.** Both halves need a built
  `*Join` NODE: `tryBuildNLI` (`nl_index_join.go:300-407`) analyses one, and
  Memoize's eligibility rides the same seam. They attach to **P5.5's
  `createPlan` arms**, so consider doing P5.4c (merge paths) or P5.5 first if
  the banner allows — check the fix_plan order before assuming.
- **Every JOIN path in the search is UNPARAMETERISED** (new invariant). The NLI
  arm admits only fully-discharged parameterisations, so `Path.Rows ==
  Rel.Rows` for join paths and only base index scans carry `RequiredOuter`.
  Do NOT relax this without P5.6's `get_parameterized_joinrel_size` —
  the star-schema case is ledgered against P5.6 and the gate is one `if` in
  `addNLIPaths`.
- **`generateNLIPath` no longer exists** (retired this loop). The single NLI
  path constructor is `addNLIPaths` (`joinpathsnli.go`). Its Q9-lesson test is
  now `TestNLIPathRuinousForLargeOuter` in `pathgen_test.go`.
- **`consideredParameterizations` now generates pairwise UNIONS**, so a
  composite index bound from two different outer rels finally gets a path.
  The ii-a ledger row that deferred this is now satisfied by the ii-b-1 row's
  `landed` column (M0119 flips `status`, not us).
- **`sizeJoinRel` is STILL the open half of the `joinRelBuilder` seam** (P5.6).
  No concrete builder, no `planSelect` call site; `GOOPG_PGSHAPED_DP` stays
  OFF. Do not write a stand-in sizer.
- **P4.1 ledger row #3 still open**: `mergeJoinStream.bufferGroup` twin.
- **NL inner work_mem bound stays OFF**; the flip needs `cost_rescan` = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`
  (21 unrelated files already show as unformatted; ignore them).
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Gate recipes** — SPOT: `scripts/tpch-spotcheck.sh`. DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p54biib1.log`);
build + `go vet ./internal/planner` + gofmt clean on every touched file;
pgbench SMOKE PASS via the commit hook. SPOT/DS05 not applicable — the arm has
no non-test caller and `GOOPG_PGSHAPED_DP` is OFF, so no plan and no row can
move.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
(fix_plan lines ~1093-1135). Nothing new to file.

In-flight: none.
