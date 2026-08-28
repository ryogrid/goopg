# 01 — Upstream PG 18.3 behavior (with citations)

All paths under `./postgres/src/backend/` unless noted.

## 1. Heap scan and the visibility map

- `access/heap/vacuumlazy.c:lazy_scan_heap` walks blocks via `heap_vac_scan_next_block`
  / `find_next_unskippable_block`, which consult
  `visibilitymap_get_status`; a contiguous run of ≥ `SKIP_PAGES_THRESHOLD`
  (= 32, access/heap/vacuumlazy.c:209) all-visible blocks is jumped without pinning
  (access/heap/vacuumlazy.c:1568–1656, 1669+).
- Skipping is disabled when `aggressive` (cutoff-driven escalation,
  access/heap/vacuumlazy.c:777, 793–799) or `DISABLE_PAGE_SKIPPING`.
- A non-aggressive pass that skipped any all-visible **not-all-frozen**
  page must NOT advance relfrozenxid (`skippedallvis` guard,
  access/heap/vacuumlazy.c:876–893, comment :1721).
- VM bits are set for pages found all-visible; cleared by DML
  (heapam.c visibilitymap_clear sites :2139/:2507/:3054/:3874/:4104).

## 2. Freeze semantics

- Cutoffs (vacuum.c:1195–1229):
  - `FreezeLimit = nextXID − min(vacuum_freeze_min_age, max_freeze_max_age/2)`
    , clamped ≤ OldestXmin;
  - `MultiXactCutoff = nextMXID − vacuum_multixact_freeze_min_age`;
  - `VACUUM (FREEZE)` ⇒ all four ages = 0 (vacuum.c:404–417), which also
    makes the cutoffs aggressive.
- Aggressive determination (vacuum.c:1244–1273): true when
  `relfrozenxid ≤ nextXID − vacuum_freeze_table_age` (default 150M) or the
  multixact analog holds. Aggressive forces a full scan and freezing of
  everything ≥ FreezeLimit.
- Anti-wraparound autovacuum is forced per table when
  `relfrozenxid < recentXid − autovacuum_freeze_max_age` (default 200M;
  autovacuum.c:3056–3062, reloption clamp :3049–3052); launcher-level DB
  selection uses `xidForceLimit = recentXid − autovacuum_freeze_max_age`
  (autovacuum.c:1132). Wraparound workers drop SKIP_LOCKED
  (autovacuum.c:2844) and bypass `autovacuum_enabled=off`
  (:3082–3087 "But ignore if at risk").

## 3. Autovacuum triggers

`relation_needs_vacanalyze` (autovacuum.c:~3001–3157):

```
vacthresh = vac_base_thresh + vac_scale_factor * reltuples        (:3122)
anlthresh = anl_base_thresh + anl_scale_factor * reltuples        (:3128)
dovacuum  = dead_tuples > vacthresh                               (:3109,:3155)
doanalyze = mod_since_analyze > anlthresh                         (:3111,:3157)
insert-only extra: ins_since_vacuum > ins_base + ins_scale*reltuples*pcnt_unfrozen
                                                              (:3110,:3118–3133)
```

Defaults: base 50 / scale 0.2 (vacuum), base 50 / scale 0.1 (analyze),
insert 1000 / 0.2. Counters come from the stats collector entries
(`tabentry->dead_tuples / ins_since_vacuum / mod_since_analyze`,
autovacuum.c:3109–3111) and are reset by vacuum/analyze completion
(`pgstat_relation.c:236–248`).

Scheduling: launcher wakes per naptime and spreads databases across it
(`millis_increment = 1000.0*autovacuum_naptime/nelems`, autovacuum.c:1039);
workers are separate processes capped by `autovacuum_max_workers`
(default 3).

## 4. Cost-based throttling

Per-page costs `page_hit`(1)/`page_miss`(10)/`page_dirty`(20) accumulate in
`VacuumCostBalance`; when over `vacuum_cost_limit` (200) sleep
`vacuum_cost_delay` ms, capped at 4× delay per sleep (vacuum.c:2472–2490);
autovacuum overrides via `autovacuum_vacuum_cost_delay/limit`
(guc_tables.c:2683, 4082). GUC anchors: guc_tables.c:2643–2663, 2794+.

## 5. ANALYZE

- Sample size `targrows = 300 * statistics_target` rows minimum
  (analyze.c:493–521, multiplier at :1940–1954; default_statistics_target
  = 100, analyze.c:71).
- Two-stage acquisition: random blocks then Vitter reservoir
  (analyze.c:1166–1185).
- Per-column scalar stats: n_distinct estimator, MCV (margin 1.25),
  equi-depth histogram, correlation, null fraction (compute_scalar_stats).
- Inherited (partitioned parent) stats via acquire_inherited_sample_rows.

## 6. Misc

- `vac_update_relstats` publishes relpages/reltuples/all-visible
  (vacuum.c:1402, 1442).
- Database-wide VACUUM refreshes datfrozenxid unconditionally
  (vacuum.c:723, defn :1624).
- Empty-tail truncation honors `vacuum_truncate` + reloption +
  statement param (access/heap/vacuumlazy.c:862, 3197–3230).
- `VACUUM (FULL)` rewrites the heap (cluster-style); CLUSTER physically
  orders by index (cluster.c).
