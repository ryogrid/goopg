# perf-optimize3 / 05 — Improvement design bundle

> **perf-optimize3-dash note (2026-07-13)**: C1's FPI-gating machinery (§4.2) is
> inherited by perf-optimize3-dash/03 (published-redo watermark, landed); C1's
> record-shape work is now the canonical **resume path** (re-enable with
> GOOPG_WAL_CANONICAL=on). C2/C3 are unaffected by the native-only default.

date: 2026-07-13 · designs against goopg `e453e3f2`+`b28e2a80` · status: design only
(implementation not started)

Detailed, implementation-ready designs for the fix candidates ranked in
[`../04-improvement-candidates.md`](../04-improvement-candidates.md), grounded
in the measurements of [`../01-results.md`](../01-results.md) (7.38× pgbench
`-N` write gap vs PostgreSQL 18.3; 1.96× read gap; `END` = 91 % of goopg's
write-transaction latency).

| Doc | Candidate | Depth | Slices |
|---|---|---|---|
| [01-c1-incremental-canonical-heap-wal.md](01-c1-incremental-canonical-heap-wal.md) | C1 — drop the per-record 8 KB FPI from canonical heap WAL; unify double logging | full | 9 |
| [02-c2-clog-commit-fsync-removal.md](02-c2-clog-commit-fsync-removal.md) | C2 — remove the commit-path pg_xact fsync | full | 4 |
| [03-c3-btree-lp-dead-on-access.md](03-c3-btree-lp-dead-on-access.md) | C3 — btree LP_DEAD on-access dead-entry cleanup | full | 5 |
| [04-c4-engine-tax-directions.md](04-c4-engine-tax-directions.md) | C4 — per-query engine tax | direction |
| [05-c5-pipelined-commit-groups.md](05-c5-pipelined-commit-groups.md) | C5 — pipelined commit groups | sketch |
| [06-observability-appendix.md](06-observability-appendix.md) | measurement gaps found by perf-optimize3 | appendix |

Each full design: problem/numbers → current-code map (file:line, verified by
exploration agents against `e453e3f2`) → PostgreSQL reference → target design +
decision log → invariants & failure modes → migration slices with per-slice
gates → test-impact matrix → performance verification → open questions
(flagged for the implementer, deliberately unresolved here).

## Common gates (referenced as G-* by every slice table)

| Gate | Command / suite |
|---|---|
| **G-race** | `go test -race ./internal/wal/ ./internal/mvcc/` (+ `./internal/access/btree/` for C3 slices) |
| **G-crash** | kill-9 / crash-recovery suites: `go test -run 'Crash|Recovery|Durability' ./internal/initdb/ ./internal/wal/`, `internal/testutil/cluster/crash_recovery_test.go` (`TestKillKillRecovery`) |
| **G-standby** | `TestE2E_FailoverGoopgToPG` (`internal/testport/e2e_failover_goopg_to_pg_test.go`, real PG 18 standby replaying goopg WAL) + `e2e_standby_attach_roundtrip_test.go` |
| **G-waldump** | `pg_waldump` parity: `internal/testport/pgwaldump_port_test.go`, `pgwaldump_savefullpage_test.go`, `pgwaldump_vacuum_prune_test.go`; unit `internal/wal/pg_waldump_compat_test.go` |
| **G-unit** | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`; pgbench smoke runs on every commit (never `--no-verify`) |
| **G-tpch** | `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical row counts) — required for DML-reachable slices |
| **G-perf** | `analysis/perf-optimize3/scripts/run_rw50.sh` + `aux2_fsync_probe.sh`; record results in `analysis/` with the commit hash |

**Gate activation requirements (a default `go test ./...` run silently skips
the headline gates):** G-standby requires a non-`-short` run, real PG 18
client binaries (`pg_basebackup`, `psql`) on PATH, and `GOOPG_SKIP_M0102_E2E`
unset. G-crash's e2e pieces (`e2e_standby_attach_roundtrip`,
`TestKillKillRecovery`) skip under `-short`. G-unit's `RALPH_PRECOMMIT_SCOPE=units`
deliberately skips the pgbench smoke — the commit hook runs it. A slice is NOT
green until the gates were actually *executed*.

## Sequencing

Restating `../04` with its storage-dependence caveat: **C2 first on this host
class** (small, self-contained; its win is proportional to the device fsync
floor — verify the floor on target hardware before committing to this order;
on low-latency NVMe, C1 leads outright). **C1 next** (the largest lever,
device-independent, sliced). **C3 in parallel with C1** (different subsystem).
C1-S1/S2 and the 06-appendix items are behavior-identical foundations that can
start immediately in parallel with C2.

## Cross-cutting risk register

- **X1 — Ordering**: as above. C1-S1/S2 ∥ C2 is safe (disjoint subsystems).
- **X2 — C1 × C2 coupling is perf-profile only.** C2's safety keys on
  page-LSN → `FlushUpTo` barriers; C1 changes WAL *volume*, not LSN
  association or monotonicity. But C1 shrinks drain per cycle → wider commit
  groups → larger CLOG batches per group → C2's dirty-page/checkpoint-burst
  profile shifts. Re-run G-perf after every landing; repeat C2-S4's burst
  measurement after C1-S6a (the HOT-update perf slice).
- **X3 — C3 × canonical stream.** C3's purge WAL record is a native
  vacuum-style record. Per `../03`, the unconditional-FPI canonical *btree*
  builder serves only system-catalog indexes — how user-index changes reach a
  real PG standby today is unresolved (open question O-C3-2). If user btree
  pages are not canonically replicated, C3-S4's purge is invisible to the
  standby by construction; if they are, the purge needs a canonical
  counterpart. Settle before C3-S4.
- **X4 — Shared gate contention.** All three candidates touch the
  crash-recovery suites and re-baseline pgbench numbers. Serialize *merges*
  (one candidate-slice at a time) even if development is parallel.
- **X5 — Land observability early.** The 06-appendix items (`pg_stat_wal`
  wiring, fsync counters, wait events) are the cheap way to watch X2/X5
  effects; land them alongside C1-S1/C2-S1.
- **X6 (rev 2) — One checkpoint-ordering invariant.** The first draft blessed
  today's order (flush → sample redo → record → reset FPI epoch at
  checkpointer.go:635). Adversarial review showed that order is **unsafe**
  for image gating: a page modified between the redo sample and the epoch
  reset emits no image yet replays from redo (C1 §5.1-F2 — latent today for
  the native family, fatal if extended to the canonical stream). The
  invariant both designs now reference:

  > **Checkpoint ordering invariant (rev 2)**: within `runCheckpoint`,
  > (0) **publish the RedoRecPtr first** (sampled from the writer's published
  > frontier at checkpoint start — PG `CreateCheckPoint` order; image-gating
  > decisions key on this published pointer, so there is no separately-timed
  > "epoch reset" event), **then** (1) flush + fsync all dirty data pages and
  > all dirty CLOG pages (`FlushCLOGFn`, error fails the checkpoint) — the
  > flush necessarily covers everything ≤ the published redo, **then**
  > (2) append + flush the checkpoint record carrying the published redo and
  > update pg_control. Consequences: pg_xact/data on disk cover everything
  > before redo (C2's reconstruction needs only post-redo WAL), and every
  > page's first image-gated record after the published redo carries a fresh
  > image (C1's torn-page cover). Implemented in C1-S3; C2 must not weaken
  > `FlushCLOGFn`'s error-fails-checkpoint contract.

## Supersession note (2026-07-13)

`analysis/perf-optimize3-dash/` (single-stream native-only WAL) partially
supersedes **C1**: real-PG-standby compat is deferred, canonical emission is
gated off by default, and C1's §4.2 gating machinery is inherited there as its
doc 03 (redo-publication fix — now mandatory and mode-independent). C1's
record-shape work (incremental `xl_heap_*`) becomes the **resume path** when
replication returns. **C2 and C3 are unaffected and remain live.**

## Relationship to prior work

The wal-backend-flush bundle (`docs/design/wal-backend-flush/`, implemented
2026-07-12) already gave goopg PG's WAL *locking* architecture
(backend-driven flush, emergent group commit). This bundle addresses what that
one deliberately did not: **what** is written per transaction (C1), the
**second** durable write on the commit path (C2), and the index-maintenance
write amplification (C3).
