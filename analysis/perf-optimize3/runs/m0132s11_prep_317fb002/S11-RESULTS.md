# M0132-S11 perf acceptance — measured result

date: 2026-08-13 · goopg `317fb002` (M0132-S10) · PostgreSQL 18.3 · pgbench scale 100, c=50, T=120

Prepared-mode A/B against a **same-day, same-HEAD simple-mode control** (not the
historical `postdash_6e3b7a37` baseline, whose fsync floor has drifted a month).
Both runs are in `runs/m0132s11_{prep,simple}_317fb002/`.

## Headline (same day, same HEAD)

| workload | simple | prepared | gap |
|---|---:|---:|---:|
| `-S` (read) | 93,738 TPS | 72,857 TPS | prepared **−22.3%** |
| `-N` (write) | 10,158 TPS | 8,781 TPS | prepared **−13.6%** |

Historical baselines for reference (Jul 14): simple `-S` 89,955 / `-N` 9,898;
pre-M0132 prepared `-S` 84,739 / `-N` 6,749.

## Verdict against the three criteria

1. **`-N` ≥ simple 9,898 — NOT MET.** Prepared `-N` = 8,781 (and the same-day
   simple control is 10,158). The 2-fsync/txn bug *is* fixed (see criterion 3);
   the residual gap is per-statement re-parse/re-plan overhead, not the commit.
2. **`-S` > simple 89,955 — NOT MET, profile-explained (O-XP-1).** Prepared
   `-S` = 72,857 vs same-day simple 93,738. The profile locates the overhead in
   the extended-protocol message loop, not in the transaction machinery M0132
   touched.
3. **fsync back to ~one/txn-group — MET.** `aux2_fsync_probe.sh` `-N` under
   strace: 53,820 txns / 17,344 `fdatasync` = **0.32 fsync/txn** (≈3 txns per
   group commit), vs the pre-M0132 "2 fsyncs/txn". The commit is on `END`
   (per-statement: `END` 4.058 ms carries the fsync; `BEGIN`/`UPDATE`/`SELECT`/
   `INSERT` are all sub-ms).

## Where the residual gap lives (O-XP-1 profile)

`goopg_S.cpu.pb.gz` (prepared, 90 s in-run) top frames by cumulative:

- `handleExecuteFrame` → `executeExtendedQuery` → `FrameWriter.Flush` →
  `Syscall6` (result write) — 28.6% flat.
- `handleDescribeFrame` → `describeExtendedQuery` → `describeViaPlanner` —
  **13.4% cumulative** (`-S`), 8.8% (`-N`).
- `parser.Parse` — 6.2% cumulative (`-N`).
- `mvcc.(*Manager).captureSnapshot` — 0.7% flat (small; the feared
  `TxnMgr.Begin`/snapshot tax is *not* the bottleneck).

The `-N` per-statement delta vs simple is spread across every statement
(+0.06–0.08 ms each) plus +0.52 ms on `END` — i.e. per-statement overhead, not
extra commit work.

## Root cause

goopg's extended protocol has **no prepared-statement cache**: it re-parses SQL
on every `Execute` (`internal/server/dispatch_extended.go:40`,
`parser.Parse(query)`) and re-parses + re-plans on every `Describe`
(`internal/server/extended.go:686`, `describeViaPlanner`). PostgreSQL caches the
raw parse tree and plan at `Parse` and reuses them for `Describe`/`Execute`
(`postgres/src/backend/commands/prepare.c`,
`postgres/src/backend/tcop/pquery.c`). This is a pre-existing divergence
(not introduced by M0132), it is outside M0132's transaction-state scope, and
it is the whole of the residual prepared-vs-simple TPS gap once the 2-fsync bug
was removed. Filed as a deferral-ledger row + M0132-S13 follow-up.

## Provenance

`env.txt` (prepared run) and `env.txt` (simple run) carry git head, binary
sha256, GOMEMLIMIT=18GiB, and host state. Both runs uncapped (methodology
`00-methodology.md` parity requirement). Binary: `tmp/goopg-bench-bin-m0132s11`.
