# PG plan parity — TODO

Companion to [DESIGN.md](DESIGN.md). Status is the truth of the tree, not an
aspiration.

**No planner code has been changed.** The survey is complete and the three gaps
are diagnosed (three of them re-diagnosed after review — see DESIGN §8).
Everything under "Implementation" is unstarted.

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

### 0 — `indexScanRows`' hardcoded 1  ← do this first

- [ ] `cardinality.go:294-297` returns 1 for any keyed index scan. goopg
      estimates 1 row where PG estimates 378 on the same predicate.
- [ ] Replace with a selectivity-derived estimate (PG: `btcostestimate` →
      `genericcostestimate`, and `clauselist_selectivity`)
- [ ] This is a prerequisite for B and probably for C — it is listed first
      because every other item is priced against it
- [ ] **Full row-count gate**: all 22 TPC-H queries + TPC-DS SF0.5

### A — Index Only Scan (Q13, Q16, Q22)

- [ ] Move the coverage decision from the top-of-plan peephole into
      per-relation path generation, so it can reach scans inside join trees
- [ ] Give the search rel an `allvisfrac`; note `relsize.go:424` documents its
      absence deliberately, so this is a new planner input, not a plumbing fix
- [ ] `cost_index` to charge heap fetches against it
- [ ] **Caveat**: on this cluster `relallvisible == relpages` for all eight
      relations, so an `allvisfrac` test here cannot distinguish a correct
      value from a hardcoded 1.0. Use a relation with partial VM coverage or
      the acceptance test is unfalsifiable.
- [ ] Verify Q22, Q16, Q13 flip **on cost**, no query-specific test
- [ ] Full row-count gate

### B — Bitmap (Q2, Q11, Q17, Q20, Q21 — 6 scans)

- [ ] Depends on item 0
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
