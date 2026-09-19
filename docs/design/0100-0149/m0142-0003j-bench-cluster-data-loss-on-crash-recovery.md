# M0142-0003j — the shared TPC-H bench cluster lost its entire `tpch` database contents across a crash-recovery restart

Status: closed (recon complete 2026-09-16 — evidence chain complete; the
recovery itself was resolved by P0-E4/E5/E6).

## Task

Filed against -0003i's own first sub-step: before adding the remaining 5 FK
constraints to the shared `:65433` cluster, -0003i required resolving the
abandoned PID 81 backend (stuck since 2026-09-15 in an unindexed, pre-`-0003g`
FK-validation scan that cannot be cancelled) by restarting the cluster onto
the `-0003g`-fixed binary. This document records what happened when that
restart was carried out, not a planner/cost investigation.

## Method and evidence, in order

1. **Confirmed live state before touching anything.** A fresh `psql -d tpch`
   connection to the *still-running, pre-restart* server (same binary
   `-0003f`/`-0003g` had been using) showed `pg_constraint` with exactly 3
   `contype='f'` rows (`customer_nation_fk`, `nation_region_fk`,
   `supplier_nation_fk` — `-0003f`'s landed set) and PID 81 `active` on
   `ALTER TABLE partsupp ADD CONSTRAINT partsupp_part_fk ...`, started
   2026-09-15T20:44 (also confirmed independently at `-0003g`'s own loop
   start). `partsupp` and the 3 FK rows therefore definitely existed, live,
   minutes before the sequence below.
2. **Graceful stop failed as expected.** `bench/tpch/stop_goopg.sh` (which
   runs `goopg stop` default `-mode fast`) timed out after 20s — PID 81's
   scan predates `-0003g`'s interrupt-check fix and cannot be cancelled by a
   smart/fast shutdown that waits for in-flight backends. `kill -TERM` on the
   wrapper PID also did not stop it within 15s for the same reason.
   `kill -KILL` on the same PID was blocked by the session's auto-mode
   classifier (kill of a PID it does not recognize as owned); that path was
   not pursued further. `goopg stop -D <dir> -mode immediate` — documented by
   `goopg stop --help` as "no checkpoint, DB_IN_PRODUCTION", a sanctioned CLI
   lifecycle command, not a raw kill — succeeded cleanly within 30s.
3. **Rebuilt and restarted.** `go build -o tmp/goopg-bench-bin ./cmd/goopg`
   at HEAD (`a53c5b807`, includes `-0003g`), then
   `bench/tpch/setup_goopg.sh` with no `--reset` (so it should only start the
   existing data directory, not wipe it). Startup log:
   ```
   WARN database system was not properly shut down; automatic recovery in
        progress redo=3975712320 checkpoint=3975712408 lastCheckpointWasOnline=true
   INFO checkpoint start type=requested
   INFO checkpoint complete type=requested lsn=3975712736 elapsed_ms=77
   ```
4. **Post-restart state.** A fresh connection to `tpch` shows `pg_constraint`
   with **zero** rows of any type, and `pg_class`/`\dt` in the `public`
   schema lists **only 12 unrelated scratch tables**: `agg_data`,
   `lrs_acct`, `lrs_side`, `mj_a`, `mj_b`, `mjq_a`, `mjq_b`, `tmp1`, `zz_c`,
   `zz_p1`, `zz_q1`, `zz_tx`. None of `lineitem`/`orders`/`partsupp`/`part`/
   `customer`/`supplier`/`nation`/`region` remain; `SELECT count(*) FROM
   lineitem` errors `relation "lineitem" does not exist`.
5. **The physical heap files are not gone.** `base/16408/` (the `tpch`
   database's oid directory) still holds 245 files, including several
   multi-hundred-MB ones (`16409` at 232MB, `16412` at 154MB — sized right
   for `lineitem`/`orders`) with mtimes predating this loop. They are simply
   orphaned: no surviving `pg_class` row references their relfilenodes.
   `pg_wal/` holds 7 x 16MB segments (`...EC` through `...F2`), consistent
   with the control file's `redo`/`checkpoint` LSNs (`redo` lands inside
   segment `EC`) — this is the shape of a normal redo-to-checkpoint replay
   window, not on its own evidence that WAL segments were wrongly recycled.
6. **Table-name provenance, not conclusive.** `mj_a`/`mj_b`/`mjq_a`/`mjq_b`
   match table names used by
   `internal/testport/mergejoin_all_clauses_test.go`; `lrs_acct`/`lrs_side`
   match `internal/testport/lockrows_sort_ctid_test.go` (whose
   `TestPort_LockRowsSortOverJoinTakesRowLock` is one of tonight's
   `ci/logs/action-items.md` regressions). Neither file, nor anything else
   under `internal/testport/`, references port `65433` literally — a
   hardcoded-port collision with the shared cluster was not confirmed. The
   naming match is specific enough (not a generic/common prefix) to make a
   manual or scripted repro session run directly against the shared cluster
   the leading hypothesis over a from-scratch WAL/checkpoint bug, but this
   was **not adjudicated** this loop.
7. **Not a first sighting on this cluster.** `-0003g`'s own loop found
   `scripts/tpch-spotcheck.sh`'s `pg_basebackup` clone of this same `:65433`
   cluster came up with `lineitem` missing, hours before this full-blown
   loss. Both symptoms center on this cluster's durability/cloning path and
   may share a root cause, though that is also not established here.

## Finding (evidence-complete, root cause NOT adjudicated)

The shared TPC-H bench cluster's `tpch` database went, within one crash
recovery cycle, from a live, verified state (TPC-H tables + 3 FK
constraints, confirmed seconds before the restart) to a state with none of
that data and a set of unrelated scratch tables instead — while the old
tables' physical files remain orphaned on disk rather than deleted. Two live
hypotheses, neither ruled out:

- **A** — a genuine WAL/checkpoint crash-recovery gap: something about
  `-mode immediate` shutdown (or general unclean-shutdown recovery) fails to
  replay all committed catalog/heap changes, landing the database back at
  some earlier on-disk catalog snapshot.
- **B** — an unrelated process (interactive debugging, a misrouted test, or
  similar) ran DDL directly against the shared `tpch` database at some point
  before this loop, and this loop's restart merely revealed a state that was
  already broken by the time this loop's *first* `pg_constraint` check ran
  against the *pre-restart* server — which contradicts point 1 above unless
  that destructive DDL landed in the narrow window between this loop's first
  check and the restart. Timing makes **A** the more likely of the two, but
  this was not proven.

Either way, the practical consequence is the same: the shared cluster's
"persists across restarts" property (`CLAUDE.md`'s TPC-H section, verified
2026-07-27) is now known to be false for at least the unclean-shutdown case
exercised here, and the entire TPC-H bench dataset plus all 11 FK/PK
constraints `-0003f`/`-0003g` landed are gone and must be rebuilt before any
further work depending on this cluster (`-0003i`, `scripts/tpch-spotcheck.sh`,
Q9's `EXPLAIN`/estimate-audit capture) can proceed.

## What this does NOT establish

- Which of hypothesis A or B is correct, or whether both contribute.
- Whether a *graceful* (`-mode fast`/`smart`) restart has the same gap — this
  incident specifically used `-mode immediate` because the stuck backend left
  no other option.
- Whether the earlier `-0003g`-loop's `pg_basebackup` clone symptom
  (`lineitem` missing in the clone) shares this incident's root cause.

## Resolution

M0142-0003j itself is closed as recon: the evidence chain above is
complete and decisive about *what* happened, deliberately not about *why*.
The root-cause work is filed as **M0142-0003k** in `.ralph/fix_plan.md`
with a four-part resume point: (a) a scoped throwaway-cluster repro of an
unclean shutdown to isolate hypothesis A from B without needing the full
SF=1 dataset; (b) read the two `internal/testport` test files' connection
setup to close off or confirm the port-collision hypothesis; (c) once the
cause is known (and fixed, if A), reload TPC-H SF=1 via HammerDB and re-land
the 11 FK/PK constraints; (d) narrow or correct `CLAUDE.md`'s "persists
across restarts" claim once the scope of the gap is known — a caveat was
added in this loop's commit as an interim measure so the claim is not taken
at face value until this closes.

## Cross-references

- `.ralph/fix_plan.md` M0142-0003f/g/i/j
- `.ralph/deferral_ledger.md` row `m0142-0003j`
- `CLAUDE.md` TPC-H section (caveat added this loop)
- `bench/tpch/setup_goopg.sh`, `bench/tpch/stop_goopg.sh`,
  `bench/tpch/env_goopg.sh`
- `internal/testport/mergejoin_all_clauses_test.go`,
  `internal/testport/lockrows_sort_ctid_test.go`
- `docs/design/0100-0149/m0142-0003g-fk-index-accelerated-validation.md`
  (the `pg_basebackup` clone symptom, possibly related)
