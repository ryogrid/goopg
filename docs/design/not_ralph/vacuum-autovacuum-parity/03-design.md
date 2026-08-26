# 03 — Detailed design

## F1. VM-based page skipping in vacuumCore (+skip guard)

`commands/vacuum/vacuum.go` gains an `Aggressive bool` option. In the block
loop, before pinning:

```go
if !opts.Aggressive && opts.VM != nil && opts.VM.AllVisible(rel, blk) {
    stats.SkippedAllVisible++
    continue
}
```

- Mirrors upstream skipping of all-visible pages; the ≥32 run-jump
  optimization is intentionally omitted (goopg's VisibilityMap is an
  in-memory structure — per-block tests are cheap; upstream's run-jump
  amortizes VM buffer reads).
- **Two-bit VM**: the map gains an ALL_FROZEN bit beside ALL_VISIBLE
  (upstream visibilitymap.c has exactly these two bits). ALL_FROZEN is set
  during a pass when a page's remaining live tuples are entirely below
  FreezeLimit (MinUnfrozenXID ≥ FreezeBelow, or zero live), cleared by DML
  wherever ClearBlock already fires. Rationale: the skip-guard must only
  stall advancement for all-visible-but-NOT-all-frozen skips
  (`vacuumlazy.c:1607–1630`); with one bit, post-freeze steady tables would
  never advance RelFrozenXID again and spuriously trip cluster-wide
  anti-wraparound later (review finding M3).
- **Skip guard**: skip counts split into `SkippedAllVisible` (not
  all-frozen) vs `SkippedAllFrozen`; callers advance RelFrozenXID only when
  `SkippedAllVisible == 0 || Aggressive`.
- **Last-block rule**: the FINAL block is never skipped
  (`access/heap/vacuumlazy.c:1726–1729` always scans it) — required anyway
  for correct tail truncation (F7).
- Aggressive passes ignore VM entirely (full scan); statement
  `(DISABLE_PAGE_SKIPPING)` maps to Aggressive too.

## F2. Aggressive determination + FREEZE option

New helper in `operators_vacuum.go`:

```
freezeTableAge = min(GUC vacuum_freeze_table_age, 0.95*autovacuum_freeze_max_age)
                // upstream caps table-age at 0.95*max_age (vacuum.c:1246)
aggressive = stmt.Freeze                       // age-0 cutoffs => aggressive
          || vs.DisablePageSkipping            // explicit full-scan request
          || vs.Full                           // rewrite path scans everything
          || relfrozenxidAge(nextXID, tbl.RelFrozenXID) >= freezeTableAge
// relfrozenxidAge treats InvalidTransactionID/0 as INFINITE age:
// PG creates heaps with relfrozenxid = InvalidTransactionId (heap.c:325)
// and the unsigned compare makes the first VACUUM of a never-vacuumed
// table AGGRESSIVE (vacuum.c:1247–1249). goopg must match.
```

`stmt.Freeze` now executes (was parsed-only): sets
`FreezeMinAge = 0`, `FreezeMultixactMinAge = 0`, and forces Aggressive —
matching "all four ages zero ⇒ aggressive cutoffs" (`vacuum.c:404–417,
1244–1273`). The launcher's anti-wraparound pass sets Aggressive too.

## F3. FreezeLimit math + GUC/reloption consumption

Single helper used by both manual and auto paths:

```go
func computeFreezeCutoffs(nextXID, oldestXmin TransactionID,
    minAge, maxAge int64 /* GUC or reloption override */) (freezeBelow TransactionID) {
    eff := minAge
    if maxAge > 0 && maxAge/2 < eff { eff = maxAge / 2 }   // min(min_age, max_age/2)
    fb := nextXID - TransactionID(eff)
    if fb > oldestXmin { fb = oldestXmin }                  // clamp ≤ OldestXmin ALWAYS
    return fb                                               // (age-0 FREEZE => limit=OldestXmin,
                                                            //  NOT nextXID: unclamped nextXID
                                                            //  would freeze in-flight xmins)
}
}
```

- Manual path reads session `vacuum_freeze_min_age` (already wired via
  dispatch.go:1380) plus `autovacuum_freeze_max_age` for the cap; reloption
  overrides win when present (catalog fields already parsed).
- Launcher drops its hardcoded 50M and reads the same registry values.

## F4. Real autovacuum start + trigger formula

0. **Counter placement (review M2)**: the launcher cannot see per-session
   pending stats (flush() only fires from pg_stat_force_next_flush), so the
   three trigger counters live DIRECTLY in the shared pgstat relation store
   as sync/atomic values, incremented at commit-fold sites alongside
   deltaDead; launcher and SQL getters read the shared store — no periodic
   flush needed. Reset-vs-concurrent-DML lost updates are accepted noise
   (same class as upstream's approximate triggers).
1. **Start it**: server bootstrap (initdb/open.go where bg workers are
   registered) constructs `autovacuum.NewLauncher(...)` into
   `ServerConfig.AutovacuumLauncher`, gated on the new `autovacuum` GUC
   (default on). Registers itself in the activity registry as
   `"autovacuum launcher"` via the existing OnRunStart/OnRunEnd hooks.
   NapInterval = `autovacuum_naptime` (seconds→duration, default 60s);
   WorkerLimit stays serial this task (documented deviation; PG forks up to
   max_workers processes — goopg keeps one goroutine loop, max_workers
   registered inert for SHOW parity).
2. **Trigger inputs**: extend the executor pgstat relation store with two
   atomics per tracked relation:
   - `insSinceVacuum` (+1 per live INSERT),
   - `modSinceAnalyze` (+1 per INSERT/UPDATE/DELETE).
   `deltaDead` already exists. New SQL surfaces
   `pg_stat_get_ins_since_vacuum(oid)` / `pg_stat_get_mod_since_analyze(oid)`
   and `pg_stat_user_tables` stops emitting hardcoded zeros for these three.
3. **Reset points**: successful table VACUUM resets dead+insert counters;
   successful ANALYZE resets mod counter (executor paths call the pgstat
   store directly, mirroring `pgstat_relation.c:236–248`).
4. **Formula** in launcher tick (replacing needsVacuum/needsAnalyze):

```
reltuples = max(RowCount, 0); reloptions override GUCs when present
vacthresh = min(vacThreshold + vacScale*reltuples,
                autovacuum_vacuum_max_threshold)   // upstream caps (:3123)
dovacuum  = deadTuples > vacthresh
          || insSinceVacuum > insThreshold + insScale*reltuples
            // pcnt_unfrozen factor := 1 (no relallfrozen stat yet; deviation)
doanalyze = modSinceAnalyze > anlThreshold + anlScale*reltuples
wraparound = RelFrozenXID valid && nextXID − RelFrozenXID > autovacuum_freeze_max_age
force = wraparound (bypasses autovacuum_enabled=off, marks pass aggressive,
        suppresses SKIP_LOCKED behavior by using plain lock acquire)
```

Anti-wraparound candidates are processed first (ordered by oldest
RelFrozenXID), matching autovacuum.c:1145–1195 priority intent within our
single-worker model. `MinVacuumAge/MinAnalyzeAge` walls remain as a
debounce (PG has naptime spacing instead) but drop to 60s defaults so the
formula, not the clock, decides.

## F5. autoanalyze upgrade

Export a sampled-analyzer core FROM package executor (the real deps are
`analyzeRelationWith(pool, mgr, cat, tbl, target, rng, mxs, dsCtx)` plus
in-package datum rendering — nil-dsCtx fallback to ISO/MDY bounds already
works, so the launcher passes nil): `executor.AnalyzeRelationSampled(...)`
wrapping `operators_analyze.go:388–955`, called by the launcher
(postmaster already imports executor — no import cycle). Persistence keeps
the existing sidecar + pg_statistic write path. The simplified
`commands/vacuum.Analyze` remains for vacuum's reltuples estimate only.

## F6. Cost-based throttling

In vacuumCore's loop, after each processed block add cost accounting:

```go
if slotWasInPool { cost += pageHit } else { cost += pageMiss }
if pageDirty { cost += pageDirtyCost }
if cost >= limit && delayMs > 0 {
    sleep(min(delayMs * cost / limit, 4*delayMs)); cost = 0  // proportional, ≤4×delay
}
```

Values from GUCs (`vacuum_cost_limit` default 200, page_hit 1 / miss 10 /
dirty 20, `vacuum_cost_delay` default 0ms ⇒ no-op by default;
autovacuum passes may override via `autovacuum_vacuum_cost_delay/limit`).
Pool hit/miss is derivable from Pin timing (track whether Pin blocked on
I/O via the pool's existing stats hook if available; else approximate
miss = first-touch per block per pass). Keep the implementation honest but
simple: hit/miss decided by buffer-pool presence probe (`pool.InPool(tag)`
style check if available).

## F7. Truncation + relallvisible

- After the scan, when `vacuum_truncate` (GUC/reloption/param) allows,
  count trailing all-empty blocks and drop them via the EXISTING capability:
  `Manager.TruncateRelationTo(rel, n)` (storage/smgr.go:579, idempotent) +
  `Pool.FlushRel` / `InvalidateBlock(n..oldN)`; emit the smgr-truncate WAL
  record kind (encode/replay already exist in recovery.go:1195/:2306;
  wire the runtime emitter, falling back to documented no-WAL if the
  record cannot be produced safely). Conditional AccessExclusiveLock
  attempt mirrors `vacuumlazy.c:3197–3230`; on lock failure skip like PG.
- Publish `relallvisible` (count of VM-set blocks) alongside relpages/
  reltuples in UpdateRelStats' pg_class view row.

## F8. GUC registrations + sample sync

Register every missing GUC from 02 §3 with upstream names/boot values
(`internal/utils/misc/defaults.go`), marking engine-consumed ones properly;
inert ones get `FlagDisallowInFile`? NO — they must appear in
postgresql.conf.sample (operator parity), so register normally and update
the sample sections (§"Autovacuum", §"Cost-Based Vacuum Delay") in the same
commit; `TestSampleConfigCoversRegistry` enforces.

Consumed immediately: autovacuum(on/off master), autovacuum_naptime,
thresholds/scale factors (vac/analyze/insert), freeze ages (min/max/table
for xid; multixact registered-inert until F-multixact lands),
cost family. Registered-inert (SHOW/sample parity): max_workers,
autovacuum_work_mem, log_autovacuum_min_duration, failsafe ages,
multixact max/table ages.

## Explicit non-goals (deferred)

VACUUM FULL rewrite, CLUSTER reorder, partitioned-parent inherited stats,
failsafe/eager-scan machinery, parallel/multi-worker autovacuum,
multixact freeze bookkeeping (relminmxid) — each documented here as future
work with resume pointers to the audited sites.
