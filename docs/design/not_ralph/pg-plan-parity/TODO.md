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

**Its Q2 regression is FIXED** by `f95d85ae2` (DESIGN §9.5): the decorrelation
cloner had no `*NestedLoopIndexJoin` arm and failed silently behind a swallowed
error. Q2 84.4 s -> 1.9 s, all plan-shape gains retained.

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
- [x] Level probe DONE and it inverted the guess: the `preDPUnnested=true` line
      is Q2's INNER subquery (tables [nation region supplier partsupp], 0
      sublinks). Q2's OUTER level has `preDPUnnested=false`, carries the 1
      sublink, and the post-search walk DOES run on it
- [x] Consequence: hypothesis 3 (sublink unreachable in the walk's arms) is OPEN
      again — its refutation used a probe that cannot be trusted, since the walk
      demonstrably runs on a `*Filter`-rooted tree containing a sublink. There
      are two `case *Filter:` sites in unnest.go and the probe likely went to
      the wrong one
- [x] Hypothesis 3 re-probed with an ASSERTED insertion point (there are 15
      `case *Filter:` sites in unnest.go, and the original probe's count=1
      replacement hit line 270 instead of the walk's line 432). Refuted: the
      walk DOES see a `*Filter` whose predicate contains the sublink
- [x] Measured `UNNESTWALK sublinks 1 -> 1` across `unnestSubqueriesInPlan` at
      Q2's outer level — the walk does NOT remove it, so no later pass
      re-introduces it. Candidate (3) eliminated
- [x] **RESOLVED** (`f95d85ae2`) — and it was none of the five hypotheses. The
      cloner `clonePlanReplacingOuter` had no `*NestedLoopIndexJoin` arm; the
      error is swallowed by the driver loop's `if err != nil { break }`, so the
      sublink silently stayed a SubPlan. See DESIGN §9.5-9.6
- [ ] ~~Leading hypothesis (#5)~~ — superseded: `unnestSubquery`'s substitution is by POINTER IDENTITY —
      `if c == conjunct` over `splitAnd(filter.Predicate)` — and if
      `findFilterContainingSubquery` returns a conjunct that is not one of
      `splitAnd`'s top-level elements (nested conjunct, under an OR, or the two
      functions flattening differently), every conjunct is copied UNCHANGED
      while `filter.Child = join` still installs the join. No bail, returns
      success, sublink survives. Reproduces all five observations
- [ ] Test: one print comparing `conjunct` against the elements of
      `splitAnd(filter.Predicate)` by pointer, at that line
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

### A — Index Only Scan — **Q22 DONE**; Q13/Q16 still blocked

- [x] **Q22 landed** (`20495f11e` stage 1, `4bb67d06b` stage 2, `38f37863a`
      stage 3). Index Only Scan 0 -> 1, matching PG, and Q22 got faster
      (1.4 s -> 0.9 s). Gated: 21/21 byte-identical, only q22's plan changed,
      units, TestPort_IsolationSuite, tpch-spotcheck
- [x] Stage 2's scope estimate was wrong by an order of magnitude in the
      discouraging direction: grep said 52 references across 26 files, the
      compiler said 4 production files + 8 test files. Ask the type checker,
      not grep
- [ ] Q13 and Q16 are NOT reachable this way — their index-only scans are
      HASH-JOIN inputs, which do publish their columns, so they still need the
      column-pruning pass below

### A (rest) — Index Only Scan (Q13, Q16) — BLOCKED on attribute-usage analysis

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

### B — Bitmap — **Q8 and Q17 DONE** (`b4bd87a1a`); Q2/Q11/Q20/Q21 remain

- [x] Bitmap Heap Scan 0 -> 2, and ~9x faster: Q8 10.0 s -> 1.1 s, Q17
      3.4 s -> 0.4 s. Gated: 21/21 byte-identical, units, isolation suite,
      spotcheck, timing per arm
- [x] Root cause of the two reverted attempts: `bitmapHeapScanOp` returned
      `(nil, nil)` on exhaustion instead of `(nil, EOF)` — a pre-existing
      contract bug only an EOF-respecting consumer could expose
- [x] And the census had been measuring the LABELLER: `EXPLAIN` had no
      `BitmapHeapScan` arm, so it printed the Go type and the count read zero
      even after bitmap paths were winning
- [x] The filtered-leaf restriction was lifted (`01b9a9686`, `BitmapHeapScan`
      now has a residual `Cond`). **It unlocked nothing** — zero plans changed.
      The hypothesis was wrong
- [x] **Measured why instead.** Those relations already GET bitmap paths,
      including the exact one PG uses for Q2:

      ```
      BM supplier.supplier_nation_fkidx req=0x0002 built=true cost=2588.7
      ```

      PG prices that same scan at **43.46**. Bitmap is not blocked for
      Q2/Q11/Q20/Q21 — it is MISPRICED by roughly 60x and loses to the plain
      index probe
- [x] **Both terms FIXED** (`94ef875ab`), and the first was worse than
      "missing" — it was INVERTED. goopg computed
      `sqrt*random + (1-sqrt)*seq`, which moves toward RANDOM as the touched
      fraction grows; PG's `random - (random-seq)*sqrt(ratio)`
      (costsize.c:1071) moves toward SEQUENTIAL, which is the whole reason the
      access method exists. The file's own comment described PG's behaviour
      while the expression did the opposite, so the two never contradicted each
      other in review. `compute_bitmap_pages` also gained PG's `loop_count`
      pro-rating (costsize.c:6514)
- [x] Result is PLAN PARITY, not a speed win, and it reads backwards at first:
      Bitmap Heap Scan went 2 -> 1. **PG's Q8 has ZERO bitmap nodes**; goopg had
      one there only because the inverted formula made small-fraction bitmaps
      too cheap. goopg's bitmap set on Q8/Q17 now matches PG's exactly. Timed
      against the TRUE pre-work baseline: q08 11.7 -> 11.5 (its 1.1s under the
      buggy formula was never real), q17 2.2 -> 0.4
- [x] Re-measured as instructed rather than guessing, and it found a THIRD
      costing bug: `costBitmapIndexScan` called `costIndexScan`, i.e. the whole
      scan including heap IO, so every bitmap path paid for its heap TWICE.
      PG's `cost_bitmap_tree_node` uses `indextotalcost` — the index side alone
      — because a bitmap index scan emits TIDs and never touches the heap.
      Fixed (`ab8fbc334`); the supplier probe's index side was ~874 against
      PG's 7.77
- [x] **Rival measured.** For `supplier` under `s_nationkey = n_nationkey`
      (req `0x0002`), after all three cost fixes:

      | path | rows | startup | total |
      |---|---:|---:|---:|
      | goopg bitmap | 400 | 14.35 | **66.42** |
      | goopg index probe | 400 | — | **54.73** |
      | **PG bitmap** (the one it picks) | 400 | **7.87** | **43.46** |

      The bitmap does not appear in `rel.CheapestParameterized` at all — it is
      DOMINATED by the index probe, by 21%. It was 1786 before the three fixes,
      so those closed 96% of the gap; what is left is a 1.53x overcost on the
      bitmap, and if goopg's bitmap were at PG's 43.46 it would beat the 54.73
      probe and win
- [x] **A FOURTH cost bug found, verified, and NOT landed** — because landing it
      breaks 9 queries. `costBitmapHeapScan` returned
      `startup + runCost + indexCost.Total` where `startup` IS
      `indexCost.Total`, so the index cost was counted TWICE. PG counts it once
      (`startup_cost += indexTotalCost`, then
      `total_cost = startup_cost + run_cost`). goopg's comment asserted PG "adds
      indexTotalCost again into total", a misreading that made the double count
      look oracle-backed — which is why it survived.

      Removing it does exactly what the arithmetic predicted: the `supplier`
      bitmap drops 66.42 -> 52.07, beats the 54.73 index probe, and **Bitmap
      Heap Scan jumps 1 -> 27 across 15 queries**. That is far past PG's 6, and
      9 of those queries then FAIL:

      ```
      ERROR: BitmapHeapScan outer is not a bitmap producer
      ```

      So the parameterised-bitmap construction is broken for shapes that only
      became reachable once bitmaps started winning; Q17 worked by luck. The
      cost fix is correct and is held back only by that
- [x] Diagnosed and PARTLY fixed. The `outer, _ :=` note above was wrong — that
      discards a LAYOUT, not an error. Measuring instead of reading found the
      real cause: `createBitmapIndexScanPlan` ended `return rewrap(bis)`,
      wrapping the bitmap PRODUCER in the leaf's `*Filter`, and
      `BitmapHeapScan.Outer` must be a producer. Fixed in `d24d9e6be`, which
      also makes createPlan panic on a construction error instead of returning
      a nil node, and makes the executor's assertion name the type it got
- [ ] **STILL BLOCKING the double-count fix**: with `d24d9e6be` in place the
      nine failures become three WRONG ANSWERS — q03 returns 702 rows of 11415,
      q08 and q14 return wrong values. So there is a second, deeper correctness
      bug in the parameterised bitmap inner beyond the Outer shape. Find it
      before landing the cost fix; the fix is written and verified against the
      oracle, and is held back only by this
- [x] **Probe run, and it relocated the bug.** Instrumenting `Rescan` produced
      NO output for q03 — so the failing bitmap is not a nested-loop inner at
      all. Its plan is:

      ```
      Gather  (Workers Planned: 4)
        -> Hash Join
             -> Bitmap Heap Scan on public.lineitem
                  Filter: (l_shipdate > '1995-03-15'::date)
                  -> Bitmap Index Scan on public.idx_lineitem_orderkey_fkidx
      ```

      An UNPARAMETERISED bitmap heap scan under a parallel `Gather`. So the
      ~94% row loss is in the PARALLEL bitmap path (`nextParallel`, the shared
      `o.pbm` page allocator), which — like the two bugs already found — had
      never been exercised end to end because bitmap paths never won on cost
- [x] **CONFIRMED in one run.** q03 with bitmap paths winning:
      `max_parallel_workers_per_gather` default -> **702 rows**; set to 0 ->
      **11415 rows, correct**. The parallel bitmap heap scan is broken
- [x] Tried the obvious conservative fix — stop stamping `BitmapHeapScan` as
      parallel-aware in `stampParallelScan` (parallel.go). **It does not work**:
      q03 stays at 702. `operators_gather.go` calls
      `attachParallelBitmapScan(child, o.pbm)` whenever it finds a bitmap op
      beneath a Gather, and that call is NOT gated on `BitmapHeapScan.Parallel`.
      So there are two independent places that put a bitmap scan into parallel
      mode, and the flag is only one of them
- [x] **ROOT CAUSE FOUND, and it is not the parallel handoff.**
      `bitmapHeapScanOp` **drops every row on a LOSSY page**, in BOTH paths:

      ```go
      // nextParallel
      if entry.isLossy {
              o.lossyPages++
              // … a lossy page emits nothing through this operator.
              // Full lossy-page iteration is deferred (S5.x follow-up).
              continue
      }

      // nextSerial
      if lossy {
              // We'll return one row at a time and save position.
              continue // fall through to lossy handling below
      }
      ```

      There is no lossy handling below. Both comments misdescribe what the code
      does — the parallel one claims to match the serial path, and the serial
      one describes an implementation that was never written.

      A TID bitmap goes lossy when it exceeds `work_mem`, which a scan of 6 M
      `lineitem` rows does immediately. That is the 94% row loss on q03: 702 of
      11415. It was invisible because bitmap paths never won on cost

### B — executor holes FIXED (`5341d652a`); one costing decision left

- [x] **The 94% row loss was NOT lossy pages.** Measured: `lossyPages=0`,
      `exactPages=27379 emitted=27379` — exactly ONE row per page.
      `nextParallel`'s exact-page loop returned the first surviving tuple and
      fell out of the function, so the next `Next()` claimed a new page and the
      rest of the current one was lost. Fixed with a per-page offset cursor
- [x] Lossy-page iteration implemented too (both paths emitted nothing on a
      lossy page; `nextSerial`'s comment promised handling that was never
      written). Not the cause here, but a real hole
- [x] **With those fixed AND the double-count fix applied, all five of PG's
      bitmap queries get bitmap scans: Q2, Q11, Q17, Q20, Q21 — and 21/21
      result sets stay byte-identical**
- [ ] **The remaining decision is a trade, not a bug.** That configuration
      selects **27** bitmap scans against PG's 6, and the over-selection costs
      time:

      | query | HEAD | with the cost fix |
      |---|---:|---:|
      | q21 | 10.7 s | **14.7 s** |
      | q08 | 0.5 s | **1.0 s** |
      | q03 | 7.2 s | 8.2 s |
      | q02/q05/q09/q11/q17/q19/q20 | — | unchanged |

      So the double-count fix is oracle-correct and buys all five PG bitmap
      targets, at the price of three slower queries. It is NOT landed; that is
      a judgement for a human, not an automated gate
- [x] **Over-selection root cause found — and it is not about bitmap.**
      goopg's `pg_stats.correlation` is EMPTY for every column where PG has real
      values (`l_orderkey` 0.206, `s_nationkey` 0.031, …). `cost_index`
      interpolates between its two I/O bounds by `correlation²`, so with zero
      correlation **every index scan in goopg is priced at `max_IO_cost`** — the
      fully-uncorrelated worst case. Index probes are systematically overpriced,
      so bitmap (and seq) rivals win too often. See DESIGN §13
- [x] **Settled by measurement.** The `pg_stats` reading was a red herring —
      that view NULLs correlation by design (`pgstats_e2e_test.go`). Printing
      what the planner reads gives `corr=0` on the restarted server, but an
      IN-SESSION `ANALYZE` restores it: `nation_pk` 1,
      `nation_regionkey_fkidx` 0.355, `supplier_nation_fkidx` -0.017. So
      correlation is computed and does reach `indexCorrelationFor` — it is
      **not persisted or not restored**
- [x] **FIXED** (`b48008455`). The persisted `pg_statistic` row already had
      `stakind3`/`stanumbers3` columns written as 0/NULL; correlation now rides
      there as PG's `STATISTIC_KIND_CORRELATION`, with the decoder in
      `catalog/codec.go` and the reload in `initdb/open.go`. Verified by
      ANALYZE -> restart -> print
- [x] **Bitmap Heap Scan 1 -> 6, PG's count**, covering four of PG's five
      (Q11, Q17, Q20, Q21). 21/21 byte-identical, every query same or FASTER,
      Q3 7.2 s -> 3.6 s
- [x] **The §11.4 trade is retired.** The double-count cost fix appeared to buy
      the bitmap targets at q21 +37% / q08 2x; those numbers were measured
      against index scans priced at `max_IO_cost`. With correlation restored
      bitmap reaches PG's count with NO cost fix and no slowdown, so the trade
      was an artefact of the missing statistic
- [ ] Remaining on bitmap: **Q2** is the one of PG's five still not covered, and
      goopg picks bitmap on **Q8** where PG does not. Re-measure that pair now
      that the statistic is real — every earlier comparison in this file is
      stale
- [x] **Re-evaluated the double-count fix on top of the restored correlation,
      and it should stay unlanded — for a NEW reason.** It now covers all five
      of PG's bitmap queries including Q2, but selects **24** bitmap scans
      against PG's 6. Without it goopg selects **6**, matching PG's count, and
      covers four of the five. Six-matching-six is closer to parity than
      twenty-four-covering-five
- [ ] **And that is a finding, not just a preference.** Removing a genuine
      double-charge makes goopg's aggregate behaviour diverge FURTHER from PG.
      That means another term is UNDER-charging bitmap, and the double charge
      was accidentally compensating for it. Find that term before removing the
      double charge — the two errors currently cancel, which is why the census
      count looks right today for the wrong reason.
- [ ] **The under-charge is IDENTIFIED.** PG:

      ```c
      cpu_per_tuple = cpu_tuple_cost + qpqual_cost.per_tuple;
      cpu_run_cost  = cpu_per_tuple * tuples_fetched;
      ```

      goopg's `costBitmapHeapScan` charges `cpuTupleCost * tuplesFetched` and
      omits `qpqual_cost.per_tuple` entirely — the per-tuple cost of the
      recheck quals and the scan's own `Cond`. That undercharges bitmap, which
      is exactly the direction needed to explain the cancellation.
      `qualEvalCost(cp, nQuals, rows)` already exists; the work is threading a
      qual count into `costBitmapHeapScan`, which does not receive one today
- [x] **TRIED, and it is NOT the counterweight — negative result.** The qpqual
      term was implemented and landed together with the double-count removal:
      the census stayed at **24** bitmap scans, unchanged. `qualEvalCost` is
      `cpu_operator_cost x numQuals x tuples` and the recheck lists hold one or
      two clauses, so against page costs in the hundreds it is noise. Both
      changes were reverted; the branch keeps the double charge and its
      matching-6 census. Look for a term of the same ORDER as `indexTotalCost`,
      not for more per-tuple CPU
- [x] Two more candidates ELIMINATED by reading, not guessing:
  - [x] "goopg does not charge the bitmap inner's STARTUP per rescan, and a
        bitmap's cost is startup-dominated" — refuted: `nestloopCost`
        (cost_funcs.go:384) charges `outerRows * innerRescanTotal`, and that
        total includes startup
  - [x] `qpqual_cost.per_tuple` (§13.4) — real but numerically negligible
- [x] Measured PG directly on the one mismatched query. PG's Q8 uses a
      PARAMETERISED `Index Scan using lineitem_part_supp_fkidx on lineitem`
      (`cost=0.43..N`) exactly where goopg picks a bitmap. So the divergence is
      a bitmap-vs-parameterised-index-probe comparison inside a nested loop, on
      one specific index — which is the narrowest form the question has taken
- [ ] **Next**: print both candidate paths' Cost{Startup,Total} for `lineitem`
      under Q8's parameterisation and compare against PG's `0.43..N`. Both
      numbers, not one — every previous round guessed at a single term and was
      wrong four times
- [ ] ~~Land the two together, never separately~~ — each alone makes the census
      worse — and gate on the census SHAPE rather than its count: goopg should
      end at PG's 6 scans AND on PG's five queries, where today it has 6 scans
      but on q08 instead of q02
- [ ] Worth a regression test in its own right, independent of plan parity: a
      bitmap scan forced over a relation big enough to go lossy, compared
      against the seq-scan result. That test would have caught this years
      earlier than a plan-shape census did
- [ ] **The remaining calibration question, after that.** Startup 14.35 vs PG's
      7.87 is roughly `indexProbeCostMultiplier`, which `btreeIndexAMCost`
      applies at costindex.go:189. That knob exists because goopg's index access
      materialises TIDs eagerly — which a bitmap index scan also does, so
      applying it is arguable. But BOTH rivals are inflated by it, so the
      multiplier alone does not explain the ORDERING flip: PG prefers bitmap
      over its own index probe and goopg does not. The next question is what
      makes goopg's index probe relatively cheaper than PG's, and it is a
      question about `costIndexScan`, not about the bitmap functions

### B (original notes) — Bitmap (Q2, Q11, Q17, Q20, Q21 — 6 scans) — BLOCKED on a consumer

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
- [x] Scoped the executor side: the join driver is ALREADY generic — it builds
      the inner through the `nliInner` interface (operators_nljoin.go:101),
      satisfied today by `indexScanOp` and `memoizeOp`. The typing blocker is
      the PLAN node's `Inner *IndexScan` field alone
- [x] What is missing is the interface's rescan half on the candidate operators:
      `indexOnlyScanOp` and `bitmapHeapScanOp` implement neither `BindOuter` nor
      `Rescan`. ONE shape of work serves BOTH gaps — gap A's Q22 index-only scan
      is a nested-loop ANTI-join inner (its columns are never read, so narrowing
      is invisible there) and all six of gap B's bitmap scans are NL inners.
      **This is the highest-leverage next step in the whole document**
- [x] SIZED stage 1. `indexOnlyScanOp.Open` is **262 lines** covering relation +
      index locks, SERIALIZABLE predicate locking, hash-index bucket-grain
      locking, key resolution and row materialisation
- [x] **Correction to a first, over-cautious reading of that size.** There is a
      DIRECT PRECEDENT for the split, in the sibling operator, and it settles
      the question the size raises. `indexScanOp` already has exactly this
      shape (operators_index.go:274/292/353/362):

      | half | contents |
      |---|---|
      | `openPrep` (one-time) | `acquireRelLock`, `acquireScanReadLockTxn`, `acquireScanIndexReadLocksTxn`, `openIndexBTree` |
      | `Rescan` (per outer row) | reset tids/idx, bind the outer slot, resolve Key/Keys/LowKey/HighKey, decide the gap-lock grain, run the scan |
      | `Open` | reopen -> `Rescan`; else `openPrep` + `Rescan` |

      So the locks and the relation-grain SIREAD belong to the ONE-TIME half and
      the per-rescan half is the probe. The allocation is not a judgement call to
      be re-derived — it is mirrored. That makes stage 1 mechanical rather than
      isolation-risky, though the isolation suite still belongs in its gate
      because `indexOnlyScanOp` has one thing `indexScanOp` does not: it
      materialises `o.rows` in `Open`, so `Rescan` must re-materialise
- [x] **PROVED the safety of the Q22 case**, which was the open question that
      made gap A look risky everywhere. For a semi/anti NLI the join's schema is
      the OUTER's alone — `nl_index_join.go:677-684`:

      ```go
      if j.Type == JoinTypeSemi || j.Type == JoinTypeAnti {
              joinedSchema = append(Schema(nil), outerNode.Output()...)
      } else { /* outer ++ inner */ }
      ```

      with the comment "the inner side is consumed only for matching, never
      projected". So narrowing a semi/anti NLI's inner is INVISIBLE above it —
      the positional-ColumnRef problem that blocks Q13 and Q16 provably does not
      arise for Q22. No column-pruning pass is needed for this one query
- [x] Confirmed goopg's Q22 plan is ALREADY structurally identical to PG's —
      same `Nested Loop Anti Join`, same `Index Cond: (o_custkey = c_custkey)`.
      The single difference is the node type: `Index Scan` vs PG's
      `Index Only Scan`. Q22 is therefore the cheapest census win available
- [x] **A cheaper shortcut for Q22 exists and is REJECTED — record so it is not
      rediscovered and taken.** For semi/anti the NLI never emits the inner row
      (`operators_nljoin.go:239-250`: Semi returns `o.outerOnly`, Anti marks and
      skips), so the inner is used only to test existence. A flag —
      `IndexScan.IndexOnly` — plus an executor branch skipping the heap fetch
      when `ctx.VM.AllVisible(block)` would produce the same execution and the
      same `Index Only Scan` EXPLAIN line for Q22, at perhaps a fifth of the
      cost of the three-stage plan. It is even correct: ALL_VISIBLE is exactly
      the guarantee that lets an index entry stand in for a visible tuple, and
      `indexOnlyScanOp` already relies on it.

      It is rejected because it is a SECOND representation of index-only
      scanning alongside the existing `IndexOnlyScan` node — the
      two-ways-to-say-one-thing shape this repo is repeatedly bitten by
      (see the `variableNumDistinct` divergence, `1081e5b84`, found in this same
      session) — and because PG's Q22 uses a real Index Only Scan node, so the
      faithful structural change is widening `Inner`, not adding a flag beside
      it. `CLAUDE.md`: "Vanilla-PG compatibility is absolute."

      If a future maintainer decides the trade is worth it, the guards are:
      set the flag only when the NLI's `Predicate` references no inner column
      (walk `ColumnRefs` for `Index >= outerWidth`) and the inner's `Cond` is
      nil, or the skipped heap fetch drops a predicate
- [x] Stage 1 fully mapped against the source, so it can be written without
      re-deriving it. `indexOnlyScanOp.Open` splits as:

      | half | contents |
      |---|---|
      | `openPrep` | handles check, privilege check, `o.ctx`, `arrayStyle`, `heapRel`, the three lock acquisitions, `isHashIdx`, the non-hash `ssiRecordRelationRead`, `openIndexBTree` |
      | `Rescan` | reset `rows`/`idx`/`hashProbeFingerprint`/`touchedBlocks`, bind the outer slot, key resolution, the hash-bucket SIREAD, `keyDecodable`, `scanFn`, `RangeScan`, the `Backward` start index, `pruneTouchedTempPages` |
      | `Open` | `openPrep` + `Rescan(nil, 0)` |

      New fields needed: `tree`, `heapRel`, `isHashIdx`, `outerSlot`,
      `outerWidth`.
- [x] **One non-obvious prerequisite inside stage 1**: the three probe helpers
      are constant-only today. `lookupKey` calls `evalExpr(o.plan.Key, nil,
      o.ctx)` — a **nil row** (operators_indexonly.go:814), and `lookupKeys` /
      `lookupRangeBounds` match. An NLI inner's probe key references OUTER
      columns, so all three must move to `evalExprSlot(expr, o.outerSlot,
      o.ctx)`, mirroring `indexScanOp`. Miss this and the scan silently probes
      with a nil row rather than failing
- [x] **Stage 2 measured, and it is the expensive one.** Widening
      `NestedLoopIndexJoin.Inner` off `*IndexScan` touches **52 references
      across 26 non-test files**, plus **11 test files**
      (`grep -rn "\.Inner\b"` in internal/optimizer + internal/executor,
      excluding the `inner*` false positives). Every one either needs a type
      switch or an assertion that the inner is still an index scan — the
      remap passes, the EXPLAIN arms, the FOR UPDATE TID-provider walks
      (`nliInnerIndexScan`), the subplan walkers, and the createplan
      assertions. That is the number behind "multi-day", and it is why stage 1
      alone — which is genuinely mechanical — buys nothing on its own
- [x] **MEASURED, not estimated** (after grep oversized stage 2 by 10x). The
      bitmap executor half is SMALL: `bitmapIndexScanOp.Open` is 36 lines and is
      already effectively an `openPrep` — it does locks + `openIndexBTree` and
      nothing else, because the actual scan lives in `buildBitmap`, which
      `bitmapHeapScanOp` calls through the `bitmapProducer` interface.
      `bitmapHeapScanOp.Open` is 53 lines. So the rescan split is roughly
      "`buildBitmap` again + reset the iteration", not a 262-line dissection
      like `indexOnlyScanOp` was
- [x] And stage 2 already removed the OTHER blocker: `NestedLoopIndexJoin.Inner`
      is now `Node`, so a `*BitmapHeapScan` may legally sit there. What is still
      needed on top: an arm in `nliInnerProbe` (or a decision that a bitmap
      inner is not a "probe" in that sense), an arm at the executor build site,
      and the planner half below
- [ ] Add bitmap rescan-per-outer-row to the executor. Note it is a CHAIN, not
      one method: `bitmapHeapScanOp.Open` builds the outer bitmap producer tree
      (`buildNode(o.plan.Outer)`, a BitmapIndexScan or a BitmapAnd/BitmapOr over
      several), so `BindOuter`/`Rescan` are needed on `bitmapIndexScanOp` too —
      that is where an outer-var probe key would actually be bound — and the
      And/Or nodes must propagate them. Sizing this before starting is the
      difference between a bounded task and an open-ended one
- [ ] Generalise `NestedLoopIndexJoin.Inner` (or add a bitmap-inner join node)
- [x] Item 0 (the 1-row estimate feeding bitmap comparisons) — done
- [x] **ATTEMPTED and reverted — the planner half works, the EXECUTOR half of
      the join does not.** A parameterised bitmap producer
      (`addParameterizedBitmapPaths`, mirroring `addParameterizedIndexPaths`)
      plus a `createNestLoopBitmapJoinPlan` consumer plus the executor build arm
      were written and did generate competing paths: Q8 and Q17's plans changed,
      Nested Loop 13 -> 15, Hash Join 36 -> 34. But the queries CRASHED the
      backend:

      ```
      panic: runtime error: index out of range [7] with length 0
        executor.(*MaterializedSlot).Get            slot.go:87
        executor.(*VirtualSlot).Get                 slot.go:154
        executor.evalExprSlot                       expr.go:451
        executor.(*nestedLoopIndexJoinOp).evalPredicateSlot  operators_nljoin.go:316
        executor.(*nestedLoopIndexJoinOp).Next      operators_nljoin.go:212
      ```

      The NLI's residual predicate reads merged column 7 while the INNER half of
      the merged slot has length 0 — the bitmap heap scan's row is not composing
      into `NestedLoopIndexJoin`'s `VirtualSlot` the way an index scan's does.
      Bitmap paths never actually WON on cost either (Bitmap Heap Scan stayed 0);
      they only perturbed the DP enough to change two plans.

      So the next attempt should start at the JOIN driver, not the planner.
      Narrowed further before reverting:

      - **The bitmap emit path is NOT the fault.** `fetchExact` ends
        `o.slot = MaterializedSlot{schema: o.plan.Output(), row: row}` with a
        cloned, correctly-sized row, so the inner does hand back a populated
        slot.
      - Which leaves the ROUTING. `nestedLoopIndexJoinOp` composes outer+inner
        into a persistent `VirtualSlot` that dispatches `Get(i)` to `outerMS` or
        `innerMS` by width. A merged column that routes to the WRONG half
        explains "index 7, length 0" exactly — it reached a slot whose row was
        never filled for that side. The suspect is the `in.merged` / `in.lay`
        that `createNestLoopBitmapJoinPlan` got from `joinInputsFor`
        disagreeing with `BitmapHeapScan.Output()`, which is the leaf's FULL
        table schema.
      - **`slotRow` is not the fault either.** It is `slot.Row()`, and the
        bitmap returns a `*MaterializedSlot` exactly as `indexScanOp` does.
      - The assertion below is aimed correctly, and reading
        `nestedLoopIndexJoinOp.Open` says why:

        ```go
        o.outerWidth = len(o.outer.Schema())
        o.innerWidth = len(o.inner.Schema())
        cols := make([]virtualCol, 0, o.outerWidth+o.innerWidth)  // one per side
        o.virtualOut = NewVirtualSlot(o.Schema(), []TupleSlot{o.outerMS, o.innerMS}, cols)
        ```

        `cols` is sized from the two OPERATOR schemas while `o.Schema()` is the
        PLANNER's merged schema. Those are independent, and nothing checks they
        agree — so a consumer that builds `in.merged` inconsistently with
        `BitmapHeapScan.Output()` produces exactly this class of crash and no
        earlier complaint.
      - **DONE, and it is now permanent** (`6d6249c60`): the check lives in
        `nestedLoopIndexJoinOp.Open` for ALL inner kinds, so retrying the bitmap
        work will produce a NAMED error identifying the width mismatch instead
        of the deep `index out of range` panic. Writing it also surfaced that
        the invariant is CONDITIONAL — a semi/anti join publishes the outer's
        schema alone, so `cols` is deliberately longer there. The first draft
        asserted `outer+inner` unconditionally and broke four existing
        semi/anti tests, which is the fastest possible confirmation that the
        rule needed both cases.
      - **HYPOTHESIS TESTED AND DISPROVED (2026-08-31).** The bitmap producer
        and consumer were rewritten with `6d6249c60`'s width check live, and the
        check **did not fire** — the planner's merged schema and the children's
        operator widths DO agree. The same
        `index out of range [7] with length 0` panic occurs, in
        `evalPredicateSlot` on the SERIAL path (Q17's stack is
        `aggregateOp.Open -> filterOp -> projectOp -> nliOp.Next`, no Gather),
        so it is not the parallel hash-build path either.

        What is now eliminated: the bitmap emit path (`fetchExact` sets a
        cloned, correctly-sized row), `slotRow` (it is `slot.Row()`, and the
        bitmap returns a `*MaterializedSlot` exactly as `indexScanOp` does),
        the merged-schema width mismatch, and the parallel path.

        What is left: `VirtualSlot.Get` dispatched to a `MaterializedSlot` whose
        `row` was nil at that moment, with widths consistent. So the next probe
        should print, at the `evalPredicateSlot` call site,
        `len(o.outerMS.row)`, `len(o.innerMS.row)`, `o.outerWidth` and the
        predicate's ColumnRef indexes — i.e. find WHICH side was empty and WHEN,
        rather than reasoning about which side ought to be.
      - Remaining: with that check in place, and write the unit test to
        drive a bitmap inner through `nestedLoopIndexJoinOp` directly so the
        failure surfaces without a 6M-row query. Consider making that assertion
        permanent in `Open` for ALL inner kinds — the index arm has simply never
        violated it.
- [ ] Allow parameterised bitmap paths (`pathbitmap.go:177` `RequiredOuter: 0`).
      `matchBitmapIndexQuals` (pathbitmap.go:234) matches only `col = const` via
      `normalizeColumnConst`; a parameterised sibling would mirror
      `addParameterizedIndexPaths`. THIS is the remaining bulk — the three
      bitmap planner files total 871 lines, and this adds a parameterised path
      producer plus its createplan consumer. Size it by building it, not by
      grepping it
- [ ] Let join clauses contribute index quals (`matchBitmapIndexQuals`)
- [ ] Verify at least one query flips on cost
- [ ] Full row-count gate

### C — Nested Loop vs Hash (1 vs 25)

- [ ] Depends on item 0
- [ ] Determine why the NLI arm loses 23 of 25 times: eligibility guards in
      `addParameterizedIndexPaths` vs join costing
- [ ] Largest blast radius — can move every join plan. Last.
- [ ] Full row-count gate + `scripts/tpch-spotcheck.sh` + TPC-DS SF0.5

## D — Prefix probes (PG `amoptionalkey`) — LANDED

- [x] Root-cause Q8's bitmap: the index producer required TOTAL index coverage
      while the bitmap producer accepted a prefix, so at `req={part}` the bitmap
      was the ONLY candidate. Never a cost difference — see DESIGN §14
- [x] Executor: `lookupKeys` accepts a short key list; pad `hiBytes` via
      `compositeUpperBound` (the single-`Key` branch has done this since
      M0053-0001, so the "executor cannot express it" comment was false)
- [x] `indexPathClauses` truncates at the first unbound column; still declines
      when the LEADING column is unbound
- [x] Rename `pickIndexCoveringAllLeadingColumns` → `...LeadingPrefix`; rank on
      bound-column count, not index width
- [x] Split probe selectivity (prefix) from `ppi_rows` (all movable clauses);
      gate the `idx.Unique` one-row short-circuit on FULL binding
- [x] Gates: 21/21 byte-identical, units PASS, `tpch-spotcheck` PASS, Q8/Q17
      timings unchanged, exactly 2 of 21 plans changed

### D-followup — Q17's SubPlan scan (NOT the node this change touched)

- [x] Established by node-by-node comparison (DESIGN §14.3): the bitmap this
      change removed sat at Q17's JOIN INPUT, where PG has no bitmap — PG
      hash-joins there. The census drop 6 → 4 is therefore not a parity loss;
      the old count matched PG's 6 by coincidence, not by position.
- [x] Two strict improvements at that node: `p_partkey = l_partkey` moved from a
      post-join `Filter:` to an `Index Cond:`, and the estimate moved from
      rows=1 to rows=30 (PG independently says 31).
- [ ] The REAL Q17 mismatch is `SubPlan 1`: PG uses a Bitmap Heap Scan
      (`Recheck Cond: (l_partkey = part.p_partkey)`), goopg an Index Scan. This
      predates the prefix arm and is unchanged by it.
- [ ] PG separates the two by **1.0%** there (bitmap 4.67..127.62, index
      0.43..128.97 with `enable_bitmapscan=off`). goopg's costs for that node
      have NOT been measured.
- [ ] **Next**: instrument the SubPlan node specifically. Do NOT reuse §14's
      `PAIR` numbers — those are the DP's parameterised candidates at the join
      input, and a correlated SubPlan's scan is parameterised from an outer
      query level, a different seam. Confirm which seam costs it before
      comparing anything.
- [ ] This supersedes DESIGN §13.4's open question. Do NOT resume hunting "a
      term of the same order as `indexTotalCost`": that framing came from
      assuming both candidates existed when one did not.

### D-followup-2 — stop reporting parity as a node-type census

- [ ] The census (Bitmap 6 vs 6, Index Scan 32 vs 24, …) cannot distinguish
      "same node type, same position" from "same node type, wrong position".
      Q17 had both engines at 1 bitmap while the bitmaps were on DIFFERENT
      scans. Replace the count with a per-query, per-position diff before
      quoting parity numbers again.

## E — pre-existing TPC-DS findings surfaced by this gate run (NOT caused by D)

Each was A/B'd against this branch's HEAD before the prefix arm
(`1c4b7a5e0`) and reproduces identically there, so none is attributable to
section D. Recorded because the gate run is where they became visible.

- [x] **Q72 regressed since 2026-08-24.** `sweep-20260824-003146` has
      `Q72 PASS 73s`; today it TIMEOUTs at >400s on BOTH `1c4b7a5e0` and the
      prefix arm, so it is not section D. **Bisected** (9 steps, 425-commit
      range, verdict = Q72 under a 200s timeout):

          first bad commit = ab8fbc334
          "optimizer(costbitmap): a bitmap INDEX scan was charged for
           heap fetches it never performs"

      That commit is CORRECT — `cost_bitmap_tree_node` (costsize.c:1150) costs a
      BitmapIndexScan from `indextotalcost` alone, and goopg was calling the
      whole `costIndexScan` including heap IO. Its own message records "HONEST
      RESULT: no plan changed", which was true **of TPC-H**; TPC-DS was not run
      at that commit. Making bitmap index scans ~100x cheaper let parameterised
      bitmap paths start winning, and Q72 is where one wins badly.

- [ ] **Q72's actual defect: the two fact tables swap roles.** Plans captured
      either side of `ab8fbc334`:

      | | drives from | probes |
      |---|---|---|
      | **PG 18.3** | `Parallel Seq Scan on catalog_sales` (232k rows/worker) | `Index Scan using inventory_pkey`, `Index Cond: ((inv_date_sk = d2.d_date_sk) AND (inv_item_sk = catalog_sales.cs_item_sk))` |
      | **goopg (good, pre-ab8fbc334)** | hash-join tree, `Seq Scan on catalog_sales rows=720657` | — |
      | **goopg (bad, current)** | `Seq Scan on inventory rows=4710000` | `Bitmap Heap Scan on catalog_sales` via `catalog_sales_pkey`, **rows=1** |

      goopg probes `catalog_sales` and scans `inventory`; PG does the exact
      inverse. The `rows=1` estimate on the parameterised bitmap is the engine
      of it: it is arithmetically defensible for a fully-bound UNIQUE pkey
      probe, but it propagates upward so every join above estimates 1 row, which
      makes a chain of nested loops look free — and each of those rescans a
      hash join whose probe side is the 4.7M-row `inventory` seq scan.

- [ ] **Next**: goopg needs the path PG uses — a parameterised probe on
      `inventory_pkey (inv_date_sk, inv_item_sk)` bound from TWO different rels
      (`d2` and `catalog_sales`). Section D's prefix arm is a prerequisite but
      is NOT sufficient on its own (Q72 still times out with it). Check first
      whether the DP ever *generates* that path — per
      DESIGN §14.4, verify generation before theorising about cost.
- [ ] Do NOT fix this by reverting `ab8fbc334`: it would restore Q72 by
      reinstating a cost the oracle names as wrong, and the bitmap wins that
      followed it (Q11/Q20/Q21) depend on it.
- [ ] **Q47 and Q69 time out** (>400s) on both binaries.
- [ ] **Q31, Q64, Q71 report ERROR inside a full sweep but PASS standalone**
      (19 / 2 / 580 rows). Same shape as the known "isolation wedges in FULL
      testport but passes standalone" trap: judge these by a standalone re-run
      before treating them as engine bugs.
- [ ] The 2026-08-31 05:19 sweep aborted at Q71 with
      `goopg (sf05) did not become ready in 180s` after a clean stop, with no
      leaked scopes and no orphan processes afterwards — a transient restart
      failure under load, not a wedge. `Q72..Q99` was then run as a subset
      probe: PASS=26 MISMATCH=0 ERROR=0, `verdict-changes=none`.

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
