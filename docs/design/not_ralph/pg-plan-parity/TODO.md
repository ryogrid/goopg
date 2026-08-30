# PG plan parity — TODO

Companion to [DESIGN.md](DESIGN.md). One line per item; status is the truth of
the tree, not an aspiration.

**Nothing in the three implementation sections below has been done.** The survey
is complete and gap A is root-caused; no planner code has changed. See
DESIGN.md §8.

Legend: `[x]` done · `[~]` in progress · `[ ]` not started

---

## Survey

- [x] Establish which access methods exist in the executor (all of them do)
- [x] Establish which paths the planner generates (index, index-only, bitmap — all)
- [x] Confirm `addBaseRelBitmapPaths` is reachable from `addBaseRelIndexPaths`
- [x] Measure goopg vs PG statistics state (`reltuples`, `relpages`, `pg_stats`, `relallvisible`)
- [x] Confirm what `reltuples=0` actually costs the planner: NOT relation size
      (`GOOPG_RELSIZE_FALLBACK` supplies it, default on) but SELECTIVITY
- [x] Confirm `ANALYZE` works, and that it persists across sessions **and** restarts
- [x] Confirm `VACUUM` populates `relallvisible`
- [x] Re-run the plan comparison with both engines VACUUMed + ANALYZEd (the fair one)
- [x] Produce the per-query gap table (DESIGN §3)

## A — Index Only Scan (Q13, Q16, Q22)

- [x] Find where an index-only path is (or is not) proposed for a covering index
      → it is NOT: `pathindexordered.go` hardcodes `index_only_scan == false`
        with the comment "goopg's search has no visibility-map model, so
        check_index_only's answer is always no". A generation gap, not costing.
- [ ] Give the search rel an `allvisfrac` (= `relallvisible / relpages`), as `plancat.c` `estimate_rel_size` does
- [ ] Port `check_index_only`'s question: are all relation columns the query needs available from the index?
- [ ] Feed `allvisfrac` (from `relallvisible / relpages`) into the index cost, as `cost_index` does
- [ ] Verify Q22 flips Index Scan → Index Only Scan **on cost**, with no query-specific test
- [ ] Verify Q16 and Q13 follow
- [ ] Row counts unchanged on all 22 queries
- [ ] `Heap Fetches` reported in EXPLAIN matches the VM state (there is an existing `explain_heap_fetches_test.go`)

## B — Bitmap scans never win (Q2, Q11, Q20, Q21)

- [x] Determine whether `buildOneBitmapPath` returns nil or the path is generated and loses
      → it IS generated and fully costed; it loses. So B is a costing/input
        problem, not a generation gap.
- [ ] Settle the leading hypothesis: `buildOneBitmapPath` matches only LOCAL
      filter conjuncts, but PG's bitmap scans here are driven by JOIN quals
      (parameterised paths). If so, B and C share one root cause.
- [ ] If generated: compare `costBitmapHeapScan` / `computeBitmapPages` against `cost_bitmap_heap_scan` / `compute_bitmap_pages`
- [ ] Check the inputs, not just the formula (`indexPages`, `totalTablePages`, `effectiveCacheSize`, `maxEntries`)
- [ ] Verify at least one of Q2/Q11/Q20/Q21 flips on cost
- [ ] Row counts unchanged on all 22 queries

## C — Nested Loop under-preferred vs Hash Join (Q3, Q19; Q12's method)

- [ ] Compare `initial_cost_nestloop` / `final_cost_nestloop` against goopg's equivalent
- [x] Check whether parameterised inner index paths are generated at all
      → yes: `addParameterizedIndexPaths` (`pathparamindex.go`) exists and runs
        from `addBaseRelIndexPaths`.
- [ ] Distinguish the two remaining candidates: its eligibility guards
      (`scanLeafFor` must yield a rebuildable leaf; the stricter NLI arm) vs the
      join cost model preferring hash
- [ ] Verify Q19 and Q3 flip on cost
- [ ] **Full row-count gate on all 22 queries** — this item can move every join plan
- [ ] Re-run `scripts/tpch-spotcheck.sh` and the TPC-DS SF0.5 regression gate

## Cross-cutting

- [ ] No change may test a relation name, query shape, or benchmark identity (DESIGN §4)
- [ ] Each landed item cites the PG function it mirrors
- [ ] `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green
- [ ] `scripts/tpch-spotcheck.sh` PASS (Q12 = 2, Q13 = 34)
- [ ] `go test -race` green
- [ ] Design doc agent-reviewed, findings recorded

## Bench-harness follow-up (not a goopg defect, but it caused the whole confusion)

- [ ] Decide whether the TPC-H gate should keep running S-cold. It measures a
      planner with no statistics, which is not what a user experiences and is
      not what the PG side is measured under. `CLAUDE.md` records the S-cold
      state as a consequence of HammerDB's ANALYZE step failing, not as a
      deliberate comparison choice.
- [ ] If it should stay S-cold, say so explicitly in the bench README so the
      next person does not re-derive "goopg does not use indexes" from it.
