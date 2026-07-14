# perf-optimize3-dash / 08 — Improvement design bundle

date: 2026-07-14 · designs against goopg `a640d2b0` (branch `wal-system-pgnize`)
· PostgreSQL 18.3 oracle (`postgres/local_install`)

## Landing status (2026-07-14 incremental run)

| doc | status | commit |
|---|---|---|
| 10 force-GC gate | **LANDED** (full) | `cf2b4770` |
| 05 BEGIN snapshot reuse | **PARTIAL** (RC first-stmt reuse; rest deferred) | `df8dc421` |
| 09, 07, 01, 02, 03, 04, 06, 08, 12 | **DEFERRED** — large multi-slice redesigns; land each in a focused session with its full gate set (see `.ralph/deferral_ledger.md`) | — |
| 11 protocol corking | **N/A** — bufio already coalesces reply frames into one flush per message; the 3-syscall premise is false | — |

The two landed measures were the safely-gateable wins in a single autonomous
pass; the deferred measures' *value-bearing* slices carry correctness risk
(transaction semantics, lock/WAL/buffer-pool concurrency, LP_DEAD marking) that
warrants dedicated verification budgets, and their foundation slices are inert.
Each doc below remains the authoritative implementation spec.

Detailed, implementation-ready designs for every improvement measure ranked in
the post-landing analyses [`../06-post-landing-analysis/`](../06-post-landing-analysis/)
(scale 100) and [`../07-scale500-analysis/`](../07-scale500-analysis/)
(scale 500), plus the enabler the two require. Each measure was, until now, a
one-line bullet in a "ranked next fixes" list; this bundle promotes each to a
full design with a verified current-code map, a PostgreSQL reference, a decision
log, invariants, migration slices, a test-impact matrix, and a performance-
verification recipe.

The anchor is **doc 01 — C5 drain/fsync split**, the full promotion of the
gated sketch [`../../perf-optimize3/05-improvement-designs/05-c5-pipelined-commit-groups.md`](../../perf-optimize3/05-improvement-designs/05-c5-pipelined-commit-groups.md)
Idea item 2. Its decision gate ("write the full design only if, after C1+C2
land, `selectgo` under `CommitTransaction` still dominates the `-N` block
delay") is **satisfied**: 06 measured 59.2 % and 07 measured 66.1 % of block
delay under `walWriteLock.acquireOrWait`.

## Measure inventory

| Doc | Measure | Source (rank) | Path | Depth |
|---|---|---|---|---|
| [01](01-c5-drain-fsync-split.md) | C5 §2 — two-phase drain-under-`writeMu` / fsync-outside with an LSN-ordered completion queue | 06-02 #2, 07-02 #2 | write | full |
| [02](02-bufferpool-miss-path-sharding.md) | Shard the buffer-pool miss path (`pinSlow`/`pinLoad`/`evictVictim` slot+partition mutexes); subsumes 06's per-file `readBlock`-mutex UPDATE finding | 07-02 #1, 06-02 #1 | write+read | full |
| [03](03-c3-rangescan-kill-migration.md) | Migrate the UPDATE-probe `RangeScan` callers to LP_DEAD kill collection (removes residual pkey doubling) | 06-02 #1, 07-02 #3 | write | medium |
| [04](04-subxact-clog-lanes.md) | Sub-transaction CLOG lanes + `pg_subtrans` parent fsync (C2 leftover) | 06-02 #4 | write | medium |
| [05](05-begin-snapshot-reuse.md) | Lazy first-statement snapshot for read-committed single-statement txns | 06-02 #3 | write | short |
| [06](06-fsm-getcandidates.md) | `storage.(*FSM).GetCandidates` scan cost under buffer pressure (4.9 % of `-N` CPU at scale 500) | 07-02 #5 | write | short |
| [07](07-lockmgr-global-mutex-sharding.md) | Shard the lockmgr global mutex by relation OID / fast-path reader locks (43–53 % of `-S` block delay) | 06-03 #1, 07-02 #4 | read | full |
| [08](08-operator-tree-reuse.md) | Reset-and-rebind executor state / generic-plan reuse (~10 % of `-S` CPU) | 06-03 #2 | read | full |
| [09](09-extended-protocol-explicit-txn.md) | **Enabler**: explicit transactions across Execute messages + prepared-statement plan reuse (also the plan+parse-ceiling measure) | 06-03 #3 + prepared-mode | read | full |
| [10](10-force-gc-gating.md) | Gate `maybeForceGCAfterCommit` to write transactions (5.4–5.9 % of `-S` CPU) | 06-03 #4 | read | short |
| [11](11-protocol-syscall-corking.md) | writev-cork DataRow+CommandComplete+ReadyForQuery; slim per-frame bookkeeping | 06-03 #5 | read | medium |
| [12](12-allocation-volume-reduction.md) | Arena/pool the ~14–15 KB/query per-query state that feeds the GC tax | 06-03 #6 | read | medium |
| [13](13-benchmark-prepared-mode.md) | Methodology addendum: the `-M prepared` harness change, the auto-commit interaction, and how doc 09 makes `-N` PG-faithful | — | — | — |

## Common gates (referenced as G-* by every slice table)

Inherited verbatim from [`../../perf-optimize3/05-improvement-designs/README.md`](../../perf-optimize3/05-improvement-designs/README.md):

| Gate | Command / suite |
|---|---|
| **G-race** | `go test -race` on the touched packages (WAL/MVCC/btree changes: `./internal/wal/ ./internal/mvcc/ ./internal/access/btree/`) |
| **G-crash** | kill-9 / crash-recovery: `go test -run 'Crash\|Recovery\|Durability' ./internal/initdb/ ./internal/wal/` + `TestKillKillRecovery` |
| **G-standby** | `TestE2E_FailoverGoopgToPG` + `e2e_standby_attach_roundtrip_test.go` (real PG 18 replaying goopg WAL) |
| **G-waldump** | `pg_waldump` parity suite (`internal/testport/pgwaldump_*`, `internal/wal/pg_waldump_compat_test.go`) |
| **G-unit** | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`; per-commit pgbench smoke (never `--no-verify`) |
| **G-tpch** | `scripts/tpch-spotcheck.sh` (Q12=2/Q13 canonical row counts) — required for DML-reachable slices |
| **G-perf** | `analysis/perf-optimize3/scripts/run_rw50.sh` + `aux2_fsync_probe.sh` (**now `-M prepared`**, see doc 13); record with commit hash |

A slice is **not green until the gates were actually executed** — a plain
`go test ./...` silently skips G-standby/G-crash e2e pieces (they need a
non-`-short` run with PG 18 client binaries on PATH).

Docs that touch transaction/lock/visibility semantics additionally reference
**D-002** — the multi-session **isolation** suite (`internal/testport/`
isolation specs, the `postgres-oracle-port-status.csv` D-002 deferred suite):
run the affected specs and compare `<waiting>`/row-output against PG 18.3.

## Sequencing

The measures are largely independent (disjoint subsystems), so development can
parallelize; **merges serialize** on the shared crash-recovery + G-perf gates
(risk X4 below). Recommended landing order by leverage-per-risk:

1. **Doc 10 (force-GC gate)** and **doc 05 (BEGIN snapshot)** — smallest,
   behavior-narrow, immediate read/write latency shavings; land first as warm-ups.
2. **Doc 09 (extended-protocol explicit-txn)** — the enabler; unblocks a
   PG-faithful `-M prepared` write-path measurement for everything after it,
   and by itself removes the plan+parse ceiling (06-03 §3).
3. **Doc 07 (lockmgr sharding)** — the single largest read-path block-delay
   sink; independent of the WAL work.
4. **Doc 01 (C5 drain/fsync split)** — the largest write-path lever and the
   highest-risk concurrency redesign; land after the observability from the
   others is in place. Depends on nothing but the landed wal-backend-flush
   bundle (`docs/design/wal-backend-flush/`).
5. **Doc 02 (miss-path sharding)** — matters most under buffer pressure (07);
   independent, can parallel doc 01 (different package).
6. Docs 03/04/06/08/11/12 — parallel, subsystem-disjoint.

## Cross-cutting risk register

- **X1 — C5 × the wal-backend-flush invariants.** Doc 01 re-opens the
  `writeMu`-holds-across-drain-and-fsync structure that the wal-backend-flush
  bundle deliberately built (emergent group commit). It MUST preserve the
  `writeLSN ≥ drainedLSN ≥ flushedLSN` invariant
  (`docs/design/wal-backend-flush/04-concurrency-and-invariants.md` §4.5) and
  the WAL-before-data contract (§4.6). Re-run G-race's `TestDrainSafetyStress`
  and the full G-crash suite on every C5 slice.
- **X2 — Doc 09 × deferred-constraint commit-time checks.** goopg's simple-query
  COMMIT path wires deferred FK/UNIQUE checks (M0119-0004); the extended path
  has no equivalent because multi-statement explicit txns were never modelled.
  Doc 09 must add that hook, not just the transaction state machine — otherwise
  `-M prepared` silently skips deferred constraints. G-tpch + the FK isolation
  specs are the gate.
- **X3 — Lock-sharding correctness (docs 02, 07).** Splitting a global mutex
  into partitions must preserve the deadlock-freedom and fairness properties
  the single mutex provided. Both docs carry a deadlock-order invariant and a
  `-race` + isolation-suite gate.
- **X4 — Shared gate contention.** Every write-path doc re-baselines G-perf and
  touches crash-recovery suites. Serialize *merges* even when development is
  parallel; re-run G-perf after each landing (the group-commit width shifts
  when any commit-path cost changes).
- **X5 — Prepared-mode measurement validity.** Until doc 09 lands, the `-M
  prepared` harness (doc 13) measures goopg's auto-commit-per-statement grouping
  on `-N`; the write-path numbers in docs 01–06 that are cited from the
  interim prepared runs must be labeled as such. The read path (`-S`) is valid
  immediately.
