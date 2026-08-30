# PG plan parity — TODO

Companion to [DESIGN.md](DESIGN.md). Status is the truth of the tree, not an
aspiration.

**Landed and gated:** item 0 (`9db8a0970`), items 1 and 2 (`80a5e334d`).
**Implemented, measured, rejected:** item 3, the `loop_count` arm — DESIGN §9.
A and B remain blocked on missing infrastructure, recorded below so the next
pass starts from the blocker rather than re-deriving it.

**Gate requirement, learned the hard way (DESIGN §9.4): a row-count gate is not
sufficient for a plan-shape change.** Item 3 kept all 21 result sets
byte-identical and passed every unit test while making Q2 43x slower. Time the
queries whose plans changed, every time.

Legend: `[x]` done · `[~]` in progress · `[ ]` not started

---

## Survey — done

- [x] Confirm every access method exists in the executor
- [x] Confirm the planner generates index, index-only and bitmap paths (5 IOS sites)
- [x] Confirm `addBaseRelBitmapPaths` is reachable and `GOOPG_PGSHAPED_DP` is on
- [x] Confirm goopg **does** emit `Index Only Scan` (`SELECT o_orderkey FROM orders WHERE o_orderkey = 5`)
- [x] Measure both clusters' statistics state
- [x] Establish what `reltuples = 0` actually costs — **not** relation size
      (`GOOPG_RELSIZE_FALLBACK` reads the live smgr block count) and **not**
      the ability to pick an index (PG uses default selectivities); it changes
      which plans win
- [x] Confirm `ANALYZE` persists across sessions and restart (durable sidecar)
- [x] Confirm **VACUUM's relstats do NOT persist** — in-memory only
- [x] Re-sweep goopg plans after VACUUM + ANALYZE
- [x] Census by node type, split by parameterisation (this is what killed the
      "Index Scan parity" reading)
- [x] Per-query gap table
- [x] Root-cause A: a top-of-plan peephole (`planner.go:1691-1704`) that cannot
      reach scans inside join trees
- [x] Root-cause B: `RequiredOuter` hardcoded 0; `matchBitmapIndexQuals` matches
      only `col = const`; `indexScanRows` returns a hardcoded 1 for any keyed
      index scan
- [x] Confirm parameterised inner index paths are generated (`pathparamindex.go:220`)
- [ ] Re-sweep **PG** after the goopg re-sweep so both halves of the "fair"
      comparison come from one capture, on one query set (see DESIGN §3 caveat)

## Implementation — none started

### 0 — `indexScanRows`' hardcoded 1 — **DONE** (`9db8a0970`)

- [x] Replaced the flat 1 with selectivity x reltuples, using
      `varEqNonConstSelectivity` (PG `var_eq_non_const`) per equality column and
      `DEFAULT_INEQ_SEL` per range bound
- [x] A unique index fully bound by equality still returns 1; no-stats still
      returns 1
- [x] Measured: `s_nationkey = 5` goes rows=1 -> rows=400 (PG 378)
- [x] **Full row-count gate**: all 24 TPC-H result sets byte-identical (`cmp`)
      to the pre-change binary on the same cluster
- [x] tpch-spotcheck PASS, units 43/43, `-race` clean
- [x] Two tests updated: the one that pinned the old constant as "the blast
      radius" (that guard was the defect), and a pass-through test that
      hardcoded 1 while its comment said not to

### 1 — parameterised paths over FILTERED relations — **DONE** (`80a5e334d`)

- [x] `addParameterizedIndexPaths` declined every relation with local quals
      (`scanLeafIsBare`), because `NestedLoopIndexJoin.Inner` is `*IndexScan`
      and cannot carry the `*Filter` wrappers
- [x] Gave `IndexScan` a residual `Cond` — PG's `Filter:` beside `Index Cond:`
      on one node — and absorbed the quals into the probe
- [x] Absorbed, not hoisted: evaluated once per index match (the costed
      semantics), not once per probed pair (the D6.3b Q9 blowup)
- [x] Non-`LeafLocal` wrappers still declined: `Cond` runs in the scan's own
      coordinates, and a merged-row predicate would read the wrong columns
- [x] `Cond` walked by `subplan_lower_walk` alongside the probe keys
- [x] Gate: 21/21 result sets byte-identical, spotcheck PASS, units, `-race`

### 2 — `get_variable_numdistinct`'s relative form — **DONE** (`80a5e334d`)

- [x] `varEqNonConstSelectivity` read only `ColumnStats.NDistinct` (absolute),
      so a column carrying only `NDistinctFrac` was treated as UNANALYSED
- [x] That is the normal state of a restarted server (the absolute count needs
      a row count that is not restored, ledger pq-P6)
- [x] TPC-H `lineitem.l_orderkey`: 1.2 M distinct values read as 200
- [x] Implemented PG's full `get_variable_numdistinct`, including the
      asymmetric no-data branches
- [x] Measured: Q3's parameterised inner 30,006 rows -> 4.9; Q4/Q21 index scans
      30,006 -> 4 (actual ~4 lineitems per order)

### 3 — `cost_index`'s `loop_count` arm — **LANDED** (`07f4f7814`), see DESIGN §9

**Carries a known regression: Q2 1.6 s -> 84.4 s.** Revert this one commit to
undo it; nothing else depends on it.

- [x] Implemented faithfully (`index_pages_fetched` over `tuples * loop_count`,
      pro-rated; `get_loop_count` = smallest outer row count)
- [x] Measured: Q3's inner probe cost 23.9 -> 4.9 (PG: 4.01); census moved
      Nested Loop 5 -> 13, Index Scan 16 -> 24 (PG: 24), Hash Join 44 -> 35;
      all 21 result sets byte-identical
- [x] **Q2 2.0 s -> 87.3 s** — goopg evaluates SubPlan 1 as a Filter above a
      160,000-row join where PG evaluates it inside the Hash Cond over 670
      `part` rows
- [x] Landed as `07f4f7814`, with the trade stated in the commit message
- [x] DESIGN §9.3b's "the DP discards the unnester's rewrite" was WRONG — the
      search runs FIRST (planner.go:1206) and the unnester decorates its output
      (planner.go:1237). Caught by adversarial review
- [x] Two more hypotheses tested and refuted: a missing `*NestedLoopIndexJoin`
      walk arm (adding it leaves Q2 at 83.6 s), and the sublink sitting in
      `Join.Predicate` (instrumenting both arms produced no hits)
- [x] Established: the `preDPUnnested` gate prints identically with and without
      the arm while the plan differs (0 SubPlans vs 2) — so the differing factor
      is not which unnest path ran
- [x] Withdrew a second wrong claim: attributing the `preDPUnnested=true` line
      to Q2's outer level was inferred from print order, and
      `whereEligibleForPreDPUnnest` says the opposite (it returns false when a
      SubqueryExpr is in the WHERE, which Q2's is). Unreconciled with the
      separate finding that the walk's Filter/Join arms see no sublink
- [ ] **Next probe**: print the SELECT level identity alongside `preDPUnnested`
      instead of inferring it from print order — the mistake this line of
      investigation made twice. Do not assert a mechanism until demonstrated
- [ ] Then: charge a SubPlan's evaluation cost over the row count it will be
      evaluated over (PG `cost_qual_eval`), `subplan_cost.go`
- [ ] Then re-run BOTH the byte gate and a timing pass and confirm Q2 is back

### 4 — close the gate gap the review found

- [ ] Items 1-3 were gated on 21 result sets; §7 declares the bar as all 22
      queries, and item 0 compared 24 files (21 + Q15's three fragments). Re-run
      Q15's `q15_create`/`q15_viewbody`/`q15_main` against the landed build so
      every landed item meets the same bar
- [ ] The `*NestedLoopIndexJoin` arm missing from `unnestSubqueriesInPlan`'s
      walk (unnest.go:513-528) is a real traversal hole even though it does NOT
      explain Q2 (tested: adding it leaves Q2 at 83.6 s). A sublink's `*Filter`
      under an NLI is silently unreachable. Fix it on its own merits, with its
      own gate

### A — Index Only Scan (Q13, Q16, Q22) — BLOCKED on attribute-usage analysis

**Blocker found.** The existing promotion derives coverage from the TOP-LEVEL
`Project`'s target list (`planner.go:1691-1704`, matching
`Project(Filter?(IndexScan))`). Inside a join tree there is no such Project, so
the question "which columns does the rest of the plan still need from this
relation" has no answer to read. PG answers it with `baserel->attr_needed`,
built during `build_base_rel_tlists`/`deconstruct_jointree`. **That analysis
does not exist in goopg**, and it is the real prerequisite — not a relocation
of the existing check.

Diagnosis corrected twice; the current one is backed by a built-and-measured
experiment (DESIGN §4-A):

- [x] `tryPromoteIndexOnlyScan` is schema-preserving, so applying it at ANY
      `*Project` needs no ancestor renumbering and no `attr_needed`
- [x] A tree-wide pass doing exactly that was written and run over all 22
      queries. It fires **zero times** and moves the census by nothing: inside a
      join tree there is no `*Project` above the scan for it to consume. Removed
      rather than left as dead code
- [x] Checked whether the PATH form escapes the schema problem. It does not:
      `searchCtx` and `joinlistProblem` carry no output-column information (so a
      needed-columns channel must be threaded from planner.go — mechanical), but
      `createIndexScanPlan` rebuilds the leaf at full table width and an
      index-only variant cannot produce the columns the index lacks. Narrowing
      it breaks every positional ColumnRef above it
- [x] Root cause named: PG addresses columns with relation-qualified
      `Var(varno, varattno)`, so narrowing a scan is invisible above it. goopg's
      `ColumnRef.Index` is POSITIONAL into a concatenated `outer || inner` join
      schema, so narrowing any input shifts everything above. That is why this
      is local in PG and cross-cutting here
- [ ] So the work is BOTH of these, not either:
  - [ ] an index-only PATH generated and costed as index-only
        (`check_index_only` + `cost_index(indexonly=true)`), so the choice
        between a seq scan and a covering index is made on cost — this is what
        PG does and what makes Q13 pick `Index Only Scan using customer_pk`
  - [ ] or schema narrowing with ancestor remapping (a column-pruning pass),
        which is the larger and riskier of the two
- [ ] Smallest reproduction to work against:
      `SELECT o.o_orderkey FROM orders o JOIN (SELECT c_custkey FROM customer
      WHERE c_custkey < 100) c ON o.o_custkey = c.c_custkey` — the customer scan
      is index-only-eligible on every count and is read directly by the join
- [ ] Give the search rel an `allvisfrac`; note `relsize.go:424` documents its
      absence deliberately, so this is a new planner input, not a plumbing fix
- [ ] `cost_index` to charge heap fetches against it
- [ ] **Caveat**: on this cluster `relallvisible == relpages` for all eight
      relations, so an `allvisfrac` test here cannot distinguish a correct
      value from a hardcoded 1.0. Use a relation with partial VM coverage or
      the acceptance test is unfalsifiable.
- [ ] Verify Q22, Q16, Q13 flip **on cost**, no query-specific test
- [ ] Full row-count gate

### B — Bitmap (Q2, Q11, Q17, Q20, Q21 — 6 scans) — BLOCKED on a consumer

**Blocker found.** All six of PG's bitmap scans here are nested-loop inners with
an outer-var `Index Cond`, so goopg needs *parameterised* bitmap paths. But
`NestedLoopIndexJoin.Inner` is typed **`*IndexScan`**, and `pathparamindex.go`
already documents that the NLI constructor is the only consumer of a
parameterised path. A parameterised bitmap path would therefore have no
consumer, and generating one would let the DP price a plan the builder must
refuse — the exact failure that file warns about.

- [x] Confirmed gap B does NOT inherit gap A's schema problem: a BitmapHeapScan
      fetches whole heap rows, so it has the same schema as the IndexScan it
      replaces and nothing above needs renumbering. B is the better-shaped gap
- [x] Confirmed the executor blocker: `bitmapHeapScanOp` has NO `Rescan` and NO
      `BindOuter` (cf. `indexScanOp` at operators_index.go:353/:362). A bitmap
      inner must rebuild its TID bitmap per outer row; that path does not exist
- [ ] Add bitmap rescan-per-outer-row to the executor. Note it is a CHAIN, not
      one method: `bitmapHeapScanOp.Open` builds the outer bitmap producer tree
      (`buildNode(o.plan.Outer)`, a BitmapIndexScan or a BitmapAnd/BitmapOr over
      several), so `BindOuter`/`Rescan` are needed on `bitmapIndexScanOp` too —
      that is where an outer-var probe key would actually be bound — and the
      And/Or nodes must propagate them. Sizing this before starting is the
      difference between a bounded task and an open-ended one
- [ ] Generalise `NestedLoopIndexJoin.Inner` (or add a bitmap-inner join node)
- [x] Item 0 (the 1-row estimate feeding bitmap comparisons) — done
- [ ] Allow parameterised bitmap paths (`pathbitmap.go:177` `RequiredOuter: 0`)
- [ ] Let join clauses contribute index quals (`matchBitmapIndexQuals`)
- [ ] Verify at least one query flips on cost
- [ ] Full row-count gate

### C — Nested Loop vs Hash (1 vs 25)

- [ ] Depends on item 0
- [ ] Determine why the NLI arm loses 23 of 25 times: eligibility guards in
      `addParameterizedIndexPaths` vs join costing
- [ ] Largest blast radius — can move every join plan. Last.
- [ ] Full row-count gate + `scripts/tpch-spotcheck.sh` + TPC-DS SF0.5

## Cross-cutting

- [ ] No change may test a relation name, query shape, or benchmark identity
- [ ] Each landed item cites the PG function it mirrors
- [ ] units / `tpch-spotcheck` / `-race` green
- [x] Design doc agent-reviewed; findings recorded (DESIGN §8)

## Bench-harness follow-up

- [ ] `CLAUDE.md:34` is stale: it says `ANALYZE <table>` in db `tpch` errors
      with a per-DB scoping gap, but `pg_stats` is populated on :65433 today.
      Correct it, and the deferral-ledger row `bench-reorg ANALYZE-scope`.
- [ ] Decide whether the TPC-H gate should keep running S-cold. It measures a
      planner with default selectivities against a PG that has real ones. If
      that is deliberate, say so in the bench README so the next person does
      not re-derive "goopg does not use indexes" from it — as this survey
      initially did.
