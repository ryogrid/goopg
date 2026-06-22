Task: M0118-0009 `freeze-the-dead` — DONE this loop. Spec PROMOTED `failed`→`pass`.
Design 0118-0020. COMMITTED.

WHAT LANDED (VACUUM prune unification):
- ROOT CAUSE (sibling-path divergence): VACUUM `vacuumCore` reclaimed dead tuples
  with a naive `isDead = h.Xmax != Invalid && h.Xmax < horizon` + physical
  `VacuumHeapPageBySlots`. Two bugs: (1) compared a MultiXactId xmax to the xid
  horizon (category error → live, still-key-share-locked HOT chain root marked
  "dead"); (2) physically removed the chain ROOT line pointer (index points there),
  orphaning the live tip → updated row vanished from index scan (0 rows) + final
  seqscan. The opportunistic prune twin `storage.PagePruneOpt` was ALREADY
  multixact+HOT-aware (redirects dead roots to live tip) — VACUUM just never used it.
- FIX: extracted shared kernel `pagePruneCore(p, oldestXmin) (PruneResult, liveN, err)`
  in internal/storage/prune.go. `PagePruneOpt` keeps its pd_prune_xid gate + delegates;
  new `PageVacuumPrune` runs the kernel UNCONDITIONALLY (VACUUM must prune regardless
  of the hint) and returns surviving-LP_NORMAL count for Stats.Live.
- internal/vacuum/vacuum.go `vacuumCore`: replaced CollectDeadHeapSlots +
  VacuumHeapPageBySlots + LogHeapVacuum with PageVacuumPrune + LogHeapPruneOpt
  (RecordKindHeapPruneOpt — carries redirects+unused, replay already exists). Index-
  vacuum DeadTIDs now from `Unused` only (redirected roots keep valid index entry;
  HOT-only removed tuples have no index entry → no-op). stats.Live/Dead accumulated inline.
- Dedicated test TestPort_IsolationFreezeTheDead (internal/testport/isolation_port_test.go).
- CSV row 529 failed→pass (comma-free rationale, Go test func). Regen'd coverage +
  inventory md. Design 0118-0020 + README index. fix_plan M0118-0009 updated.

Gates (green): TestPort_IsolationFreezeTheDead PASS; -race storage/vacuum/wal/mvcc;
go test executor (prune/freeze/fsm/vacuum); isolation regression batch
(multixact-no-forget, delete-abort-savept{,-2}, aborted-keyrevoke,
lock-committed-{update,keyupdate}, inplace-inval) PASS; gofmt/vet clean; pgbench
smoke via pre-commit hook at commit.

NEXT loop candidates (remaining M0118-0009 misc, all need NEW subsystems):
intra-grant-inplace (ALTER TABLE ADD PK should `<waiting>` on a pg_class FOR KEY
SHARE catalog-row lock — CLOSE-ish, needs catalog row lock); stats (pg_stat_*);
horizons ($$-dollar-quoted EXPLAIN bodies choke the isolation lexer + EXPLAIN JSON);
async-notify (LISTEN/NOTIFY); prepared-transactions (2PC); subxid-overflow (plpgsql
EXCEPTION subxids); temp-schema-cleanup (pg_my_temp_schema). OR a different M0118
group (0118-0005 FK, 0118-0006 MERGE, 0118-0008 DDL/VACUUM).

GOTCHAS: never gofmt -w (go1.25 repo vs local 1.26). Isolation specs run goopg as a
SUBPROCESS. CSV rationale comma-free. tpch-spotcheck INFRA-BLOCKED; pgbench smoke is
the live guard. Untracked postgres/ + weekly_loc.* + requirements.txt are stray — leave.
